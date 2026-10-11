# Codex Pipeline Playground Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A permanent dev tool — a hidden `/pipeline` route in agentsview that visualizes, step by step, how a Codex session file travels from filesystem event to database rows and search indexes, over the real ingest pipeline.

**Architecture:** An opt-in event tap (`internal/pipevents`) is emitted by `internal/sync` (watch batch, resolve, admission gates, parse lifecycle, batching, write transactions, finalize) and `internal/parser` (codex line classification, cursor updates, incremental fallbacks, staging). A `internal/pipeline` package owns a `Hub` (fan-out + JSONL recording), a `Sandbox` (second `sync.Engine` over an isolated SQLite archive and its own `sync.Watcher` watching a temp sessions dir), and scripted `Scenario`s that drive fixtures through the real watcher path. `internal/server` exposes `/api/v1/pipeline/*` (scenario list, run control, SSE event stream, recording import/export, sandbox reset, observe mode on the real engine). The Svelte frontend adds a hidden `pipeline` route: pipeline diagram (8 nodes) + scrubbable event timeline + per-step state panel (file window, parsed events, gate verdicts, DB diff, FTS, cursor state, queue).

**Tech Stack:** Go 1.x (`internal/sync`, `internal/parser`, `internal/db`, `internal/server` + huma), SQLite (`db.OpenFreshIsolatedContext`), Svelte 5 runes, SSE (`newHumaSSEStream` / `EventSource`), orval-generated client.

**Spec (agreed design, grilling session 2026-10-11):**
- Purpose: educational visualization over the real pipeline, plus debugging value.
- Data: live real pipeline AND explicit record/replay; recording is opt-in (press record), stored as versioned JSONL in the user data dir with export/import.
- Interactivity: step-through + scrubbable timeline; playback only (no pipeline-blocking breakpoints).
- Scope: codex only. One file per run. All edge cases: incremental append, re-ingest dedup, malformed line, delete, rename/title change, truncate-rewrite, cold archive.
- Instrumentation of production code allowed: no-op observer by default.
- Permanent dev tool, not a throwaway prototype.
- Granularity: event-level default, toggle to microscopic detail.
- Layout: pipeline diagram on top, timeline + state panel below (3-pane hybrid).
- Placement: hidden in-app route (`/pipeline`), no nav changes. English-only strings (dev tool — deliberately overrides the DESIGN.md i18n default for this page only).
- Transport: SSE + GET snapshot, same pattern as sync progress.
- Sandbox: second in-process `Engine` over an isolated archive (`db.OpenFreshIsolatedContext`), own `sync.Watcher` on a temp dir; fixtures enter by copying into that dir — the honest path.
- Observe mode: read-only tap on the real engine to watch the user's actual `~/.codex` traffic.
- Pipeline diagram nodes: Watch → Resolve → Gates → Parse → Batch → Write → Finalize, plus a satellite Embeddings node (async, 30 s debounce; FTS5 lives inside the write transaction — the contrast is part of the lesson).
- State panel tabs: File, Events, Gates, DB, FTS, Cursor, Queue — all seven.

## Global Constraints

- Go tests: table-driven where sensible, `github.com/stretchr/testify` (`require` for fatal setup, `assert` for independent checks), `t.TempDir()` for temp dirs. No `if got != want` manual comparisons.
- Timing budgets: literal `assert.Eventually`/`Never` durations must be ≥ 1 second (`make check-timing-budgets` enforces).
- New session/message writes must flow through existing archive-content projection helpers; the sandbox writes through the real `db` API so this is automatic.
- The observer must be non-blocking and thread-safe: it is called from sync workers and the watcher goroutine (same contract as `Emitter`, `engine.go:426-436`).
- Do not modify `AppHeader.svelte`/`NAV_ROUTES`/mobile nav: the route must be URL-reachable but invisible in navigation.
- Frontend hard-codes English strings on this page only (recorded deviation from `frontend/AGENTS.md` localization rule).
- Regenerate `openapi.yaml` + typed client via `cd frontend && npm run generate:api` after server changes.
- The observer never blocks: fan-out uses buffered channels with drop-on-full, like `server.Broadcaster` (`internal/server/broadcaster.go:125-136`).
- Commit after every task.

---

### Task 1: `internal/pipevents` — event vocabulary + recording codec

A leaf package: it must not import `internal/sync`, `internal/parser`, `internal/db`, or `internal/pipeline` (all three will import it — an import cycle otherwise).

**Files:**
- Create: `internal/pipevents/pipevents.go`
- Create: `internal/pipevents/recording.go`
- Test: `internal/pipevents/recording_test.go`

