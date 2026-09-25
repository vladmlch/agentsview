# A1 claude — SQLite indexing, ingest, and delta review

Reviewer: A1 (provider: **claude** / Claude Code). Repo HEAD under review:
`130d46b0`. Binary: `.test-data-review-sqlite-delta/bin/agentsview`
(`-tags fts5`). Sandbox: `.test-data-review-sqlite-delta/a1-claude/`
(`data/` = 58 jsonl files copied from `~/.claude/projects` incl. 19 subagents
+ 3 synthetic fixtures `cjk00000`/`trun00000`/`malf00000`; `db/` = incremental
archive; `db2/` = fresh full-parse archive for I8).

Harness for every runtime step (claude-only, one-shot, isolated):

```
AGENTSVIEW_NO_DAEMON=1 \
AGENTSVIEW_DATA_DIR=$SB/db \
CLAUDE_CONFIG_DIR=$SB/data \
bin/agentsview sync            # 57 sessions in ~0.6 s
```

`db/config.toml` scopes the run with `disabled_agents` = all 69 other
configurable providers (config.go:954-986 rejects non-configurable names such
as `chatgpt` — list was generated from providers with `FileBased || EnvVar`).

## 1. Verification matrix

| Inv | Status | Evidence |
| --- | --- | --- |
| I1 idempotent re-sync | **verified** | Runtime: second sync logged `0 sessions synced`; `sessions`, `messages`, `messages_fts` dumps byte-identical to baseline snapshots (`tmp/i1_*`). Static: freshness digest + checkpoint gates + content-hash no-op (`checkpoint.go` decision `Unchanged`; `engine.go` identical-write skip). |
| I2 append delta | **verified** | Runtime: appended 2 entries to `cjk00000` (928→1781 bytes); sync log `2 new message(s) from offset 928`; exactly ordinals 2,3 added, no dupes; `last_write_incremental=1`; `parser_checkpoints` offset 928→1781, `next_ordinal` 2→4; FTS 1499→1501 rows in parity. Static: `checkpoint.go` append decision requires `TailAnchorDigest` match + resumable `HashState` + `codexResumeHash` over `[offset,size)`; old hash state retained so the tail is not double-counted. |
| I3 truncate / rewrite / rotation | **verified** | Runtime (three sub-cases): (a) canonical duplicate truncated → canonical flipped to the larger intact duplicate per policy comment "Claude transcripts are append-only, so a larger copy is strictly newer work" (`engine.go:9280`, `pickPreferredClaudeDiscoveredFile` 9155-9208, `claudeCandidateHasAppendProgress`); content correct, freshness restamped. (b) single-copy in-place truncate 5926c5aa 427586→364259 B → `size < offset` ⇒ decision `Invalid` ("truncation: never resume", `checkpoint.go`) → full re-parse; messages 7→3, checkpoint rebuilt, FTS 1500→1496 parity. (c) rotation d250977e (new inode, new bytes, same sessionId) → inode/device gate `Invalid` → 7 old messages replaced by 2, totals 1496→1491, FTS parity. |
| I4 freshness gate | **verified** | Runtime: untouched files skipped (`0 synced`). `touch` (mtime+ctime bump, identical bytes) → log `file size unchanged at 2273733 but changed since last sync, full parse`; rows byte-identical afterwards (re-parse no-op). Static: same-size-rewrite gate requires ctime equality — "a write restores mtime but cannot restore ctime" (`checkpoint.go`); then content-hash fingerprint short-circuits to skip. Touched-but-identical costs one full re-parse — allowed by policy. |
| I5 deletion semantics | **verified (retention)** + one P2 | Runtime: deleted `f0f9c0de` file → two syncs → session row, messages, FTS, and `provider_freshness` row all retained; `source_missing_at` stays NULL; restore → clean re-sync, no dupes. Static: schema comment on `sessions.source_missing_at` — "A missing source must not hide or trash the archived session"; tombstone writer `markSessionSourceMissing` (`engine.go:7384-7425`) is only called from watch-reconciliation paths (`engine.go:7108`, `7332`) and virtual-member reconciliation (`engine.go:10513`). In **one-shot `sync` mode deletions are never tombstoned** — see F-01. |
| I6 FTS consistency | **partially verified** | Runtime: `messages_fts` row-count parity with `messages` across every mutation (1499/1501/1496/1491/1488 pairs); English `MATCH` returns correct rows; CJK rows present in `messages_fts`. **CJK `MATCH` unverified** — `messages_cjk_fts` requires the `simple` tokenizer runtime extension (`cjk_fts_runtime.go`: needs library+dictionary+fingerprint via `AGENTSVIEW_SIMPLE_DIR`), absent in the prebuilt-binary sandbox. |
| I7 index coverage | **verified** | `EXPLAIN QUERY PLAN` on read paths: project listing uses `idx_sessions_project` (+ temp b-tree for ORDER BY ended_at — P2 micro F-04); session messages use `idx_messages_session_ordinal`; machine listing uses covering `idx_sessions_machine`; tool_calls by session use `idx_tool_calls_session`; parent rollup uses `idx_sessions_parent`; FTS join hits virtual table + PK lookup. |
| I8 dedup-key stability (fresh re-parse) | **verified** | `db2` fresh full parse vs `db1` after all mutations (appends, truncates, rotation, delete+restore): `sessions` and full message content **byte-identical** (57 sessions / 1488 messages) **after** one convergence step — db1's bg-fork `09d54ba9` briefly held a stale lineage-derived prefix (52 vs 49 msgs) because I had truncated its parent earlier; a `touch` + re-parse converged it exactly. See F-02. |
| I9 capability audit (both directions) | **verified** | Static enumeration from `claude_provider.go` (~892-938) vs observed runtime: `IncrementalAppend` honored (I2), `CompositeFingerprint`/`MultiFileStatHash` **not** declared and **not** used for claude (single-file FNV-64a stat-tuple digest in `provider_freshness`; composite sidecar freshness is ICodeMate-only, `engine.go:9142` area). Declared capabilities all exercised at runtime; nothing undeclared observed. |
| I10 ingest fidelity (3 sessions end-to-end) | **verified** | (a) `8cdc084c` (real, 207 entries): 125 assistant chunks → **75 distinct `message.id` == 75 DB assistant rows**; tool_use 81 == `tool_calls` 81; tool_result 81/81 matched by id; `cwd`, `git_branch`, `model`, `first_message`, `user_message_count` exact; `token_usage` JSON verbatim, `output_tokens` promoted; message timestamp = **last chunk of the id-group** (e.g. `.071→.091Z` across thinking/text/tool_use chunks) — deterministic, documented here. (b) `agent-aec850ab7d25f7dac` (subagent): 58 chunks / 32 distinct ids → 36 rows (DAG-shaped split: 4 ids produce 2 rows each — branch-aware grouping, not id-grouping); 3/3 source text blocks contained verbatim; start/end timestamps exact; 36/36 tool calls. (c) `cjk00000` synthetic: 4 rows, ordinals 0-3, CJK content byte-exact. Naming: no summary/title lines in sources → `session_name`/`display_name` NULL (correct). |
| I11a IncrementalAppend × fingerprint interaction | **verified** | Runtime: `provider_freshness` rows == discovered files (58/58), no empty/zero digests. Freshness digest gates first, checkpoint gates second, content-hash third — three independent layers all observed firing (untouched skip, touch re-parse, identical-write no-op). |
| I11b lineage / compact re-linking on re-sync | **verified** | Static: `claude_lineage.go` content-overlap linking, fail-open on ambiguity. Runtime: I8 convergence — bg-fork `09d54ba9` re-linked to parent `5926c5aa` (`relationship_type='continuation'`) after re-parse and converged byte-identically to fresh archive. Caveat F-02. |
| I11c subagent file linking | **verified** | 19 `agent-*` files ingested as own sessions with `relationship_type='subagent'` + `parent_session_id` set; parent's `Task` tool calls carry `subagent_session_id` (`idx_tool_calls_subagent` usable): 19 links found, 18 resolve to an ingested session row; the one miss (`agent-aaf1a793ff8f2f9bf`) is a sampling artifact (file not copied into the sandbox), not a defect. |
| I11d truncation-status + malformed-line accounting | **verified** | `trun00000` (mid-line cut, no trailing NL): `is_truncated=1`, `parser_malformed_lines=1`, 2 msgs, **no** `parser_checkpoints` row — confirms the `!lastLineFailed` emission gate. `malf00000` (one garbage line mid-file): `is_truncated=0`, `parser_malformed_lines=1`, 2 msgs, checkpoint present. |

