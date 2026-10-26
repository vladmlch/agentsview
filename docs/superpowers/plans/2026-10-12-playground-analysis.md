# Playground Recording Analysis Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Open and export local pipeline recordings and compare complete
captured buffers against an immutable baseline through `/playground`.

**Architecture:** Keep the existing pipeline route, store, and backend API.
Extract the recording codec into a pure utility; use a second pure utility for
summary calculations. The comparison component owns its in-memory baseline,
and the store serializes asynchronous source replacement.

**Tech Stack:** Existing Svelte 5, TypeScript, kit-ui, Paraglide, vite-plus/test.

**Spec:** `docs/superpowers/specs/2026-10-12-playground-analysis.md`

## Global Constraints

- No application, build, generator, linter, or test runner may be executed
  while preparing the patch.
- Do not add a script editor, automatic scenario runner, scheduler, database
  schema, dependency, or API endpoint.
- Keep `/pipeline`, its route identifier, and all API paths compatible.
- Bound local and downloaded recordings to 32 MiB and 50,000 events.
- Do not retain transcript payloads or write a baseline to browser storage.
- Keep new UI message keys synchronized across all eight configured locales.

## File responsibilities

- `frontend/src/lib/utils/pipelineRecording.ts`: JSONL v1 envelope validation,
  limits, original header construction, serialization, safe download naming.
- `frontend/src/lib/utils/pipelineAnalysis.ts`: full-buffer metrics and stage
  comparisons, with no database-derived estimates.
- `frontend/src/lib/stores/pipeline.svelte.ts`: guarded source loads, export
  snapshot, gap accounting, and exact anomaly stop semantics. Re-export the
  existing recording parser/type so current callers remain compatible.
- `frontend/src/lib/components/pipeline/PipelineRunSummary.svelte`: current
  summary, baseline lifetime, comparison warnings and compact metric tables.
- `frontend/src/lib/components/pipeline/PipelinePage.svelte`: local file and
  download actions; integrate the summary without replacing replay controls.
- `frontend/src/lib/stores/router.svelte.ts`: alias normalization only.
- Adjacent `*.test.ts` files: behavior tests; `frontend/messages/*.json`: new
  copy; `docs/internal/pipeline-playground.md`: user workflow and limitations.

## Task 1: route compatibility

**Files:** modify `frontend/src/lib/stores/router.svelte.ts` and
`frontend/src/lib/stores/router.test.ts`.

**Interfaces:** consumes the existing `parsePath()`/`RouterStore`; produces
`parsePath().route === "pipeline"` for both public paths.

- [x] Add cases for `/playground`, `/pipeline`, a trailing slash, a base path,
  query parameters and browser back/forward navigation.
- [x] Normalize only the first route segment before consulting VALID_ROUTES:

```ts
const segment = segments[0] ?? "";
const routeStr = segment === "playground" ? "pipeline" : segment;
```

- [x] Inspect that session IDs and the existing route union remain unchanged.

## Task 2: a bounded recording codec

**Files:** create `frontend/src/lib/utils/pipelineRecording.ts` and its
adjacent test; modify recording imports/re-exports in the pipeline store.

**Interfaces:** produces `PipelineRecordingHeader`,
`parsePipelineRecording(text: string): PipelineRecording`,
`serializePipelineRecording(header, events): string`,
`recordingHeaderFromEvents(runId, events): PipelineRecordingHeader`, and
`pipelineRecordingFilename(runId: string): string`. The asynchronous
`readPipelineRecordingResponse(response, isCurrent)` enforces actual byte
counts while downloading, and cancels obsolete or rejected response bodies.

- [x] Write parser cases for header-only files, CRLF/blank lines, unknown
  kinds/stages, null/array envelopes, bad fields, mixed runs, duplicated or
  decreasing sequences, gaps, regressing clocks, and both limits.
- [x] Validate before replacing state. Do not sort or silently drop events.
- [x] Check serialization round-trips metadata, original sequence numbers,
  payloads and a final newline. Bound its output with the same limits.
- [x] Test safe filenames and refusal to invent a missing run start.
- [x] Use prototype-free file/pass maps in the reducer; ensure paths such as
  `__proto__` and future event kinds such as `constructor` remain literal data.
  Verify behavior through reconstructed line contents and stage counts.