**Interfaces:**
- Produces (everything later consumes):
  ```go
  package pipevents

  // Stage identifies the pipeline stage that produced an Event.
  type Stage string

  const (
      StageWatch    Stage = "watch"
      StageResolve  Stage = "resolve"
      StageGates    Stage = "gates"
      StageParse    Stage = "parse"
      StageBatch    Stage = "batch"
      StageWrite    Stage = "write"
      StageFinalize Stage = "finalize"
      StageEmbed    Stage = "embed"
  )

  // Kind values (string constants; exhaustive list):
  //   fs_event, batch_formed, batch_dispatched, path_classified,
  //   source_resolved, dedup_winner, dedup_loser, gate_verdict,
  //   parse_started, parse_done, line_classified, message_emitted,
  //   cursor_updated, incremental_fallback, staging,
  //   result_admitted, pending_accumulated, flush_triggered,
  //   tx_started, session_written, messages_written, fts_updated,
  //   checkpoint_written, tx_committed, links_resolved, sse_emitted,
  //   sync_pass_started, sync_pass_done, scenario_step,
  //   embed_scheduled, embed_started, embed_done, embed_skipped,
  //   run_started, run_finished

  // Event is one observed step of the ingest pipeline. Producers leave
  // Seq and RunID zero; the recording hub stamps them.
  type Event struct {
      Seq     uint64         `json:"seq"`
      AtMS    int64          `json:"at_ms"`  // unix milliseconds
      RunID   string         `json:"run_id,omitempty"`
      Stage   Stage          `json:"stage"`
      Kind    string         `json:"kind"`
      Path    string         `json:"path,omitempty"`
      Payload map[string]any `json:"payload,omitempty"`
  }

  // Observer receives pipeline events. Implementations must be
  // thread-safe and must not block; called from sync workers.
  type Observer interface{ ObserveEvent(Event) }

  // ObserverFunc adapts a function to Observer.
  type ObserverFunc func(Event)
  func (f ObserverFunc) ObserveEvent(e Event) { f(e) }
  ```

Recording format (versioned JSONL, `internal/pipevents/recording.go`):

```go
const RecordingFormatVersion = 1

// RecordingHeader is line 1 of a recording file.
type RecordingHeader struct {
    Kind          string `json:"kind"`            // always "header"
    FormatVersion int    `json:"format_version"`
    RunID         string `json:"run_id"`
    ScenarioID    string `json:"scenario_id,omitempty"`
    StartedAtMS   int64  `json:"started_at_ms"`
    SourceFile    string `json:"source_file,omitempty"`
    SourceSHA256  string `json:"source_sha256,omitempty"`
}

// WriteRecording serializes header + events as JSONL to w.
func WriteRecording(w io.Writer, h RecordingHeader, events []Event) error

// ReadRecording parses a recording file back.
func ReadRecording(r io.Reader) (RecordingHeader, []Event, error)
```

- [ ] **Step 1: Write the failing round-trip test**

```go
// internal/pipevents/recording_test.go
func TestRecordingRoundTrip(t *testing.T) {
    var buf bytes.Buffer
    h := RecordingHeader{
        Kind: "header", FormatVersion: RecordingFormatVersion,
        RunID: "r1", ScenarioID: "happy-path", StartedAtMS: 123,
    }
    evs := []Event{
        {Seq: 1, AtMS: 100, Stage: StageWatch, Kind: "fs_event",
            Path: "/x/rollout-1.jsonl", Payload: map[string]any{"op": "create"}},
        {Seq: 2, AtMS: 105, Stage: StageGates, Kind: "gate_verdict",
            Payload: map[string]any{"gate": "skip_cache", "decision": "pass"}},
    }
    require.NoError(t, WriteRecording(&buf, h, evs))
    gotH, gotEvs, err := ReadRecording(&buf)
    require.NoError(t, err)
    assert.Equal(t, h.RunID, gotH.RunID)
    require.Len(t, gotEvs, 2)
    assert.Equal(t, StageGates, gotEvs[1].Stage)
    assert.Equal(t, "pass", gotEvs[1].Payload["decision"])
}

func TestReadRecordingRejectsBadVersion(t *testing.T) {
    buf := bytes.NewBufferString(
        `{"kind":"header","format_version":99}` + "\n")
    _, _, err := ReadRecording(buf)
    assert.Error(t, err)
}
```

- [ ] **Step 2:** `go test ./internal/pipevents/` — expect FAIL (package absent).
- [ ] **Step 3:** Implement `pipevents.go` + `recording.go` (JSONL: one JSON object per line via `encoding/json/v2` `jsontext` — match repo usage; header line first, then events).
- [ ] **Step 4:** `go test ./internal/pipevents/` — PASS.
- [ ] **Step 5: Commit** `feat(pipevents): add pipeline event vocabulary and recording codec`

---

### Task 2: Engine observer plumbing (`internal/sync`)

Adds a passive tap: `EngineConfig.PipelineObserver`, an atomic slot so `SetPipelineObserver` can toggle at runtime (observe mode), an `emitPipeline` helper, and emit call sites across the pass. All emissions must allocate nothing measurable when the observer is nil (check `if ob == nil { return }` first).

**Files:**
- Create: `internal/sync/pipeline_events.go`
- Modify: `internal/sync/engine.go` — `EngineConfig` (~line 523, near `Emitter`), `Engine` struct field, emit sites (see table)
- Modify: `internal/sync/watch_batch_sync.go` — batch entry points (`ApplyWatchBatch` → `SyncWatchBatchThenRun`, `engine.go:11767` `processProviderFile`, gates; `engine.go:10067` `collectAndBatchWithOptions`; write paths `engine.go:17942`, `18571`, `18626`, `19374`; finalize `emit` call sites + `link`)
- Test: `internal/sync/pipeline_events_test.go`

