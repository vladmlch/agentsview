//go:build fts5

package server_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/dbtest"
	"go.kenn.io/agentsview/internal/pipeline"
	"go.kenn.io/agentsview/internal/pipevents"
	"go.kenn.io/agentsview/internal/server"
	"go.kenn.io/agentsview/internal/sync"
)

// pipelineTestEnv extends testEnv with the playground wiring under test:
// the event hub and sandbox the /api/v1/pipeline routes serve.
type pipelineTestEnv struct {
	*testEnv
	hub     *pipeline.Hub
	sandbox *pipeline.Sandbox
	// tap records the observer observe mode installs, standing in for
	// the daemon's embed schedulers, which the test env does not build.
	tap *pipelineObserverTarget
}

// pipelineObserverTarget is a test double for the embed schedulers:
// it records the observer the playground attaches/detaches so tests
// can assert the observe-window lifecycle without running builds.
type pipelineObserverTarget struct {
	mu  gosync.Mutex
	obs pipevents.Observer
}

func (t *pipelineObserverTarget) SetPipelineObserver(o pipevents.Observer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.obs = o
}

func (t *pipelineObserverTarget) observer() pipevents.Observer {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.obs
}

func setupPipelineTestEnv(t *testing.T) *pipelineTestEnv {
	return buildPipelineTestEnv(t, true)
}

// setupPipelineTestEnvNoEngine builds the same playground without a
// real engine reference, mirroring serve --no-sync / pg-serve: observe
// answers 501 while every other route keeps working.
func setupPipelineTestEnvNoEngine(t *testing.T) *pipelineTestEnv {
	return buildPipelineTestEnv(t, false)
}

// setupPipelineTestEnv builds a server with a real pipeline playground:
// a hub recording under the test data dir, a live sandbox (real watcher,
// real engine, real SQLite archive), and — when withEngine — the
// daemon's own sync engine for observe mode.
func buildPipelineTestEnv(t *testing.T, withEngine bool) *pipelineTestEnv {
	t.Helper()
	dir := tempDirWithRetryCleanup(t)
	cfg := config.Config{
		Host:         "127.0.0.1",
		Port:         0,
		DataDir:      dir,
		DBPath:       filepath.Join(dir, "test.db"),
		WriteTimeout: 30 * time.Second,
	}
	database := dbtest.OpenTestDBAt(t, cfg.DBPath)
	engine := sync.NewEngine(t.Context(), database, sync.EngineConfig{
		Machine: "test",
	})
	hub := pipeline.NewHub(filepath.Join(dir, "pipeline-recordings"))
	sandbox, err := pipeline.OpenSandbox(t.Context(), dir, hub)
	require.NoError(t, err)
	t.Cleanup(sandbox.Close)
	tap := &pipelineObserverTarget{}
	pg := &server.PipelinePlayground{
		Hub:       hub,
		Sandbox:   sandbox,
		EmbedTaps: []server.PipelineObserverTarget{tap},
	}
	if withEngine {
		pg.Engine = engine
	}
	srv := server.New(cfg, database, engine,
		server.WithPipelinePlayground(pg),
	)
	return &pipelineTestEnv{
		testEnv: &testEnv{
			srv:     srv,
			handler: wrapTestHandler(cfg, srv.Handler()),
			db:      database,
			engine:  engine,
			dataDir: dir,
		},
		hub:     hub,
		sandbox: sandbox,
		tap:     tap,
	}
}

func pipelineRequest(
	t *testing.T, te *pipelineTestEnv,
	method, path string, body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	te.handler.ServeHTTP(rec, req)
	return rec
}

// pipelineEventsRequest reads the run's SSE stream until it closes. The
// stream ends itself once the run's run_finished event is delivered, so
// a finished run returns immediately and a live run returns when the run
// ends; deadline bounds the wait either way.
func pipelineEventsRequest(
	t *testing.T, te *pipelineTestEnv,
	runID, query string, deadline time.Duration,
) []pipevents.Event {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/pipeline/runs/"+runID+"/events"+query,
		nil,
	)
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		te.handler.ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(deadline):
		t.Fatal("pipeline events stream did not close before deadline")
	}
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return parsePipelineSSE(t, rec.Body.String())
}

// parsePipelineSSE decodes an SSE response body into the pipeline events
// carried by its "event" frames.
func parsePipelineSSE(t *testing.T, body string) []pipevents.Event {
	t.Helper()
	var events []pipevents.Event
	for _, block := range strings.Split(body, "\n\n") {
		var name string
		var data string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if name != "event" || data == "" {
			continue
		}
		var e pipevents.Event
		require.NoError(t, json.Unmarshal([]byte(data), &e))
		events = append(events, e)
	}
	return events
}

