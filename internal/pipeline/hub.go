// Package pipeline implements the ingest pipeline playground: an
// event hub that fans pipevents.Event out to live subscribers and
// JSONL run recordings, plus (in later files) a sandboxed ingest
// engine and scripted scenarios that drive it.
package pipeline

import (
	"fmt"
	gosync "sync"
	"time"

	"go.kenn.io/agentsview/internal/pipevents"
)

// subscriberBufferCap is the per-subscriber event buffer. A slow
// subscriber may fall this many events behind before the hub starts
// dropping events for it; producers never wait on subscriber drains.
const subscriberBufferCap = 1024

// RunInfo.State values.
const (
	// RunStateRunning marks a run whose recording window is open.
	RunStateRunning = "running"
	// RunStateFinished marks a run closed by EndRun.
	RunStateFinished = "finished"
)

// RunInfo summarizes one run for the runs listing.
type RunInfo struct {
	RunID      string `json:"run_id"`
	ScenarioID string `json:"scenario_id"`
	State      string `json:"state"`
	// StartedAt is the run start in unix milliseconds.
	StartedAt  int64 `json:"started_at"`
	EventCount int   `json:"event_count"`
}

// Hub fans pipeline events out to SSE subscribers and the active run
// recorder. It implements pipevents.Observer: ObserveEvent is safe to
// call from sync workers and watcher goroutines and never blocks on a
// subscriber.
type Hub struct {
	mu   gosync.Mutex
	subs map[chan pipevents.Event]struct{}
	seq  uint64
	// log holds every observed event in seq order. Subscribe replays
	// it and RunEvents filters per run. It grows for the life of the
	// process, which is acceptable for the playground's short runs.
	log []pipevents.Event

	runs   *runLog
	active *runRecord
	rec    *Recorder
}

var _ pipevents.Observer = (*Hub)(nil)

// runLog indexes every run the hub has seen, in BeginRun order.
type runLog struct {
	order []string
	byID  map[string]*runRecord
}

// runRecord is one run's bookkeeping: its listing entry, its recorded
// events, and its streaming recorder (nil when recording is disabled).
type runRecord struct {
	info   RunInfo
	events []pipevents.Event
	w      *runRecorder
}

// NewHub creates a hub that stores run recordings under
// recordingsDir. An empty dir disables file recording: runs still
// stamp run ids and keep the in-memory log, but no files are written.
func NewHub(recordingsDir string) *Hub {
	return &Hub{
		subs: make(map[chan pipevents.Event]struct{}),
		runs: &runLog{byID: make(map[string]*runRecord)},
		rec:  NewRecorder(recordingsDir),
	}
}

// ObserveEvent implements pipevents.Observer. It stamps Seq (a global
// monotonically increasing id), the active RunID when a run window is
// open, and AtMS when the producer left it zero; appends the event to
// the run log and the streaming recorder; then fans out to
// subscribers. Delivery is non-blocking: a full subscriber buffer
// drops the event for that subscriber only.
func (h *Hub) ObserveEvent(e pipevents.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.observeLocked(e)
}