**Interfaces:**
- Produces:
  ```go
  // In EngineConfig (alongside Emitter):
  // PipelineObserver, when non-nil, receives fine-grained ingest
  // events. Nil disables instrumentation entirely.
  PipelineObserver pipevents.Observer

  // internal/sync/pipeline_events.go
  func (e *Engine) SetPipelineObserver(o pipevents.Observer)
  func (e *Engine) pipelineObserver() pipevents.Observer // atomic load, nil ok
  func (e *Engine) emitPipeline(stage pipevents.Stage, kind, path string,
      payload map[string]any)
  func (e *Engine) gateVerdict(file DiscoveredFile, gate, decision, reason string,
      extra map[string]any) // emits StageGates/gate_verdict
  ```
- Engine stores the observer in an `atomic.Pointer[pipevents.Observer]` (or `atomic.Value`) field `pipelineObs`; `NewEngine` seeds it from `cfg.PipelineObserver`.

**Emit sites (engine level)** — insert `e.emitPipeline`/`e.gateVerdict` at these existing functions (function names from the pipeline map; locate by grep, not line number):

| Site (file → function) | Event (stage/kind) | Payload keys |
|---|---|---|
| `SyncWatchBatchThenRun` entry + `applyWatchBatchLocked` | watch/`batch_dispatched` | `paths`, `renames`, `reconcile_roots`, `full_sync`, `lost_events` |
| `prepareChangedPathSync` / `classifyPaths` | resolve/`path_classified` | `path`, `classification` (file/dir/missing/index-sidecar) |
| `sourcesForChangedPath`-adjacent resolution in `processProviderFile` | resolve/`source_resolved` | `path`, `uuid`, `layout` (dated/flat), `key` |
| `dedupeDiscoveredFilesPreferNewestCodex` | resolve/`dedup_winner` + `dedup_loser` | `uuid`, `winner`, `loser`, `reason` |
| `processProviderFile` gate chain — one `gateVerdict` per outcome: failurecache lookup, missing source, `codexCheckpointDecision` result (unchanged/append/invalid/missing), multi-file stat digest, fingerprint, skipCache, `providerSourceUnchangedInDB`, source priority, retention budget | gates/`gate_verdict` | `gate`, `decision` (`pass`/`skip`/`fallback_full`), `reason`, plus gate-specific extras (e.g. `offset` for append) |
| `tryProviderIncrementalAppend` chosen / declined | parse/`parse_mode_selected` | `mode` (`incremental`/`full`/`staged`), `reason` |
| `providerResultAdmission` | parse/`result_admitted` | `session_hint`, `messages` |
| `collectAndBatchWithOptions` pending append + `flushPending` trigger | batch/`pending_accumulated`, batch/`flush_triggered` | `pending_count`, `pending_bytes`, `reason` (`size`/`bytes`/`pressure`/`drain`) |
| `writeBatchWithOutcomeContext` / `writeBatchBulkWithOutcomeContext` / `writeIncremental` / `writeStagedFullParse` | write/`tx_started`, write/`session_written`, write/`messages_written`, write/`fts_updated`, write/`checkpoint_written`, write/`tx_committed` | `session_id`, `inserted`, `updated`, `max_ordinal`, `fts_rows` (derived = inserted messages; document in code), `offset`, `checkpoint_version` |
| `link(ctx, e, &stats)` sites | finalize/`links_resolved` | `linked`, `repaired` |
| every `e.emitter.Emit(scope)` call site | finalize/`sse_emitted` | `scope` |
| `syncAllLocked` start/end + phase transitions (`Progress.Phase` writes) | finalize/`sync_pass_started`, finalize/`sync_pass_done`, finalize/`phase_changed` | `phase`, `stats` summary (synced/skipped/failed) |

Gate emission pattern — gates currently return early; wrap each decision point:

```go
// before an early "skip" return inside processProviderFile:
e.gateVerdict(file, "failure_cache", "skip",
    "unchanged since recorded failure", nil)
// after checkpoint decision:
e.gateVerdict(file, "codex_checkpoint", decisionKind, // "unchanged"|"append"|"invalid"|"missing"
    reason, map[string]any{"offset": cpOffset})
```

- [ ] **Step 1: failing test** — `internal/sync/pipeline_events_test.go`: build a minimal engine over `t.TempDir()` (mirror the setup used in `engine_test.go`/`engine_integration_test.go` — reuse their helpers), set a collecting observer, run a single codex file through `SyncAll`/`SyncPathsContext`, assert an ordered subsequence: `gate_verdict` events appear before `parse_started`, `parse_started` before `tx_committed`, `tx_committed` before `sync_pass_done`; assert a `gate_verdict` with `gate:"codex_checkpoint"` and `decision:"missing"` on first ingest.

