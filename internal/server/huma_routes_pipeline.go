package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/agentsview/internal/pipeline"
	"go.kenn.io/agentsview/internal/pipevents"
	"go.kenn.io/agentsview/internal/sessionwatch"
	agentsync "go.kenn.io/agentsview/internal/sync"
)

// pipelineRunStartTimeout bounds how long POST /runs waits for the
// sandbox to open the run window and emit run_started. The window opens
// almost immediately once the sandbox mutex is acquired; the budget only
// covers a Reset in flight or filesystem trouble.
const pipelineRunStartTimeout = 10 * time.Second

// errPipelineRunStartTimeout reports the run_started wait expiring.
var errPipelineRunStartTimeout = errors.New(
	"pipeline: run did not start in time")

// PipelinePlayground bundles the dependencies the /api/v1/pipeline
// routes serve: the event hub that records runs and fans events out to
// SSE subscribers, the isolated sandbox ingest engine, and the daemon's
// real sync engine that observe mode taps.
type PipelinePlayground struct {
	Hub     *pipeline.Hub
	Sandbox *pipeline.Sandbox
	// Engine is the daemon's real sync engine. POST /observe attaches
	// the hub to it; nil (serve --no-sync, pg-serve) disables observe.
	Engine *agentsync.Engine

	// sessionMu serializes run and observe window opens. The hub itself
	// enforces a single active run; holding sessionMu across the
	// check-then-open sequence makes the 409 answer race-free instead
	// of surfacing as a BeginRun error after the fact.
	sessionMu gosync.Mutex
	// observeRunID is the active observe session's run id, empty when
	// detached. Guarded by sessionMu.
	observeRunID string
}

// WithPipelinePlayground wires the codex pipeline playground behind
// /api/v1/pipeline. A nil playground (sandbox init failure, or a server
// built without one) leaves the whole route group unregistered so the
// rest of the API is unaffected.
func WithPipelinePlayground(pg *PipelinePlayground) Option {
	return func(s *Server) { s.pipeline = pg }
}

func (s *Server) registerPipelineRoutes() {
	if s.pipeline == nil {
		return
	}
	group := huma.NewGroup(s.api, "/api/v1/pipeline")
	configureRouteGroup(group, "Pipeline")

	s.get(group, "/scenarios", "List pipeline scenarios",
		s.humaPipelineScenarios)
	s.post(group, "/runs", "Start a pipeline run",
		s.humaPipelineRunStart)
	s.get(group, "/runs", "List pipeline runs", s.humaPipelineRuns)
	s.stream(group, http.MethodGet, "/runs/{id}/events",
		"Stream pipeline run events", s.humaPipelineRunEvents)
	s.post(group, "/observe", "Observe the live ingest pipeline",
		s.humaPipelineObserveStart)
	s.deleteRoute(group, "/observe", "Stop observing the live pipeline",
		s.humaPipelineObserveStop)
	s.get(group, "/recordings", "List pipeline run recordings",
		s.humaPipelineRecordings)
	s.raw(group, http.MethodGet, "/recordings/{name}",
		"Download a pipeline run recording", "application/x-ndjson",
		s.humaPipelineRecordingDownload)
	s.post(group, "/recordings", "Import a pipeline run recording",
		s.humaPipelineRecordingImport)
	s.post(group, "/sandbox/reset", "Reset the pipeline sandbox",
		s.humaPipelineSandboxReset)
}

// pipelineScenarioOut is the wire shape of one registry scenario: the
// step list is a Go func slice and never leaves the process.
type pipelineScenarioOut struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

func (s *Server) humaPipelineScenarios(
	_ context.Context, _ *emptyInput,
) (*jsonOutput[[]pipelineScenarioOut], error) {
	defs := pipeline.Scenarios()
	out := make([]pipelineScenarioOut, 0, len(defs))
	for _, sc := range defs {
		out = append(out, pipelineScenarioOut{
			ID:      sc.ID,
			Title:   sc.Title,
			Summary: sc.Summary,
		})
	}
	return &jsonOutput[[]pipelineScenarioOut]{Body: out}, nil
}