// observeLocked stamps and records e. BeginRun and EndRun call it with
// h.mu already held so their run_started/run_finished bookends land
// atomically at the window edges, before any concurrent worker event.
func (h *Hub) observeLocked(e pipevents.Event) {
	h.seq++
	e.Seq = h.seq
	if e.AtMS == 0 {
		e.AtMS = time.Now().UnixMilli()
	}
	if h.active != nil {
		e.RunID = h.active.info.RunID
		h.active.events = append(h.active.events, e)
		h.active.info.EventCount++
		if h.active.w != nil {
			h.active.w.writeEvent(e)
		}
	}
	h.log = append(h.log, e)
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// BeginRun opens a recording window: events until EndRun are tagged
// with runID and written to <recordingsDir>/<runID>.jsonl. It emits a
// run_started event as the window's first entry. Only one run may be
// active at a time, and a run id may not be reused.
func (h *Hub) BeginRun(runID, scenarioID, sourceFile string) error {
	if !isSafeRunName(runID) {
		return fmt.Errorf(
			"pipeline: unsafe run id %q: want 1-128 of [A-Za-z0-9._-], "+
				"not starting with '.'", runID)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active != nil {
		return fmt.Errorf("pipeline: run %q still active",
			h.active.info.RunID)
	}
	if _, dup := h.runs.byID[runID]; dup {
		return fmt.Errorf("pipeline: run %q already exists", runID)
	}
	rec := &runRecord{info: RunInfo{
		RunID:      runID,
		ScenarioID: scenarioID,
		State:      RunStateRunning,
		StartedAt:  time.Now().UnixMilli(),
	}}
	if h.rec.enabled() {
		w, err := h.rec.open(runID, pipevents.RecordingHeader{
			RunID:       runID,
			ScenarioID:  scenarioID,
			StartedAtMS: rec.info.StartedAt,
			SourceFile:  sourceFile,
		})
		if err != nil {
			return err
		}
		rec.w = w
	}
	h.runs.byID[runID] = rec
	h.runs.order = append(h.runs.order, runID)
	h.active = rec

	payload := map[string]any{"scenario_id": scenarioID}
	if sourceFile != "" {
		payload["source_file"] = sourceFile
	}
	h.observeLocked(pipevents.Event{
		Stage:   pipevents.StageFinalize,
		Kind:    pipevents.KindRunStarted,
		Payload: payload,
	})
	return nil
}

// EndRun closes the active run window: it emits run_finished carrying
// stats in the payload, marks the run finished, and closes the
// recording file. With no active run it does nothing. Events that
// arrive after the window closes keep their seq but get no run id and
// are not recorded to the run's file.
func (h *Hub) EndRun(stats map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active == nil {
		return
	}
	h.observeLocked(pipevents.Event{
		Stage:   pipevents.StageFinalize,
		Kind:    pipevents.KindRunFinished,
		Payload: stats,
	})
	h.active.info.State = RunStateFinished
	if h.active.w != nil {
		h.active.w.close()
		h.active.w = nil
	}
	h.active = nil
}

// Subscribe returns a channel receiving events with seq > afterSeq:
// the buffered log replays first, then live events follow. The channel
// holds up to 1024 events; a subscriber that falls further behind
// loses events (drop-on-full) rather than slowing producers — seq gaps
// reveal the loss. The returned function unsubscribes and closes the
// channel; it is safe to call more than once.
func (h *Hub) Subscribe(
	afterSeq uint64,
) (<-chan pipevents.Event, func()) {
	ch := make(chan pipevents.Event, subscriberBufferCap)
	h.mu.Lock()
	// Replaying under the lock keeps the backlog strictly ahead of
	// live events: ObserveEvent cannot interleave while it drains.
	for i := range h.log {
		if h.log[i].Seq > afterSeq {
			select {
			case ch <- h.log[i]:
			default:
			}
		}
	}
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	var once gosync.Once
	unsub := func() {
		once.Do(func() {
			h.mu.Lock()
			if _, ok := h.subs[ch]; ok {
				delete(h.subs, ch)
				// Safe to close while fanout sends: every
				// send holds h.mu, so no send is in flight
				// on a channel once it leaves h.subs.
				close(ch)
			}
			h.mu.Unlock()
		})
	}
	return ch, unsub
}

// ActiveRun reports the currently open run, if any. Callers use it to
// reject a second run or observe session while one is live.
func (h *Hub) ActiveRun() (RunInfo, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active == nil {
		return RunInfo{}, false
	}
	return h.active.info, true
}

// Recorder exposes the hub's recordings store so HTTP surfaces can
// list, download, and import the same files run recording writes.
func (h *Hub) Recorder() *Recorder { return h.rec }

// RunEvents returns a snapshot copy of the events recorded under
// runID, including the run_started/run_finished bookends. Unknown run
// ids return nil.
func (h *Hub) RunEvents(runID string) []pipevents.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	rec, ok := h.runs.byID[runID]
	if !ok {
		return nil
	}
	out := make([]pipevents.Event, len(rec.events))
	copy(out, rec.events)
	return out
}

// Runs returns every run the hub has seen, oldest first.
func (h *Hub) Runs() []RunInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]RunInfo, 0, len(h.runs.order))
	for _, id := range h.runs.order {
		out = append(out, h.runs.byID[id].info)
	}
	return out
}

// isSafeRunName reports whether s is usable both as a run id in API
// paths and as the base of a recording file name.
func isSafeRunName(s string) bool {
	if len(s) == 0 || len(s) > 128 ||
		s == "." || s == ".." || s[0] == '.' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= '0' && c <= '9' ||
			c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}