```go
type collectingObs struct{ mu gosync.Mutex; evs []pipevents.Event }
func (c *collectingObs) ObserveEvent(e pipevents.Event) {
    c.mu.Lock(); c.evs = append(c.evs, e); c.mu.Unlock()
}
func TestEngineEmitsPipelineEvents(t *testing.T) {
    obs := &collectingObs{}
    // ... engine with cfg.PipelineObserver = obs, codex fixture via testjsonl ...
    // ... drive sync ...
    kinds := /* kind sequence */
    assertOrderedSubsequence(t, kinds,
        "batch_dispatched", "gate_verdict", "parse_started",
        "tx_committed", "sync_pass_done")
}
```

- [ ] **Step 2:** `go test ./internal/sync -run Pipeline` — FAIL.
- [ ] **Step 3:** Implement plumbing + all emit sites from the table.
- [ ] **Step 4:** `go test ./internal/sync -run Pipeline` — PASS; then `go test ./internal/sync -short` to check for regressions.
- [ ] **Step 5: Commit** `feat(sync): emit pipeline observer events across ingest path`

---

### Task 3: Parser observer (`internal/parser`, codex only)

**Files:**
- Create: `internal/parser/pipeline_observer.go`
- Modify: `internal/parser/codex_provider.go` — observer field + emits in `Parse`, `ParseIncremental`
- Modify: `internal/parser/codex.go` — emits inside `parseSessionFromWithSources` line loop, `codexIncrementalNeedsFullParse` fallbacks
- Modify: `internal/sync/codex_staging.go` — `staging` events (open/append/close/error)
- Modify: `internal/sync/engine.go` — after provider construction in `sources()`/wherever providers are instantiated, inject observer getter into providers implementing the interface
- Test: `internal/parser/codex_pipeline_observer_test.go`

**Interfaces:**
```go
// internal/parser/pipeline_observer.go
// ObserverSource resolves the active observer lazily so an engine can
// toggle instrumentation at runtime without rebuilding providers.
type ObserverSource func() pipevents.Observer

// PipelineObserverProvider is implemented by providers that can report
// fine-grained parse events.
type PipelineObserverProvider interface {
    SetPipelineObserverSource(ObserverSource)
}
```

`codexProvider` gains `obsSource ObserverSource` + `func (p *codexProvider) emit(kind, path string, payload map[string]any)` (nil-safe). Thread the observer into `codexStagingSink` and cursor serialization via constructor/field — use the same `ObserverSource` indirection.

**Emit sites (parser level):**

| Site | Kind | Payload |
|---|---|---|
| `codexProvider.Parse` / `ParseIncremental` entry | `parse_started` | `mode`, `offset`, `size`, `seeded` |
| each classified JSONL line inside the parse loop | `line_classified` | `line_no`, `offset`, `type` (session_meta/response_item/token_count/...), `raw` (truncate to 4 KiB, `truncated: true`) |
| each emitted `ParsedMessage` | `message_emitted` | `ordinal`, `kind`, `role`, `digest` |
| cursor marshal (`codexCursorState.MarshalBinary` call sites) | `cursor_updated` | `offset`, `pending_calls`, `first_user_seen`, `fork_gate_active`, `next_ordinal` |
| every `codexIncrementalNeedsFullParse`/`IncrementalNeedsFullParse` path | `incremental_fallback` | `reason`, `line_no` |
| `codexStagingSink` open/event/close + `stageErr` | `staging` | `op` (`open`/`append`/`close`/`error`), `bytes`, `events` |
| Parse exit | `parse_done` | `messages`, `consumed_bytes`, `safe`, `checkpoint` (bool) |

**Injection (engine side, in `internal/sync`):** wherever the engine materializes providers from `providerFactories` (inside `sources()`), add:

```go
for _, prov := range providers {
    if o, ok := prov.(parser.PipelineObserverProvider); ok {
        o.SetPipelineObserverSource(e.pipelineObserverSource)
    }
}
// engine.go / pipeline_events.go:
func (e *Engine) pipelineObserverSource() pipevents.Observer {
    return e.pipelineObserver()
}
```

- [ ] **Step 1: failing test** — parse a codex fixture (use `testjsonl.CodexSessionMetaJSON`, `CodexMsgJSON`, `CodexTokenCountJSON` to build a small rollout file in `t.TempDir()`; reuse existing `codex_parser_test.go` fixture idioms) with a collecting `ObserverSource`; assert `line_classified` count equals file line count, `message_emitted` events carry increasing ordinals, `parse_done` is last.
- [ ] **Step 2:** `go test ./internal/parser -run PipelineObserver` — FAIL.
- [ ] **Step 3:** Implement observer + emits + engine injection.
- [ ] **Step 4:** `go test ./internal/parser -run PipelineObserver` — PASS; `go test ./internal/parser -short` regressions.
- [ ] **Step 5: Commit** `feat(parser): report codex parse events to pipeline observer`

---

### Task 4: `internal/pipeline` — hub + recorder + run log

**Files:**
- Create: `internal/pipeline/hub.go`
- Create: `internal/pipeline/recorder.go`
- Test: `internal/pipeline/hub_test.go`