// pipelineRunKinds returns the ordered kinds of a run's event list.
func pipelineRunKinds(events []pipevents.Event) []string {
	kinds := make([]string, len(events))
	for i, e := range events {
		kinds[i] = e.Kind
	}
	return kinds
}

// pipelineWaitRunFinished polls GET /runs until runID reports
// state=finished or the assertion budget expires.
func pipelineWaitRunFinished(
	t *testing.T, te *pipelineTestEnv, runID string,
) {
	t.Helper()
	require.Eventually(t, func() bool {
		rec := pipelineRequest(
			t, te, http.MethodGet, "/api/v1/pipeline/runs", nil)
		var runs []pipeline.RunInfo
		if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil {
			return false
		}
		for _, r := range runs {
			if r.RunID == runID {
				return r.State == pipeline.RunStateFinished
			}
		}
		return false
	}, 30*time.Second, 50*time.Millisecond)
}

// pipelineStartRun posts /runs for scenario and requires a run id back.
func pipelineStartRun(
	t *testing.T, te *pipelineTestEnv, scenario string,
) string {
	t.Helper()
	rec := pipelineRequest(t, te, http.MethodPost,
		"/api/v1/pipeline/runs", map[string]string{
			"scenario_id": scenario,
		})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var out struct {
		RunID string `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotEmpty(t, out.RunID)
	return out.RunID
}

func TestPipelineScenariosListsRegistry(t *testing.T) {
	te := setupPipelineTestEnv(t)
	rec := pipelineRequest(
		t, te, http.MethodGet, "/api/v1/pipeline/scenarios", nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var scenarios []struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &scenarios))
	require.GreaterOrEqual(t, len(scenarios), 8,
		"registry should expose every built-in scenario")
	seen := map[string]bool{}
	for _, sc := range scenarios {
		assert.NotEmpty(t, sc.ID)
		assert.NotEmpty(t, sc.Title)
		assert.False(t, seen[sc.ID], "duplicate scenario id %q", sc.ID)
		seen[sc.ID] = true
	}
	assert.True(t, seen["happy-path"])
}

func TestPipelineRunHappyPathStreamsToRunFinished(t *testing.T) {
	te := setupPipelineTestEnv(t)
	runID := pipelineStartRun(t, te, "happy-path")

	events := pipelineEventsRequest(t, te, runID, "", 30*time.Second)
	require.NotEmpty(t, events)
	kinds := pipelineRunKinds(events)
	assert.Equal(t, pipevents.KindRunStarted, kinds[0])
	assert.Equal(t, pipevents.KindRunFinished, kinds[len(kinds)-1])
	var lastSeq uint64
	for _, e := range events {
		assert.Equal(t, runID, e.RunID)
		assert.Greater(t, e.Seq, lastSeq, "seqs must be strictly increasing")
		lastSeq = e.Seq
	}
	pipelineWaitRunFinished(t, te, runID)

	// Replaying a finished run returns the same ordered stream and the
	// after_seq cursor resumes mid-stream without duplicates.
	replay := pipelineEventsRequest(t, te, runID, "", 10*time.Second)
	require.Equal(t, kinds, pipelineRunKinds(replay))
	partial := pipelineEventsRequest(
		t, te, runID, "?after_seq=1", 10*time.Second)
	require.NotEmpty(t, partial)
	for _, e := range partial {
		assert.Greater(t, e.Seq, uint64(1))
	}

	// A cursor at or past the run's final event means the client already
	// consumed the whole stream: the answer is an empty stream that
	// closes immediately rather than heartbeating forever.
	tail := pipelineEventsRequest(
		t, te, runID,
		"?after_seq="+strconv.FormatUint(lastSeq, 10),
		10*time.Second)
	assert.Empty(t, tail)
}

func TestPipelineRunConflictWhileActive(t *testing.T) {
	te := setupPipelineTestEnv(t)
	runID := pipelineStartRun(t, te, "happy-path")

	rec := pipelineRequest(t, te, http.MethodPost,
		"/api/v1/pipeline/runs", map[string]string{
			"scenario_id": "happy-path",
		})
	assert.Equal(t, http.StatusConflict, rec.Code,
		"second run while one is active must be 409, body: %s",
		rec.Body.String())

	pipelineWaitRunFinished(t, te, runID)
	pipelineStartRun(t, te, "cold-archive")
}

func TestPipelineRunUnknownScenarioAndUnknownRun(t *testing.T) {
	te := setupPipelineTestEnv(t)
	rec := pipelineRequest(t, te, http.MethodPost,
		"/api/v1/pipeline/runs", map[string]string{
			"scenario_id": "no-such-scenario",
		})
	assert.Equal(t, http.StatusNotFound, rec.Code)

	rec = pipelineRequest(t, te, http.MethodGet,
		"/api/v1/pipeline/runs/run-nope/events", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestPipelineObserveAttachesToRealEngine(t *testing.T) {
	te := setupPipelineTestEnv(t)
	rec := pipelineRequest(t, te, http.MethodPost,
		"/api/v1/pipeline/observe", map[string]string{
			"run_id": "observe-1",
		})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	// Attaching also plugs the embed taps, so production embed_* events
	// join the same observe recording the engine's events land in.
	assert.Equal(t, te.hub, te.tap.observer())

	// A second observe session cannot attach while one is live.
	rec = pipelineRequest(t, te, http.MethodPost,
		"/api/v1/pipeline/observe", map[string]string{
			"run_id": "observe-2",
		})
	assert.Equal(t, http.StatusConflict, rec.Code)

	// The daemon's real engine now reports pipeline events into the
	// observe run.
	_, err := te.engine.SyncThenRun(
		t.Context(), false, nil, func(bool) error { return nil })
	require.NoError(t, err)

	// Detaching ends the run window, unplugs the taps again, and a
	// second delete reports 404.
	rec = pipelineRequest(t, te, http.MethodDelete,
		"/api/v1/pipeline/observe", nil)
	require.Equal(t, http.StatusNoContent, rec.Code,
		"body: %s", rec.Body.String())
	assert.Nil(t, te.tap.observer(),
		"observe detach must unplug the embed taps")
	rec = pipelineRequest(t, te, http.MethodDelete,
		"/api/v1/pipeline/observe", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	events := te.hub.RunEvents("observe-1")
	require.NotEmpty(t, events)
	kinds := pipelineRunKinds(events)
	assert.Equal(t, pipevents.KindRunStarted, kinds[0])
	assert.Equal(t, pipevents.KindRunFinished, kinds[len(kinds)-1])
	assert.Contains(t, kinds, pipevents.KindSyncPassStarted,
		"observe mode must capture the real engine's sync pass events")
}

// TestPipelineSandboxRunLeavesEmbedTapsDetached is the regression guard
// for the review's Important finding: the hub stamps every observed
// event with the active run id, so an embed tap left attached outside
// observe windows would leak production embed_* events into sandbox
// recordings and SSE streams. The taps must stay detached for the
// entire sandbox-run lifecycle.
func TestPipelineSandboxRunLeavesEmbedTapsDetached(t *testing.T) {
	te := setupPipelineTestEnv(t)
	runID := pipelineStartRun(t, te, "happy-path")
	assert.Nil(t, te.tap.observer(),
		"sandbox run must not attach the embed taps")
	pipelineWaitRunFinished(t, te, runID)
	assert.Nil(t, te.tap.observer(),
		"embed taps must stay detached after the sandbox run")

	for _, e := range te.hub.RunEvents(runID) {
		assert.NotEqual(t, pipevents.StageEmbed, e.Stage,
			"sandbox recordings must not contain embed-stage events")
	}
}

// TestPipelineObserveRequiresEngineAndValidRunID covers the observe
// precondition errors: no real engine → 501 (serve --no-sync /
// pg-serve), malformed run_id → 400 before the hub is touched.
func TestPipelineObserveRequiresEngineAndValidRunID(t *testing.T) {
	teNoEngine := setupPipelineTestEnvNoEngine(t)
	rec := pipelineRequest(t, teNoEngine, http.MethodPost,
		"/api/v1/pipeline/observe", map[string]string{
			"run_id": "observe-nope",
		})
	assert.Equal(t, http.StatusNotImplemented, rec.Code,
		"body: %s", rec.Body.String())
	assert.Nil(t, teNoEngine.tap.observer())

	te := setupPipelineTestEnv(t)
	for _, runID := range []string{"bad id", ".hidden", "a/b"} {
		rec = pipelineRequest(t, te, http.MethodPost,
			"/api/v1/pipeline/observe", map[string]string{
				"run_id": runID,
			})
		assert.Equal(t, http.StatusBadRequest, rec.Code,
			"run_id %q must be rejected", runID)
	}
	assert.Nil(t, te.tap.observer())
	_, active := te.hub.ActiveRun()
	assert.False(t, active, "rejected observe must not open a window")
}

// TestPipelineObserveDetachedOnShutdown covers the forgotten-session
// path: a live observe window is detached by Server.Shutdown, closing
// its recording with run_finished and unplugging the taps instead of
// leaving a half-open run blocking everything.
func TestPipelineObserveDetachedOnShutdown(t *testing.T) {
	te := setupPipelineTestEnv(t)
	rec := pipelineRequest(t, te, http.MethodPost,
		"/api/v1/pipeline/observe", map[string]string{
			"run_id": "observe-shutdown",
		})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	require.NoError(t, te.srv.Shutdown(
		context.WithoutCancel(t.Context())))
	assert.Nil(t, te.tap.observer())

	events := te.hub.RunEvents("observe-shutdown")
	require.NotEmpty(t, events)
	kinds := pipelineRunKinds(events)
	assert.Equal(t, pipevents.KindRunFinished, kinds[len(kinds)-1],
		"shutdown must close the forgotten observe run")
	_, active := te.hub.ActiveRun()
	assert.False(t, active)
}

func TestPipelineRecordingsListDownloadImport(t *testing.T) {
	te := setupPipelineTestEnv(t)
	runID := pipelineStartRun(t, te, "happy-path")
	pipelineEventsRequest(t, te, runID, "", 30*time.Second)
	pipelineWaitRunFinished(t, te, runID)

	rec := pipelineRequest(t, te, http.MethodGet,
		"/api/v1/pipeline/recordings", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var names []string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &names))
	require.NotEmpty(t, names)
	recordingName := names[0]
	assert.True(t, strings.HasSuffix(recordingName, ".jsonl"))

	// The recording downloads as raw JSONL with the header line first.
	rec = pipelineRequest(t, te, http.MethodGet,
		"/api/v1/pipeline/recordings/"+recordingName, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	downloaded := rec.Body.Bytes()
	header, _, err := pipevents.ReadRecording(bytes.NewReader(downloaded))
	require.NoError(t, err)
	assert.Equal(t, runID, header.RunID)

	// Importing the same bytes under a new name registers it.
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	fw, err := mw.CreateFormFile("file", "imported-run.jsonl")
	require.NoError(t, err)
	_, err = fw.Write(downloaded)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/pipeline/recordings", &form)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec = httptest.NewRecorder()
	te.handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code,
		"body: %s", rec.Body.String())

	rec = pipelineRequest(t, te, http.MethodGet,
		"/api/v1/pipeline/recordings", nil)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &names))
	assert.Contains(t, names, "imported-run.jsonl")

	// A body that is not a valid recording is rejected.
	form.Reset()
	mw = multipart.NewWriter(&form)
	fw, err = mw.CreateFormFile("file", "bad.jsonl")
	require.NoError(t, err)
	_, err = fw.Write([]byte("not a recording\n"))
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	req = httptest.NewRequest(http.MethodPost,
		"/api/v1/pipeline/recordings", &form)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec = httptest.NewRecorder()
	te.handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestPipelineRecordingDownloadRejectsUnsafeNames covers the recording
// name validator behind GET /recordings/{name}: anything that is not a
// plain "*.jsonl" base name answers 400 before touching the filesystem,
// while a well-formed but absent name answers 404.
func TestPipelineRecordingDownloadRejectsUnsafeNames(t *testing.T) {
	te := setupPipelineTestEnv(t)
	for _, name := range []string{
		"x.txt",         // not a recording extension
		".hidden.jsonl", // dotfile
		".jsonl",        // extension only, no base name
		"..x.jsonl",     // dot-prefixed traversal look-alike
	} {
		rec := pipelineRequest(t, te, http.MethodGet,
			"/api/v1/pipeline/recordings/"+name, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code,
			"name %q must be rejected, body: %s", name, rec.Body.String())
	}

	rec := pipelineRequest(t, te, http.MethodGet,
		"/api/v1/pipeline/recordings/missing.jsonl", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestPipelineSandboxResetConflictsWhileRunActive covers the 409 path:
// resetting the sandbox under a live run must refuse rather than wait
// on the sandbox mutex or corrupt the run's archive.
func TestPipelineSandboxResetConflictsWhileRunActive(t *testing.T) {
	te := setupPipelineTestEnv(t)
	runID := pipelineStartRun(t, te, "happy-path")

	rec := pipelineRequest(t, te, http.MethodPost,
		"/api/v1/pipeline/sandbox/reset", nil)
	assert.Equal(t, http.StatusConflict, rec.Code,
		"reset while a run is active must be 409, body: %s",
		rec.Body.String())

	pipelineWaitRunFinished(t, te, runID)
	rec = pipelineRequest(t, te, http.MethodPost,
		"/api/v1/pipeline/sandbox/reset", nil)
	assert.Equal(t, http.StatusNoContent, rec.Code,
		"reset after the run finishes must succeed, body: %s",
		rec.Body.String())
}

func TestPipelineSandboxResetAllowsNewRun(t *testing.T) {
	te := setupPipelineTestEnv(t)
	runID := pipelineStartRun(t, te, "happy-path")
	pipelineWaitRunFinished(t, te, runID)

	rec := pipelineRequest(t, te, http.MethodPost,
		"/api/v1/pipeline/sandbox/reset", nil)
	require.Equal(t, http.StatusNoContent, rec.Code,
		"body: %s", rec.Body.String())

	// The reset sandbox runs scenarios again on a clean archive.
	runID = pipelineStartRun(t, te, "malformed-line")
	events := pipelineEventsRequest(t, te, runID, "", 30*time.Second)
	require.NotEmpty(t, events)
	kinds := pipelineRunKinds(events)
	assert.Equal(t, pipevents.KindRunFinished, kinds[len(kinds)-1])
}

// pipelineImportRecording POSTs data as a multipart file field under
// filename to the recording import route.
func pipelineImportRecording(
	t *testing.T, te *pipelineTestEnv,
	filename string, data []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	fw, err := mw.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = fw.Write(data)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(
		http.MethodPost, "/api/v1/pipeline/recordings", &form)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	te.handler.ServeHTTP(rec, req)
	return rec
}

// TestPipelineRunEventsReplayBeyondSubscriberCap is the regression test
// for the review's truncated-replay finding: a run whose events outgrow
// the 1024-event subscriber buffer must still stream completely. The hub
// log itself is unbounded, so the overflow is forced honestly — one run
// window that observes more events than the buffer holds. Replaying
// through the capped Subscribe path would deliver a torn prefix and
// then heartbeat forever because run_finished never arrives.
func TestPipelineRunEventsReplayBeyondSubscriberCap(t *testing.T) {
	te := setupPipelineTestEnv(t)
	hub := te.hub

	const fillers = 1500 // comfortably past the 1024-event cap
	require.NoError(t, hub.BeginRun("big-run", "manual", ""))
	emit := func(from, to int) {
		for i := from; i < to; i++ {
			hub.ObserveEvent(pipevents.Event{
				Stage: pipevents.StageParse,
				Kind:  "filler",
				Path:  fmt.Sprintf("/big/%04d", i),
			})
		}
	}
	emit(0, fillers-100)

	// Open the stream while the run is still live so the tail path —
	// not only the finished-run snapshot — is exercised: the remaining
	// events and run_finished may arrive after the handler snapshots.
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/pipeline/runs/big-run/events", nil)
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		te.handler.ServeHTTP(rec, req)
		close(done)
	}()
	// Give the handler a moment to subscribe, so the last batch and the
	// run_finished bookend really flow through the live subscription.
	time.Sleep(50 * time.Millisecond)
	emit(fillers-100, fillers)
	hub.EndRun(nil)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("live-tailed stream did not close on run_finished")
	}
	require.Equal(t, http.StatusOK, rec.Code,
		"body: %s", rec.Body.String())
	live := parsePipelineSSE(t, rec.Body.String())

	// assertBigRun verifies one complete big-run stream: bookends at
	// the edges, strictly increasing seqs, and every filler present in
	// emit order. Kind and Path filters keep stray non-run events from
	// disturbing the count.
	assertBigRun := func(events []pipevents.Event) {
		t.Helper()
		require.NotEmpty(t, events)
		assert.Equal(t, pipevents.KindRunStarted, events[0].Kind)
		assert.Equal(t, pipevents.KindRunFinished,
			events[len(events)-1].Kind)
		seen := 0
		var lastSeq uint64
		for _, e := range events {
			assert.Equal(t, "big-run", e.RunID)
			assert.Greater(t, e.Seq, lastSeq)
			lastSeq = e.Seq
			if e.Kind == "filler" {
				assert.Equal(t,
					fmt.Sprintf("/big/%04d", seen), e.Path,
					"fillers must arrive in emit order")
				seen++
			}
		}
		assert.Equal(t, fillers, seen,
			"every filler event must survive the stream")
	}
	assertBigRun(live)

	// Finished-run replay is equally complete and closes promptly.
	replay := pipelineEventsRequest(
		t, te, "big-run", "", 10*time.Second)
	assertBigRun(replay)
	lastSeq := replay[len(replay)-1].Seq

	// A resume cursor mid-run returns exactly the remaining tail.
	resume := pipelineEventsRequest(
		t, te, "big-run", "?after_seq=1401", 10*time.Second)
	require.NotEmpty(t, resume)
	assert.Greater(t, resume[0].Seq, uint64(1401))
	for _, e := range resume {
		assert.Equal(t, "big-run", e.RunID)
	}
	assert.Equal(t, pipevents.KindRunFinished,
		resume[len(resume)-1].Kind)

	// A cursor at the run's last event closes empty immediately.
	tail := pipelineEventsRequest(t, te, "big-run",
		"?after_seq="+strconv.FormatUint(lastSeq, 10), 10*time.Second)
	assert.Empty(t, tail)

	// Other runs piling onto the global log afterwards do not eat into
	// this run's replay: the run's own event list is the source.
	require.NoError(t, hub.BeginRun("noise", "manual", ""))
	for i := 0; i < 50; i++ {
		hub.ObserveEvent(pipevents.Event{
			Stage: pipevents.StageWatch, Kind: "noise",
		})
	}
	hub.EndRun(nil)
	again := pipelineEventsRequest(
		t, te, "big-run", "", 10*time.Second)
	assertBigRun(again)
}

// TestPipelineRecordingImportRejectsActiveRun covers the guard the T4
// review forwarded to the import handler: an upload named after the
// file an in-flight run is writing must be refused, because os.Rename
// would swap the path under the recorder's open descriptor and send the
// rest of the run's events to an unlinked inode.
func TestPipelineRecordingImportRejectsActiveRun(t *testing.T) {
	te := setupPipelineTestEnv(t)

	var rec bytes.Buffer
	require.NoError(t, pipevents.WriteRecording(&rec,
		pipevents.RecordingHeader{RunID: "uploaded", StartedAtMS: 1},
		[]pipevents.Event{
			{Seq: 1, Stage: pipevents.StageParse, Kind: "k"},
		}))
	upload := rec.Bytes()

	require.NoError(t, te.hub.BeginRun("liverun", "manual", ""))
	te.hub.ObserveEvent(pipevents.Event{
		Stage: pipevents.StageParse, Kind: "live-event",
	})

	// The live file is a forbidden target; any other name imports fine.
	res := pipelineImportRecording(t, te, "liverun.jsonl", upload)
	assert.Equal(t, http.StatusConflict, res.Code,
		"import over a live recording must be 409, body: %s",
		res.Body.String())
	res = pipelineImportRecording(t, te, "unrelated.jsonl", upload)
	assert.Equal(t, http.StatusCreated, res.Code,
		"unrelated import must succeed, body: %s", res.Body.String())

	// The run's recording on disk is untouched by the rejected import:
	// it still parses as this run's live file, not the upload.
	data, err := te.hub.Recorder().ReadRecordingFile("liverun.jsonl")
	require.NoError(t, err)
	header, _, err := pipevents.ReadRecording(bytes.NewReader(data))
	require.NoError(t, err)
	assert.Equal(t, "liverun", header.RunID,
		"rejected import must not shadow the live recording")

	// Once the run ends the file is a finished recording again and may
	// be replaced by an explicit import.
	te.hub.EndRun(nil)
	res = pipelineImportRecording(t, te, "liverun.jsonl", upload)
	assert.Equal(t, http.StatusCreated, res.Code,
		"import after the run finished must succeed, body: %s",
		res.Body.String())
	header, _, err = te.hub.Recorder().ReadRecording("liverun.jsonl")
	require.NoError(t, err)
	assert.Equal(t, "uploaded", header.RunID)
}

func TestPipelineRoutesAbsentWithoutPlayground(t *testing.T) {
	// A server built without the playground registers no pipeline group:
	// the path falls through to the SPA fallback, which answers HTML,
	// not the JSON scenario list.
	te := setup(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet, "/api/v1/pipeline/scenarios", nil)
	te.handler.ServeHTTP(rec, req)
	assert.NotContains(t,
		rec.Header().Get("Content-Type"), "application/json",
		"unregistered pipeline routes must not answer API JSON")
}