type pipelineRunStartInput struct {
	Body struct {
		ScenarioID string `json:"scenario_id" required:"true" doc:"Scenario id from GET /scenarios"`
	}
}

// pipelineRunStarted is the response for POST /runs and POST /observe.
type pipelineRunStarted struct {
	RunID string `json:"run_id"`
}

func (s *Server) humaPipelineRunStart(
	ctx context.Context, in *pipelineRunStartInput,
) (*jsonOutput[pipelineRunStarted], error) {
	pg := s.pipeline
	scenarios := pipeline.Scenarios()
	var scenario *pipeline.Scenario
	for i := range scenarios {
		if scenarios[i].ID == in.Body.ScenarioID {
			scenario = &scenarios[i]
			break
		}
	}
	if scenario == nil {
		return nil, apiError(http.StatusNotFound,
			"unknown pipeline scenario: "+in.Body.ScenarioID)
	}
	pg.sessionMu.Lock()
	defer pg.sessionMu.Unlock()
	if active, ok := pg.Hub.ActiveRun(); ok {
		return nil, pipelineRunConflictError(active)
	}
	// Subscribe before launching the run so the run_started event that
	// names the server-generated run id cannot be missed.
	events, unsub := pg.Hub.Subscribe(math.MaxUint64)
	defer unsub()
	runErr := make(chan error, 1)
	runCtx := s.baseCtx
	if runCtx == nil {
		runCtx = context.WithoutCancel(ctx)
	}
	go func() {
		_, err := pg.Sandbox.Run(runCtx, scenario)
		runErr <- err
	}()
	runID, err := awaitPipelineRunStarted(ctx, events, runErr)
	if err != nil {
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if errors.Is(err, errPipelineRunStartTimeout) {
			return nil, apiError(http.StatusGatewayTimeout, err.Error())
		}
		return nil, internalError("pipeline run start", err)
	}
	return &jsonOutput[pipelineRunStarted]{
		Body: pipelineRunStarted{RunID: runID},
	}, nil
}

// awaitPipelineRunStarted waits for the freshly launched run's
// run_started event, which carries the run id Sandbox.Run generated.
// runErr reports Run returning before that event arrived — meaning the
// run window never opened — while an error after the window opens still
// leaves run_started queued on events and is drained first.
func awaitPipelineRunStarted(
	ctx context.Context,
	events <-chan pipevents.Event,
	runErr <-chan error,
) (string, error) {
	deadline := time.NewTimer(pipelineRunStartTimeout)
	defer deadline.Stop()
	for {
		select {
		case err := <-runErr:
			for {
				select {
				case ev := <-events:
					if ev.Kind == pipevents.KindRunStarted {
						return ev.RunID, nil
					}
				default:
					return "", err
				}
			}
		case ev := <-events:
			if ev.Kind == pipevents.KindRunStarted {
				return ev.RunID, nil
			}
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", errPipelineRunStartTimeout
		}
	}
}

func pipelineRunConflictError(active pipeline.RunInfo) error {
	return apiError(http.StatusConflict,
		fmt.Sprintf("pipeline run %q is already active", active.RunID))
}

func (s *Server) humaPipelineRuns(
	_ context.Context, _ *emptyInput,
) (*jsonOutput[[]pipeline.RunInfo], error) {
	runs := s.pipeline.Hub.Runs()
	if runs == nil {
		runs = []pipeline.RunInfo{}
	}
	return &jsonOutput[[]pipeline.RunInfo]{Body: runs}, nil
}

type pipelineRunEventsInput struct {
	ID       string `path:"id" required:"true" doc:"Run id"`
	AfterSeq uint64 `query:"after_seq" doc:"Only events with seq greater than this"`
}