**Interfaces:**
```go
package pipeline

// Hub fans pipeline events out to SSE subscribers and the active
// recorder. Implements pipevents.Observer.
type Hub struct{ /* subs map[chan pipevents.Event]struct{}; runs *runLog; rec *Recorder; mu */ }

func NewHub(recordingsDir string) *Hub

// ObserveEvent stamps Seq + current RunID, appends to the run log and
// recorder, then fans out. Non-blocking per subscriber (drop on full).
func (h *Hub) ObserveEvent(e pipevents.Event)

// BeginRun opens a recording window: events until EndRun are tagged
// RunID and written to <recordingsDir>/<runID>.jsonl.
func (h *Hub) BeginRun(runID, scenarioID, sourceFile string) error
func (h *Hub) EndRun(stats map[string]any)

// Subscribe returns a channel receiving events with seq > afterSeq
// (replay from run log first, then live). Buffer cap 1024, drop on full.
func (h *Hub) Subscribe(afterSeq uint64) (<-chan pipevents.Event, func())

// RunLog returns the recorded events for a run (for GET snapshot and
// recordings).
func (h *Hub) RunEvents(runID string) []pipevents.Event
func (h *Hub) Runs() []RunInfo  // {run_id, scenario_id, state, started_at, event_count}
```

`Recorder` (`recorder.go`): opens `<dir>/<runID>.jsonl`, writes `RecordingHeader` then each event via `pipevents.WriteRecording`-compatible streaming (header first, one JSON per event, flush per event — recordings must survive a crash mid-run). Also `ListRecordings()`, `ReadRecording(name)`, `ImportRecording(name, r io.Reader)`.

- [ ] **Step 1: failing test** — `BeginRun("r1")`, emit 3 events via `hub.ObserveEvent`, `EndRun`; assert `RunEvents("r1")` has seq 1..3 and run_ids stamped; assert recording file exists in `t.TempDir()` and `pipevents.ReadRecording` round-trips; assert a subscriber with `afterSeq=1` receives exactly seq 2..3 replayed.
- [ ] **Step 2:** `go test ./internal/pipeline/` — FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS.
- [ ] **Step 5: Commit** `feat(pipeline): add event hub with run recording`

---

### Task 5: `internal/pipeline` — sandbox engine

**Files:**
- Create: `internal/pipeline/sandbox.go`
- Test: `internal/pipeline/sandbox_test.go`

**Design:**
```go
// Sandbox owns an isolated ingest pipeline: its own SQLite archive,
// its own sync.Engine pointed at a private codex root, and a real
// sync.Watcher feeding ApplyWatchBatch.
type Sandbox struct {
    Root    string // <dataDir>/pipeline-sandbox
    SrcDir  string // <root>/sessions (the AgentCodex root)
    MetaDir string // <root> (holds session_index.jsonl beside sessions/)
    // db *db.DB; engine *sync.Engine; watcher *sync.Watcher; hub *Hub
    // mu serializes runs — one file at a time by design.
}

func OpenSandbox(ctx context.Context, dataDir string, hub *Hub) (*Sandbox, error)
func (s *Sandbox) Close()
func (s *Sandbox) Reset(ctx context.Context) error // wipes db + src dir, fresh isolated db
func (s *Sandbox) Run(ctx context.Context, sc *Scenario) (runID string, err error)
```

Construction details (mirror `cmd/agentsview/main.go` wiring, shrunk to codex):
- `Root = <dataDir>/pipeline-sandbox`; `SrcDir = Root/sessions`; `MetaDir = Root`.
- DB: `db.Open(ctx, Root/sandbox.db)` for persistence across runs; `Reset` closes + recreates via `db.OpenFreshIsolatedContext` (which requires a pre-created empty regular file — `os.Create` first, per `db.go:1359-1370`).
- Engine:
  ```go
  sync.NewEngine(ctx, sandboxDB, sync.EngineConfig{
      AgentDirs:      map[parser.AgentType][]string{parser.AgentCodex: {srcDir}},
      Machine:        "playground",
      Ephemeral:      false, // keep parser_checkpoints + skipped_files — dedup scenarios need them
      PipelineObserver: hub,
      Emitter:        hub,   // reuse: hub adapts Emit(scope) → sse_emitted event too
      DisabledAgents: allAgentsExcept(parser.AgentCodex),
  })
  ```
- Watcher (honest path, `watcher.go:766` signature):
  ```go
  w, err := sync.NewWatcherWithCallback(
      50*time.Millisecond, 50*time.Millisecond,
      func(ctx context.Context, batch sync.WatchBatch) error {
          hub.ObserveEvent(pipevents.Event{Stage: pipevents.StageWatch,
              Kind: "batch_formed", Payload: batchPayload(batch)})
          return sync.ApplyWatchBatch(ctx, s.engine, batch, nil)
      },
      nil /* excludes */, sync.WatcherOptions{})
  w.WatchRecursive(srcDir)
  w.Start()
  ```
  (`WatchRecursive`/`Start` — `watcher.go:910,934`. `ApplyWatchBatch` takes `*WatchRecoveryScope`, nil is acceptable for a single-root sandbox; if `ValidateWatchBatch` rejects nil, construct the minimal scope the validation requires — check `watch_batch_sync.go` `ValidateWatchBatch`.)

