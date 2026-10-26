# Codex Pipeline Playground

Open `/playground` on the running daemon to inspect ingest events and compare
captured runs. The existing `/pipeline` address remains an alias for the same
hidden page; neither address adds a navigation entry. All API routes remain
under `/api/v1/pipeline`.

## Run or observe the pipeline

The scenario picker drives eight scripted ingest cases: cold ingest, live
append, re-ingest unchanged, malformed line, delete, truncate rewrite, title
change, and cold archive. They use an isolated codex root at
`<dataDir>/pipeline-sandbox/sessions`, a separate SQLite archive, and a real
`sync.Watcher` feeding `sync.ApplyWatchBatch`. Every stage emits the same
`pipevents` stream as production: watch, resolve, gates, parse, batch, write,
finalize, and embed.

Start runs with the Run button (`POST /api/v1/pipeline/runs`). Their events tail
over SSE at `/api/v1/pipeline/runs/{id}/events`. The stage diagram, timeline,
inspector and state panel show line classification, gate verdicts, cursor
state, write counts and per-file journeys. Search, stage/kind filters and
anomaly filtering narrow the displayed timeline without discarding events.

Observe (`POST /api/v1/pipeline/observe`) instead attaches to the daemon's real
sync engine and embedding schedulers for a bounded window. Stop observe
(`DELETE /api/v1/pipeline/observe`) detaches the tap and closes its recording.
Observation does not operate on the sandbox; it records real traffic from the
configured session roots.

Replay can advance by event count or recorded timestamps. Pause on anomaly
stops immediately after the first anomalous event encountered, even if one
fast replay tick could consume many events, and selects that event in the
inspector. Later events remain unconsumed until playback resumes.

## Compare two captured runs

1. Run a scenario or open recording A. Expand Metrics and stages to inspect
   totals, then choose Pin as baseline.
2. Open or run B. The current totals update while A's pinned summary stays
   fixed. The tables show current values, baseline values and arithmetic
   changes, including event and anomaly counts for each stage.
3. Choose Clear baseline to remove A, or Pin as baseline to replace it with
   the current capture.

Comparison uses the **full captured event buffer**, not the replay cursor's
consumed prefix and not filtered timeline rows. The baseline stores only
summary counters in the mounted page's memory; it does not retain transcript
payloads or write to local storage. Leaving the page clears the baseline.

Metrics have deliberately narrow meanings:

- Events, classified lines and anomalies count observed events; the anomaly
  definition is shared with the timeline and pause-on-anomaly behavior.
- Observed paths counts distinct non-empty `event.path` strings, not unique
  physical files. Relative and absolute spellings remain separate.
- Message inserts sums `messages_written.inserted`. It does not also add
  transaction aggregate totals, which would count the same inserts twice.
- Observed span is the difference between the greatest and least captured
  timestamps in milliseconds, not a sum of per-stage execution times.
- Missing sequence events counts interior sequence gaps. The hub assigns
  sequence numbers globally, so a first sequence greater than one does not
  itself indicate lost events.

A capture is labeled partial when a run bookend is absent or a sequence gap is
known. Its counts remain useful observations, not a claim of complete run
results. A complete capture can still contain errors. Different known
scenario IDs produce a separate warning: differences between unrelated
scenarios do not prove a regression, and lower counts are not automatically
better. Unknown future stages remain visible in the comparison table.

## Open and share recordings

Finished runs write JSONL recordings under `<dataDir>/pipeline-recordings/`.
The recordings picker lists those server-side files. Import uploads a selected
file to the connected server for later replay; its tooltip explicitly names
that destination.

**Open local** instead reads a `.jsonl` or `.ndjson` recording directly in the
browser. It never calls the recording upload API and remains usable when the
pipeline API is unavailable. The page may still request the normal scenario,
run and recording lists on mount. A local read or validation error preserves
the current view. A later recording selection, attaching a live run or leaving
the page invalidates older pending reads. A failed local read does not stop
the currently attached live stream.

Before replacing the current view, the reader validates the JSONL v1 header,
event object shapes, safe integer sequences/timestamps and run identity. It
rejects duplicated or decreasing sequences, but preserves sequence gaps,
unknown kind/stage names and regressing wall-clock timestamps. JSON errors
report physical line numbers without echoing transcript content. UTF-8 BOM,
CRLF, blank lines and header-only recordings are accepted.

Browser reads and exports are limited to **32 MiB and 50,000 events**. Local
file size is checked before reading. Server responses are read incrementally,
checking actual byte counts even without a trustworthy Content-Length. These
are browser safety limits, not a guarantee that rendering a near-limit
recording will be fast; the existing timeline is not virtualized.

**Export capture** saves the full captured buffer in the existing JSONL v1
format, regardless of filters or replay position. It preserves imported header
metadata and global sequence numbers. A live capture needs its observed
`run_started` event to reconstruct the original header; export is unavailable
when that metadata is missing. Partial captures can be exported when their
header is known, but missing events are never fabricated. Exports are snapshots
of what this browser captured, not requests to recover any missing server log.

Local opening and exporting do not upload recordings or automatically redact
them. Recordings may include private paths, raw transcript text and source
metadata. Review a recording before sharing it. No database migration, new
server endpoint or new dependency is required for this workflow.