// humaPipelineRunEvents streams one run's events as SSE: the hub's
// buffered log replays first (Subscribe semantics), then live events
// tail until the run's run_finished bookend — the last event stamped
// with that run id — closes the stream. Events outside the run window
// carry no run id and are filtered out.
func (s *Server) humaPipelineRunEvents(
	_ context.Context, in *pipelineRunEventsInput,
) (*huma.StreamResponse, error) {
	pg := s.pipeline
	if pg.Hub.RunEvents(in.ID) == nil {
		return nil, apiError(http.StatusNotFound,
			"unknown pipeline run: "+in.ID)
	}
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		stream, ok := newHumaSSEStream(hctx)
		if !ok {
			writeHumaJSON(hctx, http.StatusInternalServerError,
				apiResponseError{Message: "streaming not supported"})
			return
		}
		events, unsub := pg.Hub.Subscribe(in.AfterSeq)
		defer unsub()
		heartbeat := time.NewTicker(
			sessionwatch.PollInterval * sessionwatch.HeartbeatTicks)
		defer heartbeat.Stop()
		for {
			select {
			case <-hctx.Context().Done():
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				if ev.RunID != in.ID {
					continue
				}
				if !stream.SendJSON("event", ev) {
					return
				}
				if ev.Kind == pipevents.KindRunFinished {
					return
				}
			case <-heartbeat.C:
				if !stream.Send(
					"heartbeat", time.Now().Format(time.RFC3339)) {
					return
				}
			}
		}
	}}, nil
}

type pipelineObserveInput struct {
	Body struct {
		RunID string `json:"run_id" required:"true" doc:"Client-chosen run id for this observe session"`
	}
}

// humaPipelineObserveStart opens a recording window on the hub and taps
// the daemon's real sync engine: every pipeline event the live ingest
// path emits lands in the observe run until DELETE /observe detaches.
// Only one session may hold the tap; a detached tap is mandatory because
// leaving it on would keep recording the production pipeline and grow
// the hub's event log indefinitely.
func (s *Server) humaPipelineObserveStart(
	_ context.Context, in *pipelineObserveInput,
) (*jsonOutput[pipelineRunStarted], error) {
	pg := s.pipeline
	if pg.Engine == nil {
		return nil, apiError(http.StatusNotImplemented,
			"observe mode requires the daemon sync engine")
	}
	runID := in.Body.RunID
	if !validPipelineRunID(runID) {
		return nil, apiError(http.StatusBadRequest,
			"invalid run_id: want 1-128 of [A-Za-z0-9._-], "+
				"not starting with '.'")
	}
	pg.sessionMu.Lock()
	defer pg.sessionMu.Unlock()
	if active, ok := pg.Hub.ActiveRun(); ok {
		return nil, pipelineRunConflictError(active)
	}
	if err := pg.Hub.BeginRun(runID, "observe", ""); err != nil {
		return nil, apiError(http.StatusConflict, err.Error())
	}
	pg.Engine.SetPipelineObserver(pg.Hub)
	pg.observeRunID = runID
	return &jsonOutput[pipelineRunStarted]{
		Body: pipelineRunStarted{RunID: runID},
	}, nil
}

// humaPipelineObserveStop detaches the hub from the real engine before
// closing the run window, so the run_finished bookend is the last event
// the session records.
func (s *Server) humaPipelineObserveStop(
	_ context.Context, _ *emptyInput,
) (*noContentOutput, error) {
	pg := s.pipeline
	pg.sessionMu.Lock()
	defer pg.sessionMu.Unlock()
	if pg.observeRunID == "" {
		return nil, apiError(http.StatusNotFound,
			"no pipeline observe session is active")
	}
	if pg.Engine != nil {
		pg.Engine.SetPipelineObserver(nil)
	}
	pg.Hub.EndRun(map[string]any{"source": "observe"})
	pg.observeRunID = ""
	return &noContentOutput{Status: http.StatusNoContent}, nil
}