`Run`:
```go
s.mu.Lock(); defer s.mu.Unlock()
runID := fmt.Sprintf("run-%d", time.Now().UnixMilli())
s.hub.BeginRun(runID, sc.ID, sc.Fixture)
defer s.hub.EndRun(...)
// execute sc.Steps against SrcDir; each step emits scenario_step events;
// WaitIdle step: block until hub sees no events for quiet window (500 ms)
// or ctx deadline.
```

- [ ] **Step 1: failing integration test** — `TestSandboxRunHappyPath`: `OpenSandbox` in `t.TempDir()`-as-dataDir, run a scenario that copies a small rollout fixture (built with `testjsonl.Codex*` helpers) into `SrcDir`; `require.Eventually(t, ..., 10*time.Second, 50*time.Millisecond)` until `hub.RunEvents` contains `tx_committed`; assert the sandbox DB actually holds the session (`db` query: session count 1, agent `codex`); assert event order watch→gates→parse→write.
- [ ] **Step 2:** `go test ./internal/pipeline -run Sandbox` — FAIL.
- [ ] **Step 3:** Implement `sandbox.go`.
- [ ] **Step 4:** PASS (this is the linchpin test — real watcher on macOS FSEvents; if flaky, poll with `Eventually`, do not shorten the budget below 1 s literals).
- [ ] **Step 5: Commit** `feat(pipeline): add sandbox engine driving real watcher ingest`

---

### Task 6: Scenario DSL + eight fixtures

**Files:**
- Create: `internal/pipeline/scenario.go`
- Create: `internal/pipeline/scenarios.go` — the built-in registry
- Create: `internal/pipeline/testdata/rollout-happy.jsonl`, `rollout-malformed.jsonl`, `rollout-append-a.jsonl` + `rollout-append-b.jsonl` (chunks), `rollout-truncated.jsonl`, `session-index.jsonl`, archive files
- Test: `internal/pipeline/scenario_test.go`

```go
// scenario.go
type Step func(ctx context.Context, rc *RunContext) error

type RunContext struct {
    SrcDir, MetaDir string
    Hub *Hub
    // helpers: CopyFixture(name), AppendFixture(name),
    // AppendRaw(s string), Remove(rel string), WriteMeta(name, content),
    // WaitIdle(quiet time.Duration)
}

type Scenario struct {
    ID, Title, Summary string
    Steps []Step
}

func Scenarios() []Scenario
```

Built-ins (`scenarios.go`), each ~5-15 lines of `Steps` — the runner emits a `scenario_step` event per step (`{n, action, detail}`):

| ID | Steps |
|---|---|
| `happy-path` | CopyFixture(rollout-happy) → WaitIdle |
| `live-append` | CopyFixture(append-a) → WaitIdle → AppendFixture(append-b) → WaitIdle (expect incremental path: `gate_verdict codex_checkpoint=append`, `parse_started mode=incremental`) |
| `reingest-unchanged` | CopyFixture(happy) → WaitIdle → touch file (same content, new mtime) → WaitIdle (expect `codex_checkpoint=unchanged` → skip) |
| `malformed-line` | CopyFixture(malformed) → WaitIdle (expect anomaly/`line_classified` with parse error + failurecache note) |
| `delete-file` | CopyFixture(happy) → WaitIdle → Remove → WaitIdle (tombstone path) |
| `truncate-rewrite` | CopyFixture(append-a+append-b concat) → WaitIdle → overwrite with shorter rollout → WaitIdle (`checkpoint invalid` → full parse) |
| `title-change` | CopyFixture(happy) → WaitIdle → WriteMeta(session-index updated title) → WaitIdle (index sidecar event → stat digest broken → re-read) |
| `cold-archive` | CopyFixture ×3 (three rollout files at once) → WaitIdle (batch flush, multiple sessions) |

Fixture authoring: copy proven line shapes from `internal/testjsonl` (`CodexSessionMetaJSON`, `CodexMsgJSON`, `CodexTokenCountJSON`, `CodexFunctionCallJSON`, `CodexFunctionCallOutputJSON`) — generate the testdata files with a throwaway `go test` helper or hand-write from those shapes; rollout filenames must match `rollout-*.jsonl` and contain a UUID in the name (`isCodexSessionFilename` + `uuidRe`, `discovery.go:776,23`).

- [ ] **Step 1: failing test** — `TestScenarioRegistry`: every `Scenarios()` entry has unique ID and ≥1 step; `TestMalformedScenario`: run against sandbox from Task 5 (reuse), assert events include a parse/anomaly signal.
- [ ] **Step 2:** FAIL.
- [ ] **Step 3:** Implement DSL + scenarios + fixtures.
- [ ] **Step 4:** `go test ./internal/pipeline/` PASS.
- [ ] **Step 5: Commit** `feat(pipeline): add scripted codex ingest scenarios and fixtures`

