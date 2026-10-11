//go:build fts5

package server_test

import (
	"bytes"
	"encoding/json/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
}

// setupPipelineTestEnv builds a server with a real pipeline playground:
// a hub recording under the test data dir, a live sandbox (real watcher,
// real engine, real SQLite archive), and the daemon's own sync engine for
// observe mode.
func setupPipelineTestEnv(t *testing.T) *pipelineTestEnv {
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
	srv := server.New(cfg, database, engine,
		server.WithPipelinePlayground(&server.PipelinePlayground{
			Hub:     hub,
			Sandbox: sandbox,
			Engine:  engine,
		}),
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

	// Detaching ends the run window; a second delete reports 404.
	rec = pipelineRequest(t, te, http.MethodDelete,
		"/api/v1/pipeline/observe", nil)
	require.Equal(t, http.StatusNoContent, rec.Code,
		"body: %s", rec.Body.String())
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