## 2. Findings

| ID | Severity | Provider | Layer | Invariant | Evidence | Repro | Fix sketch |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F-01 | P2 | claude | delta | I5 | `markSessionSourceMissing` (`engine.go:7384-7425`) only called from watch-reconcile (`engine.go:7108,7332`) and virtual-member reconciliation (`engine.go:10513`); one-shot `agentsview sync` never calls it. | Delete a session file, run one-shot `sync` twice: session/messages/FTS retained (by design), but `source_missing_at` stays NULL and the `provider_freshness` row survives, so the schema's documented tombstone signal never fires outside watch mode. | In the one-shot sweep, after discovery diff, call `markSessionSourceMissing` for claude files present in `provider_freshness`/sessions but absent on disk (same reconciliation the watch path does), or document that tombstoning is watch-mode-only. |
| F-02 | P2 | claude | delta | I8/I11b | Runtime: bg-fork `09d54ba9` archived 52 msgs with `parent_session_id=''` when its parent `5926c5aa` was truncated; after parent state changed and the fork was re-parsed, it converged to 49 msgs + `parent=5926c5aa`, byte-identical with a fresh archive. | Truncate the parent file of a bg-linked pair after both are archived; re-sync (fork unchanged → skipped, keeps stale prefix); touch the fork; re-sync → rows churn then converge. | Acceptable fail-open behavior (`claude_lineage.go`), but consider keying the fork's freshness to the linked parent's content hash so a parent change invalidates the fork's skip decision and convergence is immediate rather than touch-triggered. |
| F-03 | P2 | claude | delta | I4 | Runtime log: `file size unchanged at 2273733 but changed since last sync, full parse` after a pure `touch`. ctime gate forces a full re-parse of a 2.27 MB file whose content hash then matches and is skipped. | `touch` any large archived session file; `sync`. | Cheap hash-first probe for same-size ctime-only changes (hash the file before falling back to full reparse) would turn this into a stat-level skip; purely an efficiency note, correctness is preserved. |
| F-04 | P2 (micro) | claude | index | I7 | `EXPLAIN QUERY PLAN` for project session listing: uses `idx_sessions_project` then a temp b-tree for `ORDER BY ended_at DESC`. | Any project listing query on a large archive. | Composite `(project, ended_at)` index if project listings become hot; not a correctness issue. |