---

### Task 7: HTTP surface (`internal/server`) + wiring (`cmd/agentsview`)

**Files:**
- Create: `internal/server/huma_routes_pipeline.go`
- Modify: `internal/server/server.go` — `pipeline` field (hub+sandbox+real engine refs) + `WithPipelinePlayground` option (mirror the `localSyncRunner` option pattern at `server.go:631`)
- Modify: `internal/server/huma_route_groups.go` — add `s.registerPipelineRoutes()` at the end
- Modify: `cmd/agentsview/` serve path — construct `pipeline.NewHub(<dataDir>/pipeline-recordings)`, `pipeline.OpenSandbox(ctx, cfg.DataDir, hub)`, pass both + the real engine via the new option; on sandbox init failure: log + register nothing (playground absent, rest of API fine)
- Modify: `cmd/agentsview/embed_scheduler.go` — optional observer for `embed_scheduled`/`embed_started`/`embed_done`/`embed_skipped` (a `pipevents.Observer` param on the scheduler; wire hub)
- Test: `internal/server/huma_routes_pipeline_test.go`

**Endpoints** (`huma.NewGroup(s.api, "/api/v1/pipeline")` + `configureRouteGroup(group, "Pipeline")`; guard whole group on `s.pipeline != nil`):

| Op | Handler |
|---|---|
| `GET /scenarios` | `[{id,title,summary}]` from `pipeline.Scenarios()` |
| `POST /runs` `{scenario_id}` | starts `sandbox.Run` in goroutine → `{run_id}`; 409 while a run is active |
| `GET /runs` | `hub.Runs()` |
| `GET /runs/{id}/events?after_seq=` | SSE (`newHumaSSEStream`, `huma_routes.go:615`): replay buffered events then live tail via `hub.Subscribe`; `streamResponse()` option on the op |
| `POST /observe` `{run_id}` | `realEngine.SetPipelineObserver(hub)` + `hub.BeginRun(runID,"observe",...)` → live tail of the real pipeline |
| `DELETE /observe` | `SetPipelineObserver(nil)` + `EndRun` |
| `GET /recordings` / `GET /recordings/{name}` / `POST /recordings` | list / download JSONL / import |
| `POST /sandbox/reset` | `sandbox.Reset` |

- [ ] **Step 1: failing test** — `httptest` server with a sandbox in `t.TempDir()` (pattern from `server_test.go`): `GET /api/v1/pipeline/scenarios` 200 with ≥8 entries; `POST /runs{happy-path}` → run_id; read SSE until `run_finished` (bounded by `require.Eventually`-style or context deadline ≥ 5 s literals); `GET /runs/{id}/events` snapshot returns ordered seqs.
- [ ] **Step 2:** FAIL.
- [ ] **Step 3:** Implement endpoints + options + cmd wiring + embed events.
- [ ] **Step 4:** `go test ./internal/server -run Pipeline` PASS.
- [ ] **Step 5:** Regenerate API surface: `cd frontend && npm run generate:api` (runs `go run ./cmd/agentsview openapi --yaml` → `openapi.yaml` → orval → `src/lib/api/generated`). Commit generated files.
- [ ] **Step 6: Commit** `feat(server): expose codex pipeline playground API`

---

### Task 8: Frontend store — event buffer, replay, state reconstruction

**Files:**
- Create: `frontend/src/lib/stores/pipeline.svelte.ts`
- Modify: `frontend/src/lib/api/client.ts` — add `watchPipelineEvents(runId, onEvent, opts)` mirroring `watchEvents` (EventSource on `/api/v1/pipeline/runs/{id}/events`); use generated types after Task 7
- Test: `frontend/src/lib/stores/pipeline.test.ts` (vitest, `*.test.ts` beside code)

```ts
export interface PipelineEvent {
  seq: number; at_ms: number; run_id?: string;
  stage: "watch"|"resolve"|"gates"|"parse"|"batch"|"write"|"finalize"|"embed";
  kind: string; path?: string; payload?: Record<string, unknown>;
}

export interface PipelineViewState {
  fileLines: { no: number; raw: string; parsed: boolean }[]; // File tab
  parsedEvents: PipelineEvent[];                            // Events tab
  gates: { gate: string; decision: string; reason: string }[]; // Gates tab
  dbDiff: { session_id?: string; inserted: number; updated: number }[]; // DB tab
  ftsRows: number;                                          // FTS tab
  cursor: Record<string, unknown> | null;                   // Cursor tab
  queue: { pending: number; bytes: number };                // Queue tab
  activeStage: PipelineEvent["stage"] | null;               // diagram highlight
}

class PipelineStore {
  events = $state<PipelineEvent[]>([]);
  cursor = $state(0);          // replay position
  live = $state(true);         // cursor follows tail while live
  runs = $state<RunInfo[]>([]);
  viewState = $derived.by(() => reduceEvents(this.events.slice(0, this.cursor)));
  // loadRuns, startRun(scenarioId), attachLive(runId) [EventSource],
  // playRecording(file|importedRunId), seek(i), stepBy(d), play/pause(speed)
}

export function reduceEvents(evs: PipelineEvent[]): PipelineViewState
```

