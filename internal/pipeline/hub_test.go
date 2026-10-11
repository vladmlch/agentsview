package pipeline

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	gosync "sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/pipevents"
)

// recvEvent reads one event off a subscription channel. The hub
// delivers synchronously, so a generous wall-clock budget only guards
// against a regression that stops delivery entirely.
func recvEvent(t *testing.T, ch <-chan pipevents.Event) pipevents.Event {
	t.Helper()
	select {
	case e, ok := <-ch:
		require.True(t, ok, "subscription closed unexpectedly")
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pipeline event")
		return pipevents.Event{}
	}
}

// TestHubRunRecording is the spec walk: begin a run, observe three
// events, end the run. The run log stamps seq and run id, the JSONL
// recording round-trips through the pipevents codec, and a subscriber
// replaying from afterSeq=1 sees exactly the events after seq 1.
func TestHubRunRecording(t *testing.T) {
	dir := t.TempDir()
	h := NewHub(dir)

	require.NoError(t, h.BeginRun("r1", "happy-path", "/src/rollout.jsonl"))
	h.ObserveEvent(pipevents.Event{
		Stage: pipevents.StageWatch, Kind: pipevents.KindFSEvent,
		Path: "/x/rollout-1.jsonl",
	})
	h.ObserveEvent(pipevents.Event{
		Stage: pipevents.StageGates, Kind: pipevents.KindGateVerdict,
		Path:    "/x/rollout-1.jsonl",
		Payload: map[string]any{"gate": "skip_cache", "decision": "pass"},
	})
	h.ObserveEvent(pipevents.Event{
		Stage: pipevents.StageWrite, Kind: pipevents.KindTxCommitted,
	})

	// A subscriber joining after seq 1 replays the buffered backlog.
	sub, unsub := h.Subscribe(1)

	h.EndRun(map[string]any{"files": 1})

	// RunEvents returns the run's recorded events with stamped seq and
	// run id, including the run_started/run_finished bookends.
	evs := h.RunEvents("r1")
	require.Len(t, evs, 5)
	for i, e := range evs {
		assert.Equal(t, uint64(i+1), e.Seq, "event %d seq", i)
		assert.Equal(t, "r1", e.RunID, "event %d run_id", i)
	}
	assert.Equal(t, pipevents.KindRunStarted, evs[0].Kind)
	assert.Equal(t, pipevents.KindFSEvent, evs[1].Kind)
	assert.Equal(t, pipevents.KindGateVerdict, evs[2].Kind)
	assert.Equal(t, pipevents.KindTxCommitted, evs[3].Kind)
	assert.Equal(t, pipevents.KindRunFinished, evs[4].Kind)
	assert.Equal(t, 1, evs[4].Payload["files"])

	// The recording file exists and round-trips through the codec.
	f, err := os.Open(filepath.Join(dir, "r1.jsonl"))
	require.NoError(t, err)
	defer f.Close()
	hdr, recEvs, err := pipevents.ReadRecording(f)
	require.NoError(t, err)
	assert.Equal(t, "r1", hdr.RunID)
	assert.Equal(t, "happy-path", hdr.ScenarioID)
	assert.Equal(t, "/src/rollout.jsonl", hdr.SourceFile)
	// Compare via JSON: decoding normalizes numbers to float64, so the
	// round-tripped events differ from the in-memory ones only in
	// payload numeric types.
	wantJSON, err := json.Marshal(evs)
	require.NoError(t, err)
	gotJSON, err := json.Marshal(recEvs)
	require.NoError(t, err)
	assert.JSONEq(t, string(wantJSON), string(gotJSON))

	// The subscriber sees seq 2..4 replayed, then seq 5 live.
	for want := uint64(2); want <= 5; want++ {
		e := recvEvent(t, sub)
		assert.Equal(t, want, e.Seq)
		assert.Equal(t, "r1", e.RunID)
	}

	// Nothing else arrives; unsubscribing detaches the channel.
	unsub()
	var extra []pipevents.Event
	for e := range sub {
		extra = append(extra, e)
	}
	assert.Empty(t, extra)
}

// TestHubSubscriberReceivesLiveEvents covers the live path: a
// subscription made before BeginRun receives every event as it is
// observed, including the bookends.
func TestHubSubscriberReceivesLiveEvents(t *testing.T) {
	h := NewHub(t.TempDir())
	sub, unsub := h.Subscribe(0)
	defer unsub()

	require.NoError(t, h.BeginRun("r1", "s", ""))
	e := recvEvent(t, sub)
	assert.Equal(t, pipevents.KindRunStarted, e.Kind)
	assert.Equal(t, "r1", e.RunID)
	assert.NotZero(t, e.AtMS, "hub stamps AtMS when producer leaves 0")

	h.ObserveEvent(pipevents.Event{
		Stage: pipevents.StageParse, Kind: pipevents.KindParseStarted,
	})
	e = recvEvent(t, sub)
	assert.Equal(t, uint64(2), e.Seq)

	h.EndRun(nil)
	e = recvEvent(t, sub)
	assert.Equal(t, pipevents.KindRunFinished, e.Kind)
}