No P0/P1 findings for the claude provider.

Notes that are **not** findings (verified intended behavior, recorded so the
aggregator does not double-report):

- `tool_calls.result_content` is empty for **Read** (and Glob) results by
  default policy `result_content_blocked_categories = ["Read","Glob"]`
  (config.go:1203); `result_content_length` is always retained
  (messages.go:3350-3360 comment: "result_content (the possibly blocked body)
  is represented only by its length"). Observed: 26/26 Read calls have
  length>0, content withheld; Bash/Grep bodies stored (Grep up to 10951 B).
- Single-event tool results are stored in `tool_calls.result_content` with **0**
  rows in `tool_result_events` (dedup model, `tool_result_dedup.go:9-18`);
  multi-event results persist event rows. Observed: session 8cdc084c has 81
  matched results and 0 event rows — consistent.
- Non-linear (DAG-branched) claude parses set `ClaudeLinearParse=false`
  (`claude.go:1691`) and therefore get **no** `parser_checkpoints` row
  (`buildCodexFullParseCheckpoint`, checkpoint.go:356-399 requires a linear
  cursor); such sessions always full-re-parse. Observed: bg-fork `09d54ba9`
  and truncated `trun00000` are the only sessions without checkpoints
  (55 rows for 57 sessions). This is the safety net behind I11b convergence.
- Message timestamps use the **last** streaming chunk of a `message.id` group
  (e.g. source chunks `.071/.074/.085/.091Z` → stored `.091Z`).
- Assistant chunk entries sharing one `message.id` are grouped into one
  message row; branch boundaries can split an id into two rows (DAG-shaped
  grouping). No content loss observed either way.

## 3. Unverified items

- **CJK full-text MATCH search** (`messages_cjk_fts`): the table is gated on
  the `simple` tokenizer runtime extension
  (`internal/db/cjk_fts_runtime.go`: requires library path + dictionary path +
  fingerprint; `AGENTSVIEW_SIMPLE_DIR`). The prebuilt binary sandbox has no
  such runtime, so `messages_cjk_fts` does not exist and CJK `MATCH` cannot be
  exercised. CJK rows **are** present in `messages_fts` (porter tokenizer) and
  CJK content fidelity in `messages` is verified (cjk00000 byte-exact).
- **Watch-mode behaviors** (file-watcher reconciliation, watch-triggered
  tombstones at `engine.go:7108/7332`): out of scope for the one-shot harness;
  tombstone logic was verified statically only. This is exactly the gap behind
  F-01.

## 4. Notes for the aggregator

- Harness used `disabled_agents` (69 entries) to scope one-shot sync to
  claude; `config.go:965` rejects non-configurable providers in that list, so
  the list must be generated from `FileBased || EnvVar != ""` providers.
- Early in the review, one sync was accidentally run **without** provider
  scoping; it read all live provider roots (6153 sessions, read-only) before
  the sandbox was rebuilt claude-only. No live data was mutated; all reported
  numbers above come from the claude-only rebuild.
- A sandbox copy of `d0003548` was briefly zeroed by a failed truncation
  pipeline and restored from its surviving duplicate (`cp -p`); the live
  original had rotated away on its own. This incidentally exercised the
  duplicate-preference path (`engine.go:9280`) used in I3(a).
- Duplicate transcripts for the same session id are resolved deterministically:
  "larger copy is strictly newer work: rank size first, mtime second. The
  committed stored copy wins only while its stat still matches the stored
  metadata" (`engine.go:9280`, `dedupeClaudeDiscoveredFiles` 9044,
  `pickPreferredClaudeDiscoveredFile` 9155).
- Checkpoint/hash machinery is codex-named but shared verbatim by claude
  (`checkpoint.go`); claude sessions carry `session_kind` ('' or 'bg') and
  `claude_linear_parse` flags that gate incremental eligibility.
- Sandbox evidence artifacts (snapshots, diffs, per-invariant logs) are under
  `.test-data-review-sqlite-delta/a1-claude/tmp/`.