`reduceEvents` rules (each `kind` → state mutation): `line_classified` appends to `fileLines`+`parsedEvents`; `gate_verdict` appends `gates`; `messages_written`/`tx_committed` accumulate `dbDiff`; `fts_updated` adds `fts_rows`; `cursor_updated` replaces `cursor`; `pending_accumulated`/`flush_triggered` update `queue`; last event's `stage` → `activeStage`. Recording replay: same stream, `live=false`.

- [ ] **Step 1: failing test** — feed a synthetic event list through `reduceEvents`, assert tab contents per kind; assert `seek` correctness.
- [ ] **Step 2:** `vp test` — FAIL.
- [ ] **Step 3:** Implement store + client helper.
- [ ] **Step 4:** `vp test` PASS.
- [ ] **Step 5: Commit** `feat(frontend): add pipeline replay store`

---

### Task 9: Frontend page + route

**Files:**
- Create: `frontend/src/lib/components/pipeline/PipelinePage.svelte` — 3-pane layout: `PipelineDiagram` top, `PipelineTimeline` bottom-left, `PipelineStatePanel` right; header row: scenario `Typeahead` (kit-ui — no native selects, `frontend/AGENTS.md`), Run button, Recordings list, Record toggle
- Create: `frontend/src/lib/components/pipeline/PipelineDiagram.svelte` — 8 nodes `[watch, resolve, gates, parse, batch, write, finalize]` + `embed` satellite row; active node = `viewState.activeStage`; click node → timeline filters to that stage
- Create: `frontend/src/lib/components/pipeline/PipelineTimeline.svelte` — one row per event (`seq`, `dt` from `at_ms`, stage chip, kind, path basename); current marker at `cursor`; click row → `seek`; Play/Step/Prev controls + speed
- Create: `frontend/src/lib/components/pipeline/PipelineStatePanel.svelte` — tab strip (kit-ui `SegmentedControl`) over the 7 tabs; `Gates` = verdict table; `File` = `<pre>` with consumed/unconsumed highlight; `Cursor` = key/value dump
- Modify: `frontend/src/lib/stores/router.svelte.ts:3-30` — add `"pipeline"` to `Route` union and `VALID_ROUTES`
- Modify: `frontend/src/App.svelte` (~line 913, before final `{:else}`) — `{:else if router.route === "pipeline"}<div class="page-scroll"><PipelinePage /></div>` + import

Do **not** touch `AppHeader.svelte`, `ThreeColumnLayout.svelte` nav, or messages JSON (English-only page).

- [ ] **Step 1:** component test (vitest — check an existing `*.test.ts` under `lib/components` for mount convention; if the project mounts components in tests, assert diagram renders 8 nodes and timeline seeks on click; otherwise cover via store tests only).
- [ ] **Step 2:** Implement components + route.
- [ ] **Step 3:** `vp check` + `vp test` PASS.
- [ ] **Step 4: Commit** `feat(frontend): add hidden pipeline playground route`

---

### Task 10: End-to-end verification + docs

- [ ] **Step 1:** `make dev` (or built binary) → open `http://localhost:PORT/pipeline` → run `happy-path` → verify all 8 nodes light up in order, timeline populated, File tab shows lines with offset cursor, Gates tab shows verdicts, Cursor tab shows `codexCursorState` fields.
- [ ] **Step 2:** Run each remaining scenario; per-scenario expected verdicts (from Task 6 table).
- [ ] **Step 3:** Record a run → file appears under `<dataDir>/pipeline-recordings/` → download → import → replay scrubs identically.
- [ ] **Step 4:** Observe mode: `POST /observe`, then append a line to a real `~/.codex` session file (or wait for natural traffic) → events stream live.
- [ ] **Step 5:** `make check-timing-budgets`, `go vet ./internal/...` (or repo lint subset), `vp check` — all clean.
- [ ] **Step 6:** One paragraph in `docs/` or `AGENTS.md`-adjacent dev docs describing the tool and `/pipeline` URL. Commit.

---

## Self-review notes

- `internal/pipevents` must stay import-free of internal packages — it is the shared vocabulary of parser, sync, pipeline, server.
- `SyncWatchBatchThenRun` refuses writes in force-parse mode — sandbox never sets that; fine.
- `Ephemeral: false` on the sandbox engine is deliberate: `parser_checkpoints`, `skipped_files`, `source_failures` must persist across steps for dedup/incremental scenarios. `Reset` gives a clean slate on demand.
- FTS events report derived row counts (FTS writes happen inside db triggers in the same tx — instrumenting `internal/db` is out of scope; document the derivation at the emit site).
- Embedding events come from `cmd/agentsview/embed_scheduler.go`, not `internal/sync` — wire via scheduler param; keep optional so PG-serve mode compiles unchanged.
- If sandbox watcher proves flaky under test, fall back to invoking `sync.ApplyWatchBatch` with a synthetic `WatchBatch` in the scenario runner only for CI (keep the real watcher as the default path — it is the honest one).