// TestHubDropsOnFullSubscriber proves delivery never blocks: a
// subscriber that never drains still caps at its 1024-event buffer,
// and the newest events are the ones dropped.
func TestHubDropsOnFullSubscriber(t *testing.T) {
	h := NewHub(t.TempDir())
	sub, unsub := h.Subscribe(0)
	defer unsub()

	const total = subscriberBufferCap + 30
	for i := 0; i < total; i++ {
		h.ObserveEvent(pipevents.Event{
			Stage: pipevents.StageWatch, Kind: pipevents.KindFSEvent,
		})
	}

	require.Equal(t, subscriberBufferCap, len(sub))
	first := recvEvent(t, sub)
	assert.Equal(t, uint64(1), first.Seq)
	var last uint64
	for len(sub) > 0 {
		last = (<-sub).Seq
	}
	assert.Equal(t, uint64(subscriberBufferCap), last)
}

// TestHubConcurrentObserve hammers ObserveEvent from many goroutines;
// every event must land exactly once with a unique seq. Run with the
// race detector this guards the mutex discipline.
func TestHubConcurrentObserve(t *testing.T) {
	h := NewHub(t.TempDir())
	require.NoError(t, h.BeginRun("r1", "s", ""))

	const workers, per = 8, 250
	var wg gosync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				h.ObserveEvent(pipevents.Event{
					Stage: pipevents.StageWrite,
					Kind:  pipevents.KindMessagesWritten,
					Path:  fmt.Sprintf("/w/%d", w),
				})
			}
		}(w)
	}
	wg.Wait()
	h.EndRun(nil)

	evs := h.RunEvents("r1")
	require.Len(t, evs, workers*per+2)
	seen := make(map[uint64]bool, len(evs))
	for _, e := range evs {
		assert.False(t, seen[e.Seq], "duplicate seq %d", e.Seq)
		seen[e.Seq] = true
		assert.Equal(t, "r1", e.RunID)
	}
	assert.Equal(t, uint64(workers*per+2), evs[len(evs)-1].Seq)
}

// TestHubBeginRunConflicts covers window bookkeeping failures: a second
// active run, a reused id, and unsafe ids are all rejected.
func TestHubBeginRunConflicts(t *testing.T) {
	h := NewHub(t.TempDir())
	require.NoError(t, h.BeginRun("r1", "s", ""))
	assert.Error(t, h.BeginRun("r2", "s", ""),
		"second run while one is active")
	h.EndRun(nil)
	assert.Error(t, h.BeginRun("r1", "s", ""), "run id reuse")
	assert.Error(t, h.BeginRun("../evil", "s", ""), "traversal run id")
	assert.Error(t, h.BeginRun("", "s", ""), "empty run id")
}

// TestHubEndRunWithoutRun is a no-op, not a crash.
func TestHubEndRunWithoutRun(t *testing.T) {
	h := NewHub(t.TempDir())
	assert.NotPanics(t, func() { h.EndRun(nil) })
	assert.Empty(t, h.Runs())
}

// TestHubRunsListing exercises the Runs summary: begin order, state
// transitions, and the per-run event count including bookends.
func TestHubRunsListing(t *testing.T) {
	h := NewHub(t.TempDir())
	require.NoError(t, h.BeginRun("r1", "happy-path", "/f.jsonl"))

	runs := h.Runs()
	require.Len(t, runs, 1)
	assert.Equal(t, "r1", runs[0].RunID)
	assert.Equal(t, "happy-path", runs[0].ScenarioID)
	assert.Equal(t, RunStateRunning, runs[0].State)
	assert.Equal(t, 1, runs[0].EventCount, "run_started counts")
	assert.NotZero(t, runs[0].StartedAt)

	h.ObserveEvent(pipevents.Event{
		Stage: pipevents.StageParse, Kind: pipevents.KindParseDone,
	})
	h.EndRun(nil)

	runs = h.Runs()
	require.Len(t, runs, 1)
	assert.Equal(t, RunStateFinished, runs[0].State)
	assert.Equal(t, 3, runs[0].EventCount)

	require.NoError(t, h.BeginRun("r2", "live-append", ""))
	runs = h.Runs()
	require.Len(t, runs, 2)
	assert.Equal(t, "r1", runs[0].RunID)
	assert.Equal(t, "r2", runs[1].RunID)
}

