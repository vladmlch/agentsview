# Codex Pipeline Playground

`/pipeline` is a hidden route (no nav entry) on the running daemon that opens
the codex ingest pipeline playground — a dev tool for watching how a rollout
file travels from filesystem event to committed archive row. A scenario
picker drives eight scripted ingest cases (cold ingest, live append,
re-ingest unchanged, malformed line, delete, truncate rewrite, title change,
cold archive) against an isolated sandbox: its own codex root at
`<dataDir>/pipeline-sandbox/sessions`, its own SQLite archive, and a real
`sync.Watcher` feeding `sync.ApplyWatchBatch`, so every stage — watch,
resolve, gates, parse, batch, write, finalize, embed — emits the same
`pipevents` stream production produces. The page lights each stage node as
events arrive and replays the timeline for inspection (per-line
classification, gate verdicts, cursor state, write counts). Runs are started
via `POST /api/v1/pipeline/runs`, their events tail over SSE at
`/api/v1/pipeline/runs/{id}/events`, and each finished run leaves an NDJSON
recording under `<dataDir>/pipeline-recordings/` that the page can list,
import, and replay deterministically. `POST /api/v1/pipeline/observe`
instead taps the daemon's real sync engine and embed schedulers for a
bounded window — live ingest events from the configured session roots stream
into the observe run until `DELETE /api/v1/pipeline/observe` detaches the
hub and closes the recording, so production traffic can be watched (and
recorded) without touching the sandbox.
