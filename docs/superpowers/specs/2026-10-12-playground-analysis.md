# Playground: design decisions

This is the design record for the recording-analysis extension. The requester
explicitly delegated every grilling decision: choose the recommended option
without asking follow-up questions. No application, build, generator, linter,
or test runner may be executed while preparing the patch.

## Repository facts

The hidden page is currently `/pipeline`, not `/playground`. It already has
sandbox scenarios, live observation, server recording import/replay, stage and
kind filters, text search, anomaly filtering, an inspector, per-file journeys,
and event/time replay. The store can read a local File, but the page only
exposes server upload. The recording reader trusts parsed event shapes.

The frontend uses Svelte 5, TypeScript, kit-ui, and Paraglide. The pipeline
recording contract is JSONL format version 1. Sequence numbers are assigned by
a global hub, not restarted for every run. Source paths and event payloads may
contain private transcript data.

## Round 1: purpose and boundaries

### Q1. What is the missing user workflow?

Recommended and selected: compare two captured runs, and inspect a recording
without uploading it. Do not duplicate existing search or replay controls.

### Q2. Which route should open the tool?

Recommended and selected: `/playground` is an alias for the existing pipeline
route. Keep `/pipeline`, its route identifier, and all API paths compatible.
The server already provides SPA fallback, so no new backend route is needed.

### Q3. Should this extension execute arbitrary scenarios or change the daemon?

Recommended and selected: no new execution capabilities. Preserve the existing
sandbox/observe controls. Do not add a script editor, automatic scenario
runner, scheduler, database schema, dependency, or API endpoint.

## Round 2: comparison semantics (depends on Q1)

### Q4. What does an immutable baseline contain?

Recommended and selected: a summary of the complete currently captured buffer,
not the replay cursor prefix. Pin A, open or run B, compare B against A. Keep
the baseline through source switches, but only in the mounted page's memory.
Do not retain transcript payloads or write a baseline to browser storage.

### Q5. Which measurements are defensible from these events?

Recommended and selected: observed events, distinct non-empty event paths,
anomalous events, interior sequence gaps, classified lines, message inserts
from `messages_written` only, and the min/max observed timestamp span. Add
per-stage event/anomaly counts and arithmetic deltas. Never add transaction
aggregate writes to per-message writes, infer per-stage durations, or label a
lower number as automatically better. Unknown future stages remain visible.

### Q6. When is the comparison misleading?

Recommended and selected: warn if a capture lacks either run bookend or has
sequence gaps, and separately warn for different known scenario IDs. A
complete capture is not a successful run: anomalies are reported independently.
Do not invent missing events before the first global sequence number.

## Round 3: recording safety (depends on Q1, Q4, Q6)

### Q7. How should a local file enter the page?

Recommended and selected: a separate Open local action. Read the file in the
browser without calling the upload API. Preserve the existing Import action
as explicitly server-side. Make loading visible and failures non-destructive.
Last requested source wins, including switching back to a live run or leaving
the page while a file is still being read.

### Q8. What validation is required before replacing the current view?

Recommended and selected: check the header/version and event envelope,
positive safe integer sequence numbers in strictly increasing order, safe
non-negative timestamps, string identifiers, object payloads, and run identity.
Allow gaps and new event/stage names. Allow wall-clock regression; preserve
sequence order. Reject invalid JSON with its physical line number. Bound local
and downloaded recordings to 32 MiB and 50,000 events. These are safety limits,
not a performance guarantee for the non-virtualized timeline. Check downloaded
bytes incrementally instead of trusting Content-Length. Treat recording paths
and unknown kinds as data, not inherited JavaScript object property names.

### Q9. What should export contain?

Recommended and selected: the full captured buffer, regardless of the replay
cursor or filters, in the existing JSONL v1 format. Preserve an imported
header and original event sequence numbers. For live captures, derive the
header from the observed `run_started` event; refuse export if original start
metadata is unavailable rather than invent it. Warn about partial captures
and sensitive contents. Export does not upload or redact data automatically.

## Round 4: replay and delivery (depends on Q5-Q9)

### Q10. Where must pause-on-anomaly stop at high replay speeds?

Recommended and selected: immediately after the first anomalous event in the
tick, selecting that event for inspection. Do not consume later events in the
same tick. Apply the rule to both event-count and time-based replay.

### Q11. How should new controls fit the frontend?

Recommended and selected: kit-ui controls, existing design tokens, translated
new labels in every configured locale, a compact collapsible comparison
section, and responsive tables. Leave unrelated legacy English copy alone.

### Q12. What verification is allowed for this delivery?

Recommended and selected: add focused behavioral tests, inspect source and
patches, check patch applicability and text integrity. Do not execute the
project, its tests, package scripts, code generators, formatters, or compiler.
Deliver the patch with this limitation, not an unverified claim that tests pass.

All selected branches terminate in the implementation plan. Runtime behavior
and visual layout remain unverified until the project checks are run locally.