Representative contract:

```ts
const source = [header, first, { ...second, seq: first.seq + 3 }]
  .map((entry) => JSON.stringify(entry)).join("\n");
expect(parsePipelineRecording(source).events.map((event) => event.seq))
  .toEqual([first.seq, first.seq + 3]);
expect(() => parsePipelineRecording("null\n")).toThrow(/header/);
```

## Task 3: full-buffer comparison

**Files:** create `frontend/src/lib/utils/pipelineAnalysis.ts`,
`frontend/src/lib/components/pipeline/PipelineRunSummary.svelte`, and their
adjacent tests.

**Interfaces:** `summarizePipelineRun(events, metadata): PipelineRunSummary`
returns scalar metrics and fresh stage-count objects, never event payloads;
`comparePipelineStages(current, baseline)` includes the union of both stages.
The component consumes events/header/runId/dropped and owns the pinned summary.

- [x] Write literal expected totals from a fixture containing two paths,
  malformed input, a sequence gap, two `messages_written` events and a
  `tx_committed` aggregate. Assert inserts are not counted twice.
- [x] Calculate observed span as `Math.max(at_ms) - Math.min(at_ms)`, or null
  with no events. Count gaps only between adjacent global sequence numbers.
- [x] Render baseline/current/delta columns and per-stage event/anomaly
  tables; keep deltas neutral rather than claiming performance improvement.
- [x] Test pin A, replace events with B, compare, and clear. Verify mutation
  of the live event array cannot change the baseline and cursor changes do
  not change metrics. Cover partial captures and mismatched known scenarios.

## Task 4: source loading and replay correctness

**Files:** modify `frontend/src/lib/stores/pipeline.svelte.ts` and its tests.

**Interfaces:** `playRecording(source: File | string): Promise<boolean>`
reports whether this request became the current source;
`exportRecording(): { name: string; text: string } | undefined` takes the full
buffer; `recordingLoading` exposes load state. Existing methods remain usable.

- [x] Add deferred-promise cases where an older load resolves or rejects
  after a newer one, after `attachLive`, and after `detach`.
- [x] Preserve current events on read/HTTP/validation failure. Count imported
  interior sequence gaps and clear stale errors on successful replacement.
- [x] Validate File size before `.text()` and count downloaded stream bytes
  before decoding each chunk. Inspect HTTP status, cancel rejected response
  bodies and handle rejected reads.
- [x] Stop after the first anomaly in a tick in both replay modes:

```ts
this.cursor = i + 1;
this.selected = i;
this.live = false;
this.pauseTimer();
return;
```

- [x] Update the existing time-mode assertion that currently expects
  overshoot, and add high-speed and resume cases with fixed expected cursors.

## Task 5: page integration and handoff

**Files:** modify `PipelinePage.svelte`, `PipelinePage.test.ts`, all eight
`frontend/messages/*.json` catalogues and
`docs/internal/pipeline-playground.md`.

**Interfaces:** page calls `playRecording(file)` for local open (never the
upload API), downloads `exportRecording()` using a temporary Blob URL, and
passes full store events to `PipelineRunSummary`.

- [x] Add separate Open local and Export capture controls, load indicator,
  file accept filters, accessible input labels, and a privacy explanation.
- [x] Test that local open does not call importRecordingApi; failed reads
  preserve the view; export ignores cursor/filter positions and releases its
  Blob URL. Keep existing upload behavior and test it separately.
- [x] Translate new labels/warnings in en, zh-CN, zh-TW, ko, fr, ja, az, es.
  Do not edit generated Paraglide sources.
- [x] Document the pin/open/compare workflow, partial capture interpretation,
  limits, route alias, sensitive exports and lack of server changes.
- [x] Inspect the complete diff and verify patch applicability to the
  reconstructed baseline with `git apply --check`. Do not run project code.

## Deferred local verification

The checked items record authored changes and source/patch inspection only.
The behavioral tests are authored but deliberately not run in this delivery.
The project's usual frontend test/check workflow, translation generation and
browser layout/accessibility review remain local follow-up work. No success
claim is made for these checks.