// TestRecordingReadableMidRun is the crash-survival property: with the
// run still open (no EndRun — the process "died"), the recording file
// must already parse, because every event was flushed on write.
func TestRecordingReadableMidRun(t *testing.T) {
	dir := t.TempDir()
	h := NewHub(dir)
	require.NoError(t, h.BeginRun("r1", "s", "/f.jsonl"))
	h.ObserveEvent(pipevents.Event{
		Stage: pipevents.StageParse, Kind: "e1",
	})
	h.ObserveEvent(pipevents.Event{
		Stage: pipevents.StageParse, Kind: "e2",
	})

	f, err := os.Open(filepath.Join(dir, "r1.jsonl"))
	require.NoError(t, err)
	hdr, evs, err := pipevents.ReadRecording(f)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.Equal(t, "r1", hdr.RunID)
	require.Len(t, evs, 3)
	assert.Equal(t, "e2", evs[2].Kind)
}

// TestHubObserveOutsideRun: events with no active run get a seq but no
// run id, still fan out, and do not appear in any run's log.
func TestHubObserveOutsideRun(t *testing.T) {
	h := NewHub(t.TempDir())
	sub, unsub := h.Subscribe(0)
	defer unsub()

	h.ObserveEvent(pipevents.Event{
		Stage: pipevents.StageWatch, Kind: "stray",
	})
	e := recvEvent(t, sub)
	assert.Equal(t, uint64(1), e.Seq)
	assert.Empty(t, e.RunID)
	assert.Nil(t, h.RunEvents("nope"))
}

// TestHubUnsubscribeClosesAndDetaches: unsubscribe is idempotent, the
// buffered backlog still drains after close, and later events cannot
// panic on the detached channel.
func TestHubUnsubscribeClosesAndDetaches(t *testing.T) {
	h := NewHub(t.TempDir())
	sub, unsub := h.Subscribe(0)

	h.ObserveEvent(pipevents.Event{
		Stage: pipevents.StageWatch, Kind: "a",
	})
	unsub()
	assert.NotPanics(t, unsub, "second unsubscribe is a no-op")
	assert.NotPanics(t, func() {
		h.ObserveEvent(pipevents.Event{
			Stage: pipevents.StageWatch, Kind: "b",
		})
	})

	e := recvEvent(t, sub)
	assert.Equal(t, "a", e.Kind)
	_, ok := <-sub
	assert.False(t, ok, "closed channel drains then reports closed")
}

// TestHubWithoutRecordingsDir: an empty dir disables file recording
// while runs still stamp ids and keep the in-memory log.
func TestHubWithoutRecordingsDir(t *testing.T) {
	h := NewHub("")
	require.NoError(t, h.BeginRun("r1", "s", ""))
	h.ObserveEvent(pipevents.Event{
		Stage: pipevents.StageWatch, Kind: "e",
	})
	h.EndRun(nil)
	assert.Len(t, h.RunEvents("r1"), 3)

	names, err := h.rec.ListRecordings()
	require.NoError(t, err)
	assert.Empty(t, names)
}

// TestRecorderListReadImport covers the recording file manager:
// listing, reading a stored recording back, and importing a stream.
func TestRecorderListReadImport(t *testing.T) {
	dir := t.TempDir()
	rec := NewRecorder(dir)

	names, err := rec.ListRecordings()
	require.NoError(t, err)
	assert.Empty(t, names)

	var buf bytes.Buffer
	hdr := pipevents.RecordingHeader{RunID: "imported", StartedAtMS: 7}
	evs := []pipevents.Event{
		{Seq: 1, Stage: pipevents.StageParse, Kind: "k"},
	}
	require.NoError(t, pipevents.WriteRecording(&buf, hdr, evs))
	require.NoError(t, rec.ImportRecording("imported.jsonl", &buf))

	names, err = rec.ListRecordings()
	require.NoError(t, err)
	assert.Equal(t, []string{"imported.jsonl"}, names)

	gotH, gotEvs, err := rec.ReadRecording("imported.jsonl")
	require.NoError(t, err)
	assert.Equal(t, "imported", gotH.RunID)
	require.Len(t, gotEvs, 1)
	assert.Equal(t, "k", gotEvs[0].Kind)
}

// TestRecorderRejectsBadInput pins the validation: unsafe or
// non-recording names and unparseable content fail without leaving
// files behind.
func TestRecorderRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	rec := NewRecorder(dir)

	t.Run("unsafe names", func(t *testing.T) {
		for _, name := range []string{
			"../escape.jsonl", "a/b.jsonl", `a\b.jsonl`,
			"noext", ".jsonl", ".hidden.jsonl", "",
		} {
			err := rec.ImportRecording(
				name, bytes.NewBufferString("{}\n"))
			assert.Error(t, err, "name %q", name)
			_, _, err = rec.ReadRecording(name)
			assert.Error(t, err, "read name %q", name)
		}
	})

	t.Run("invalid content", func(t *testing.T) {
		err := rec.ImportRecording(
			"bad.jsonl", bytes.NewBufferString("not json\n"))
		assert.Error(t, err)
		_, statErr := os.Stat(filepath.Join(dir, "bad.jsonl"))
		assert.True(t, os.IsNotExist(statErr),
			"rejected import leaves no file")
	})
}
