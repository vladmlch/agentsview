package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
	"time"

	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/pipevents"
	"go.kenn.io/agentsview/internal/sync"
)

const (
	// sandboxDirName names the sandbox's private root inside the
	// caller's data directory.
	sandboxDirName = "pipeline-sandbox"
	// sandboxDBName is the SQLite archive file under Root.
	sandboxDBName = "sandbox.db"
	// sandboxSrcName is the codex sessions directory under Root.
	sandboxSrcName = "sessions"
	// sandboxMachine is the machine label the sandbox engine stamps
	// onto ingested sessions.
	sandboxMachine = "playground"

	// sandboxWatchBudget bounds recursive watch registration,
	// mirroring the daemon's recursiveWatchBudget.
	sandboxWatchBudget = 8192

	// sandboxWatchBatchDelay and sandboxWatchMinInterval tune the
	// sandbox watcher for run-length cycles; the daemon's 500 ms
	// batching and 5 s dispatch floor would dominate a test run.
	sandboxWatchBatchDelay  = 50 * time.Millisecond
	sandboxWatchMinInterval = 50 * time.Millisecond

	// defaultIdleQuiet is the quiet window a WaitIdle step applies
	// when the scenario passes a non-positive duration.
	defaultIdleQuiet = 500 * time.Millisecond

	// idleDeliveryFloor is the minimum time a WaitIdle step waits
	// before it may report idle. On macOS the FSEvents stream
	// coalesces delivery at darwinFSEventsLatency (500 ms) before
	// the watcher's batch delay and dispatch floor add more, so the
	// step's own filesystem effects legitimately surface ~600 ms+
	// after the write. A quiet window that only counts hub silence
	// would expire while that work is still in flight; the floor
	// holds the window open until delivery had a fair chance.
	// 1.5 s covers the stream latency, the batch delay, dispatch,
	// and the sync pass with slack.
	idleDeliveryFloor = 1500 * time.Millisecond
)

// Scenario is the minimal scripted-run shape the sandbox executes.
// Task 6 owns the full DSL (titles, summaries, fixture helpers); the
// Step func shape is fixed here so its scenarios plug into Run
// unchanged.
type Scenario struct {
	ID string
	// Fixture names the scenario's source artifact; it is recorded as
	// the run's source_file in the recording header.
	Fixture string
	Steps   []Step
}

// Step is one scripted action in a scenario. It runs against the
// sandbox's RunContext and returns an error to abort the run.
type Step func(ctx context.Context, rc *RunContext) error

// RunContext exposes the sandbox surface a step may touch: the codex
// source tree (SrcDir), the metadata dir beside it (MetaDir, which
// holds session_index.jsonl), and the event hub. The runner stamps
// each helper call as a scenario_step event.
type RunContext struct {
	SrcDir  string
	MetaDir string
	Hub     *Hub

	ctx context.Context
	n   int // index of the Step func currently running
}

// emitStep records the step's action in the active run. It fires
// before the filesystem effect so the recording orders the scripted
// cause ahead of the watch events it produces.
func (rc *RunContext) emitStep(action, path, detail string) {
	payload := map[string]any{"n": rc.n, "action": action}
	if detail != "" {
		payload["detail"] = detail
	}
	rc.Hub.ObserveEvent(pipevents.Event{
		Stage:   pipevents.StageFinalize,
		Kind:    pipevents.KindScenarioStep,
		Path:    path,
		Payload: payload,
	})
}