// validPipelineRunID mirrors the hub's run-id safety rules so observe
// requests can fail 400 before reaching BeginRun's opaque error.
func validPipelineRunID(s string) bool {
	if len(s) == 0 || len(s) > 128 || s[0] == '.' {
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

func (s *Server) humaPipelineRecordings(
	_ context.Context, _ *emptyInput,
) (*jsonOutput[[]string], error) {
	names, err := s.pipeline.Hub.Recorder().ListRecordings()
	if err != nil {
		return nil, internalError("list pipeline recordings", err)
	}
	if names == nil {
		names = []string{}
	}
	return &jsonOutput[[]string]{Body: names}, nil
}

type pipelineRecordingInput struct {
	Name string `path:"name" required:"true" doc:"Recording file name"`
}

func (s *Server) humaPipelineRecordingDownload(
	_ context.Context, in *pipelineRecordingInput,
) (*bytesOutput, error) {
	data, err := s.pipeline.Hub.Recorder().ReadRecordingFile(in.Name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, apiError(http.StatusNotFound,
				"pipeline recording not found: "+in.Name)
		}
		return nil, apiError(http.StatusBadRequest, err.Error())
	}
	return &bytesOutput{
		ContentType: "application/x-ndjson",
		ContentDisposition: fmt.Sprintf(
			"attachment; filename=%q", in.Name),
		NoSniff: "nosniff",
		Body:    data,
	}, nil
}

type pipelineRecordingForm struct {
	File huma.FormFile `form:"file" contentType:"application/octet-stream" required:"true"`
}

type pipelineRecordingImportInput struct {
	RawBody huma.MultipartFormFiles[pipelineRecordingForm]
}

type pipelineRecordingImported struct {
	Name string `json:"name"`
}

// humaPipelineRecordingImport stores an uploaded JSONL recording after
// validating it against the pipevents.ReadRecording contract, so a
// corrupt file can never shadow a good recording under the same name.
func (s *Server) humaPipelineRecordingImport(
	_ context.Context, in *pipelineRecordingImportInput,
) (*createdOutput[pipelineRecordingImported], error) {
	file := in.RawBody.Data().File
	if !file.IsSet {
		return nil, apiError(http.StatusBadRequest,
			"missing 'file' field in form data")
	}
	defer file.Close()
	name := file.Filename
	if !validPipelineRecordingName(name) {
		return nil, apiError(http.StatusBadRequest,
			"invalid recording name: want a plain *.jsonl file name")
	}
	data, err := io.ReadAll(io.LimitReader(file, 1<<30))
	if err != nil {
		return nil, apiError(http.StatusBadRequest,
			"reading upload: "+err.Error())
	}
	if _, _, err := pipevents.ReadRecording(bytes.NewReader(data)); err != nil {
		return nil, apiError(http.StatusBadRequest,
			"invalid recording: "+err.Error())
	}
	if err := s.pipeline.Hub.Recorder().ImportRecording(
		name, bytes.NewReader(data)); err != nil {
		return nil, internalError("import pipeline recording", err)
	}
	return &createdOutput[pipelineRecordingImported]{
		Status: http.StatusCreated,
		Body:   pipelineRecordingImported{Name: name},
	}, nil
}

// validPipelineRecordingName mirrors the recorder's file-name rules so
// imports fail 400 before touching the filesystem.
func validPipelineRecordingName(name string) bool {
	return name != "" && name[0] != '.' &&
		len(name) > len(".jsonl") &&
		strings.HasSuffix(name, ".jsonl") &&
		filepath.Base(name) == name &&
		!strings.ContainsAny(name, `/\`)
}

func (s *Server) humaPipelineSandboxReset(
	ctx context.Context, _ *emptyInput,
) (*noContentOutput, error) {
	pg := s.pipeline
	pg.sessionMu.Lock()
	defer pg.sessionMu.Unlock()
	if active, ok := pg.Hub.ActiveRun(); ok {
		return nil, pipelineRunConflictError(active)
	}
	if err := pg.Sandbox.Reset(ctx); err != nil {
		return nil, internalError("pipeline sandbox reset", err)
	}
	return &noContentOutput{Status: http.StatusNoContent}, nil
}