// WriteFile writes content to rel under SrcDir, creating parent
// directories — a rollout file drop or a rewrite.
func (rc *RunContext) WriteFile(rel, content string) error {
	rc.emitStep("write-file", rel, "")
	path, err := sandboxPath(rc.SrcDir, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("pipeline: step mkdir %s: %w", rel, err)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// AppendFile appends content to rel under SrcDir — the live-append
// ingest path.
func (rc *RunContext) AppendFile(rel, content string) error {
	rc.emitStep("append", rel, "")
	path, err := sandboxPath(rc.SrcDir, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("pipeline: step mkdir %s: %w", rel, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// RemoveFile deletes rel under SrcDir — the tombstone ingest path.
func (rc *RunContext) RemoveFile(rel string) error {
	rc.emitStep("delete", rel, "")
	path, err := sandboxPath(rc.SrcDir, rel)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// WriteMeta writes content to name under MetaDir — sidecar files such
// as session_index.jsonl that live beside sessions/, not inside it.
func (rc *RunContext) WriteMeta(name, content string) error {
	rc.emitStep("write-meta", name, "")
	path, err := sandboxPath(rc.MetaDir, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("pipeline: step mkdir %s: %w", name, err)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// WaitIdle blocks until the hub observes no new events for quiet, or
// the run context ends — but never sooner than idleDeliveryFloor,
// which gives the filesystem watcher a fair window to deliver the
// step's own effects. A non-positive quiet uses defaultIdleQuiet.
// The subscription opens before the window starts, so work queued in
// the watcher but not yet dispatched still resets the timer.
func (rc *RunContext) WaitIdle(quiet time.Duration) error {
	if quiet <= 0 {
		quiet = defaultIdleQuiet
	}
	rc.emitStep("wait-idle", "", quiet.String())
	ctx := rc.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// Subscribing at the max seq skips the buffered backlog: only
	// events that arrive after this call count against the window.
	ch, unsub := rc.Hub.Subscribe(math.MaxUint64)
	defer unsub()
	notBefore := time.Now().Add(max(quiet, idleDeliveryFloor))
	timer := time.NewTimer(quiet)
	defer timer.Stop()
	reset := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(quiet)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			reset()
		case <-timer.C:
			// A pending event counts as activity even when the
			// timer branch won the select draw.
			select {
			case _, ok := <-ch:
				if !ok {
					return nil
				}
				reset()
				continue
			default:
			}
			if wait := time.Until(notBefore); wait > 0 {
				timer.Reset(wait)
				continue
			}
			return nil
		}
	}
}

// sandboxPath resolves rel under base, rejecting absolute paths and
// parent traversal outside the sandbox.
func sandboxPath(base, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("pipeline: empty scenario path")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf(
			"pipeline: absolute scenario path %q not allowed", rel)
	}
	p := filepath.Join(base, rel)
	if p != base && !strings.HasPrefix(
		p, base+string(filepath.Separator)) {
		return "", fmt.Errorf(
			"pipeline: scenario path %q escapes the sandbox", rel)
	}
	return p, nil
}

// Sandbox owns an isolated ingest pipeline: its own SQLite archive,
// its own sync.Engine pointed at a private codex root, and a real
// sync.Watcher feeding ApplyWatchBatch.
type Sandbox struct {
	Root    string // <dataDir>/pipeline-sandbox
	SrcDir  string // <root>/sessions (the AgentCodex root)
	MetaDir string // <root> (holds session_index.jsonl beside sessions/)

	hub     *Hub
	db      *db.DB
	engine  *sync.Engine
	watcher *sync.Watcher

	// mu serializes runs and lifecycle: one scripted file at a time
	// by design, and Reset/Close never overlap a run.
	mu     gosync.Mutex
	runSeq uint64
	closed bool
}

// OpenSandbox builds a sandbox under dataDir: Root at
// <dataDir>/pipeline-sandbox, a persistent SQLite archive at
// Root/sandbox.db, a codex-only engine, and a live watcher over
// SrcDir. The hub receives every pipeline event the engine emits.
func OpenSandbox(
	ctx context.Context, dataDir string, hub *Hub,
) (*Sandbox, error) {
	if hub == nil {
		return nil, errors.New("pipeline: nil hub")
	}
	root := filepath.Join(dataDir, sandboxDirName)
	s := &Sandbox{
		Root:    root,
		SrcDir:  filepath.Join(root, sandboxSrcName),
		MetaDir: root,
		hub:     hub,
	}
	if err := os.MkdirAll(s.SrcDir, 0o755); err != nil {
		return nil, fmt.Errorf("pipeline: sandbox source dir: %w", err)
	}
	// db.Open keeps the archive persistent across runs and sandbox
	// lifetime; Reset swaps it for a fresh isolated one.
	database, err := db.Open(ctx, filepath.Join(root, sandboxDBName))
	if err != nil {
		return nil, fmt.Errorf("pipeline: open sandbox db: %w", err)
	}
	s.db = database
	if err := s.startPipeline(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	return s, nil
}

// startPipeline builds the engine and watcher over the current db and
// starts dispatching. Caller holds s.mu (or is inside OpenSandbox
// before the sandbox escapes).
func (s *Sandbox) startPipeline(ctx context.Context) error {
	engine := sync.NewEngine(ctx, s.db, sync.EngineConfig{
		AgentDirs: map[parser.AgentType][]string{
			parser.AgentCodex: {s.SrcDir},
		},
		Machine: sandboxMachine,
		// Keep parser_checkpoints and skipped_files: incremental and
		// dedup scenarios need them.
		Ephemeral:        false,
		PipelineObserver: s.hub,
		// Emitter stays nil on purpose: Engine.emit reports
		// sse_emitted through PipelineObserver even without an
		// Emitter, so a hub adapter here would double-report.
		Emitter:        nil,
		DisabledAgents: disabledSandboxAgents(),
	})

	w, err := sync.NewWatcherWithCallback(
		sandboxWatchBatchDelay, sandboxWatchMinInterval,
		func(ctx context.Context, batch sync.WatchBatch) error {
			s.hub.ObserveEvent(pipevents.Event{
				Stage:   pipevents.StageWatch,
				Kind:    pipevents.KindBatchFormed,
				Payload: watchBatchPayload(batch),
			})
			// ValidateWatchBatch accepts a nil scope for
			// path-only batches but requires one once a batch
			// turns authoritative (full sync or renames).
			// Mirror syncWatchBatch in cmd/agentsview and build
			// the scope lazily, covering the sandbox's only root.
			var recovery *sync.WatchRecoveryScope
			if batch.FullSync || len(batch.Renames) > 0 {
				recovery = &sync.WatchRecoveryScope{
					AvailableRoots: []string{s.SrcDir},
				}
			}
			return sync.ApplyWatchBatch(ctx, engine, batch, recovery)
		},
		nil, sync.WatcherOptions{},
	)
	if err != nil {
		engine.Close()
		return fmt.Errorf("pipeline: sandbox watcher: %w", err)
	}
	// RegisterRoots — not a bare WatchRecursive — because it is the
	// daemon's path on macOS: it also records the root's owning agent
	// scopes, which rename classification and lifecycle coverage use.
	results := w.RegisterRoots([]sync.WatchRoot{{
		Path:      s.SrcDir,
		Recursive: true,
		Exists:    true,
		Scopes: []sync.WatchScope{{
			Agent:   string(parser.AgentCodex),
			SyncDir: s.SrcDir,
		}},
	}}, sandboxWatchBudget)
	if len(results) > 0 && results[0].Err != nil {
		w.Stop()
		engine.Close()
		return fmt.Errorf(
			"pipeline: watch %s: %w", s.SrcDir, results[0].Err)
	}
	if err := w.Start(); err != nil {
		w.Stop()
		engine.Close()
		return fmt.Errorf("pipeline: start sandbox watcher: %w", err)
	}
	s.engine = engine
	s.watcher = w
	return nil
}

// stopPipeline halts the watcher (draining in-flight callbacks) and
// the engine's scheduler. Caller holds s.mu.
func (s *Sandbox) stopPipeline() {
	if s.watcher != nil {
		s.watcher.Stop()
		s.watcher = nil
	}
	if s.engine != nil {
		s.engine.Close()
		s.engine = nil
	}
}

// Close stops the watcher and engine and closes the archive. Safe to
// call more than once.
func (s *Sandbox) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.stopPipeline()
	if s.db != nil {
		_ = s.db.Close()
		s.db = nil
	}
}

// Reset returns the sandbox to a clean slate: the watcher and engine
// are rebuilt over a fresh isolated archive and an empty source dir.
// Everything else under Root is left alone.
func (s *Sandbox) Reset(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("pipeline: sandbox is closed")
	}
	s.stopPipeline()
	if s.db != nil {
		if err := s.db.CloseContext(ctx); err != nil {
			s.db = nil
			return fmt.Errorf(
				"pipeline: close sandbox db: %w", err)
		}
		s.db = nil
	}
	dbPath := filepath.Join(s.Root, sandboxDBName)
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(dbPath + suffix); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf(
				"pipeline: remove %s: %w", dbPath+suffix, err)
		}
	}
	// The engine's staged-parse scratch holds sqlite databases tied
	// to the old archive; drop it with the rest of the state.
	if err := os.RemoveAll(
		filepath.Join(s.Root, "scratch")); err != nil {
		return fmt.Errorf("pipeline: wipe scratch: %w", err)
	}
	if err := os.RemoveAll(s.SrcDir); err != nil {
		return fmt.Errorf("pipeline: wipe source dir: %w", err)
	}
	if err := os.MkdirAll(s.SrcDir, 0o755); err != nil {
		return fmt.Errorf("pipeline: recreate source dir: %w", err)
	}
	// OpenFreshIsolatedContext requires a pre-created empty regular
	// file (db.go:1359-1370).
	f, err := os.Create(dbPath)
	if err != nil {
		return fmt.Errorf("pipeline: create fresh db file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("pipeline: close fresh db file: %w", err)
	}
	database, err := db.OpenFreshIsolatedContext(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("pipeline: open fresh sandbox db: %w", err)
	}
	s.db = database
	return s.startPipeline(ctx)
}

// Run executes sc against the sandbox, serialized against other runs.
// The hub records a run window around the steps: file actions land in
// SrcDir, the real watcher picks them up, and ingest events flow back
// into the same recording.
func (s *Sandbox) Run(
	ctx context.Context, sc *Scenario,
) (string, error) {
	if sc == nil {
		return "", errors.New("pipeline: nil scenario")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("pipeline: sandbox is closed")
	}
	if s.db == nil || s.engine == nil {
		return "", errors.New(
			"pipeline: sandbox has no running pipeline")
	}
	s.runSeq++
	runID := fmt.Sprintf("run-%d-%d", time.Now().UnixMilli(), s.runSeq)
	if err := s.hub.BeginRun(runID, sc.ID, sc.Fixture); err != nil {
		return "", err
	}
	stats := map[string]any{"steps_total": len(sc.Steps)}
	var runErr error
	completed := 0
	defer func() {
		stats["steps_completed"] = completed
		if runErr != nil {
			stats["error"] = runErr.Error()
		}
		s.hub.EndRun(stats)
	}()

	rc := &RunContext{
		SrcDir:  s.SrcDir,
		MetaDir: s.MetaDir,
		Hub:     s.hub,
		ctx:     ctx,
	}
	for i, step := range sc.Steps {
		if step == nil {
			runErr = fmt.Errorf(
				"pipeline: scenario %q step %d is nil", sc.ID, i)
			break
		}
		if err := ctx.Err(); err != nil {
			runErr = err
			break
		}
		rc.n = i
		if err := step(ctx, rc); err != nil {
			runErr = fmt.Errorf("step %d: %w", i, err)
			break
		}
		completed++
	}
	return runID, runErr
}

// disabledSandboxAgents lists every registered agent except codex —
// the sandbox is a codex-only pipeline.
func disabledSandboxAgents() []parser.AgentType {
	var out []parser.AgentType
	for _, def := range parser.Registry {
		if def.Type != parser.AgentCodex {
			out = append(out, def.Type)
		}
	}
	return out
}

// watchBatchPayload summarizes one dispatched watcher batch for the
// batch_formed event: counts plus a bounded path sample.
func watchBatchPayload(b sync.WatchBatch) map[string]any {
	payload := map[string]any{
		"paths":           len(b.Paths),
		"renames":         len(b.Renames),
		"reconcile_roots": len(b.ReconcileRoots),
		"full_sync":       b.FullSync,
		"lost_events":     b.LostEvents,
	}
	const maxSamplePaths = 8
	if n := min(len(b.Paths), maxSamplePaths); n > 0 {
		payload["sample_paths"] = append(
			[]string(nil), b.Paths[:n]...)
	}
	return payload
}
