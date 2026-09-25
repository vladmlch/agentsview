# A2 codex — SQLite indexing / ingest / delta review

Agent: A2 (codex). Provider under review: **codex** (Codex CLI) only. The shared
sync engine (`internal/sync`) was treated as a black box except where the
provider's contract points into it (`checkpoint.go`, `codex_staging.go`,
`verified_source_gate.go`, engine gate call sites).

Sandbox: `.test-data-review-sqlite-delta/a2-codex/` (data/ = 45 rollout files
+ real `session_index.jsonl` + `state_5.sqlite` copied from `~/.codex`;
db/ = fresh archive + config with `port = 0`; tmp/ = probes). Prebuilt binary
`.test-data-review-sqlite-delta/bin/agentsview` (HEAD, `-tags fts5`). All sync
runs used isolated env: `CODEX_SESSIONS_DIR=<sandbox>/data/codex/sessions`,
`AGENTSVIEW_DATA_DIR=<sandbox>/db/agentsview-data`, empty Claude/Gemini roots,
`AGENTSVIEW_NO_DAEMON=1`. 43 codex sessions / 1683 messages ingested.

Baseline tests (all pass, `SDKROOT=/Library/Developer/CommandLineTools/SDKs/MacOSX26.5.sdk`):
`go test -tags fts5 ./internal/parser/ -run 'Codex' -count=1` and
`./internal/sync/ -run 'Codex'`.

## 1. Verification matrix

| Inv | Result | Evidence |
|-----|--------|----------|
| I1 idempotent re-sync | verified | Runtime: second `sync` on unchanged tree printed "Sync complete: 0 sessions synced"; sessions=43, messages=1683 stable; message-level sha256 dump (`tmp/msgs1.sha` vs `tmp/msgs2.sha`) byte-identical. |
| I2 append delta incl. partial-line tails | verified | Runtime: appended valid line to a synced rollout → `last_write_incremental=1`, message_count +1, checkpoint offset==new size. Programmatic split of a valid line (`tmp/partial_a.bin`+`partial_b.bin`): mid-line flush leaves checkpoint offset/message_count frozen at last complete-line boundary (offset 496409 unchanged); completing the line ingests exactly once, no duplicate ordinals. Static: `codexSafeResumeOffsetFile` (internal/parser/codex.go:2798) requires byte at offset-1 to be `\n`; full parse sets checkpoint only at line-boundary EOF. |
| I3 truncate / rewrite / rotation | verified | Runtime: in-place rewrite (same size, new content) → full replace, `last_write_incremental=0`, new hash, messages replaced (no duplicates). Rotation (rename + new file at old path) → inode/device mismatch → `codexCheckpointInvalid` → full reparse. Truncate-to-zero → session re-keyed to filename fallback `codex:rollout-<file>` (session_meta lost), old key cleanly replaced (no orphan rows: sessions count stable, zero messages under stale key); restoring content re-keys back. Static: checkpoint.go:193 `size < offset → Invalid // truncation: never resume`. |
| I4 freshness gate | verified | Runtime: `python3 os.utime` touch of all 44 transcripts (mtime-only, identical bytes) → "0 sessions synced"; `provider_freshness` holds 43 rows; stat-digest skip path hit (engine.go:11847-11868). Touched-but-identical skips per UnchangedResultPolicy. |
| I5 deletion semantics | verified | Runtime: deleted one rollout file → one-shot sync retains the session (no tombstone; 43→42 files on disk, DB still 43). Static: `tombstoneMissingWatchSourcesLocked` exists for watch-root reconciliation only; one-shot CLI sync never deletes. Documented retention semantics. |
| I6 FTS consistency incl. CJK | verified | Runtime: `messages_fts` row parity 1683==1683 with messages; session content searchable (e.g. "superpowers" hits codex:01a0e9d3). CJK sample (Japanese text in session codex:01a0edfd): stored intact; bare `日本語` returns 0 rows in `messages_fts` because unicode61 treats the CJK run as one token — `日本語*` prefix and exact phrase match DO find it; `messages_cjk_fts` (jieba sidecar) absent in this sandbox build (lazily created, extension-gated; internal/db/cjk_fts_search.go routes CJK queries there when present). Not an ingestion defect. |
| I7 index coverage | verified | Runtime: `EXPLAIN QUERY PLAN` for session list by agent, messages by session+ordinal, FTS join, tool_calls by message, token-usage lookup — all use indexes (no SCAN of messages/tool_calls beyond FTS-MATCH). |
| I8 dedup-key stability across fresh archives | verified | Runtime: second fresh archive (`db/fresh/`) full-parse: identical session ids and message hash (`af3dd389…` aggregate) excluding the deliberately deleted session. Also proves incremental-vs-full parse parity (I11a corroboration). |
| I9 capability audit (both directions) | verified | Declared (codex_provider.go:1411-1456): DiscoverSources/StreamingDiscovery/WatchSources/ActivityHints/ClassifyChangedPath/FindSource/CompositeFingerprint/IncrementalAppend/ForceReplaceOnParse/VerifiedLocalStat/MultiFileStatHash all Supported; MultiSessionSource/PerSessionErrors/ExcludedSessions NotApplicable; ProviderSyncSemantics FingerprintHashInCacheKey+FingerprintHashRequiredForFreshness+SkipCacheFreshWithoutStoredRow all true; RawCapture Files+AppendOne, SnapshotNone. Observed: IncrementalAppend via `last_write_incremental=1` rows; ForceReplaceOnParse via rewrite test; VerifiedLocalStat+MultiFileStatHash via touch test and `provider_freshness` rows; CompositeFingerprint via unchanged skips. No capability declared-but-unexercised or exercised-but-undeclared found. |
| I10 ingest fidelity (3 sessions) | verified | (a) plain `codex:01a0e9d3-…`: project/cwd, git_branch `pedals`, timestamps, tool_calls (7==7), token-usage rows (8==8) match source; user-message count 1 vs 2 source user records explained by documented stripping of injected `# AGENTS.md instructions…` and `<environment_context>` blocks. (b) fork `codex:01a0e92f-…`: `forked_from_id` → relationship_type=fork, parent codex:01a0e91c-…. (c) subagent `codex:01a0e988-…`: `source.subagent.thread_spawn`+`parent_thread_id` → relationship_type=subagent, same parent; session_name `a6_reviewer` from agent_path leaf fallback. |
| I11 provider-specific | verified | See breakdown below. |

### I11 breakdown

- **(a) cursor vs hash consistency — verified (static + runtime).**
  Decision tree in internal/sync/checkpoint.go:71-239: `unchanged` requires
  inode+device+changeTime+size==offset (+mtime or index-only bump);
  `append` requires identity match + tail anchor digest match
  (`codexCheckpointAnchorMatches`, checkpoint.go:201) + resumable hash state
  whose digest == `cp.Hash` (checkpoint.go:216-220), then resume-hashes only
  `[offset, size)`; truncation (`size < offset`) → `Invalid` → full reparse.
  Cursor-says-nothing-new-but-hash-changed cannot occur silently: append
  branch proves prefix via anchor+hashState; unchanged branch is stat-identity
  gated with ctime catching same-size same-mtime rewrites (checkpoint.go:162-171).
  Hash-changed-but-cursor-invalid → `Invalid` → full parse (no stale resume).
  `IncrementalNoNewData` returns without writes (engine.go:15581).
  Cross-checks in checkpoint_review_test.go: append-during-checkpoint, stale
  checkpoint vs newer DB offset, cold-restart resume parity, audit repairs
  same-stat rewrite. Runtime: I2/I3/I4/I8 exercises all reached expected
  decisions (`last_write_incremental` flags + stable hashes).

- **(b) staging crash mid-stage recovery — verified (static).**
  internal/sync/codex_staging.go: scratch DB `agentsview-codex-stage-*.sqlite`
  (journal_mode=OFF); `stageErr` is sticky — once set, all later event appends
  and the publish fail (lines 84-99), so a disk-full/I/O error can never
  commit a partial batch. Publish via `InsertEventsTx` runs inside the caller's
  main-DB transaction (lines 843-849): "the transaction itself only ever
  modifies main, so the cross-database crash-atomicity limit for WAL-mode
  attached databases is respected" — publish is crash-atomic. Orphan scratch
  cleanup: `prepareCodexStagingDir` removes scratch files older than 24h
  (line 142). Tests: codex_staging_test.go
  (TestDrainResultsReleasesStagedScratchAndGCGuard,
  TestCodexStagingBlockedContentNeverEntersScratch,
  TestCodexStagingDropsOrphanResultEvents,
  TestCodexStagedParseCancellation, TestCodexEngineStagedSyncParity,
  TestCodexEngineResyncBulkStagedParity, streaming-vs-legacy parity).

- **(c) sidecar signature changes — verified (static + runtime).**
  `codexSidecarSignature` (internal/sync/verified_source_gate.go:317): primary
  index fills sidecar stat fields, alias-home indexes fold into FNV digest,
  fails closed on non-regular/no-changeTime; `CodexMetadata.EffectiveMtime`
  takes max(transcript, index mtimes) (codex_metadata.go:149-158). Runtime:
  index mtime-only touch with no title change → 0 sessions synced
  (indexOnlyBump exception, checkpoint.go:177-181); appended title entry to
  the effective index (`data/codex/session_index.jsonl`, made newer than
  `state_5.sqlite`) → rename detected, session re-parsed, stored
  `session_name` updated to REVIEW-RENAMED-FORK. Index merge is mtime-ordered
  across `session_index.jsonl` + `state_*.sqlite` (codex_metadata.go:102-109);
  an empty title in the newest index wins over a non-empty older one
  (verified: state_5.sqlite held the session with `title=''` and masked the
  older jsonl entry). Tests: codex_sidecar_signature_test.go,
  codex_metadata_test.go (alias-home precedence, corrupt-index tolerance),
  engine_test.go:11859-11997 (codexIndexSessionNameChanged paths).

- **(d) user-text resync — verified (static + runtime).**
  Data version 113 triggers full resync stripping injected AGENTS.md /
  environment_context blocks; codex_user_text_resync_test.go covers versions
  111/112 → NeedsResync → full resync repairs. Runtime: I10(a) shows the
  stripped blocks absent from stored user messages while genuine user text
  is preserved.

- **(e) replay simulator append+truncate coverage — partially verified.**
  internal/parser/codex_replay_simulator_test.go: two tests
  (TestCodexCapturedForkReplayTotals, TestCodexCapturedForkLineReplayTotals)
  replay 5 fork children line-by-line with incremental parse + cursor-cache
  Put per line; assert 171 messages / 96476 output tokens / cost parity.
  Both **skip unless `AGENTSVIEW_CODEX_REPLAY_ROOT` is set** (not run in CI
  baseline; not run here — env not provided). Coverage is append-only:
  **no truncate sequence is exercised by the simulator**. Truncate/rewrite
  recovery is instead covered by checkpoint tests (checkpoint_review_test.go
  same-stat rewrite repair; checkpoint.go:193 truncation → Invalid) and by
  my runtime I3 experiments. Residual risk is low but the simulator's
  append+truncate matrix the task asked about does not exist as one test.

## 2. Findings

Template: `ID | severity | provider | layer | violated invariant | evidence file:line | repro steps | fix sketch`

- A2-CODEX-1 | P2 | codex | delta | I3 (edge) | internal/parser/codex.go:2255 (session id = `codex:`+session_id or filename fallback); runtime: truncate-to-zero re-keyed session to `codex:rollout-<filename>` | Copy a synced rollout, truncate to 0 bytes, run one-shot sync: stored session id changes from `codex:<uuid>` to filename fallback; restoring content re-keys back. No orphan rows or duplicate messages observed. | Consider keeping the prior session id (checkpoint row survives) and marking the session empty until session_meta reappears, to avoid id churn in external references.
- A2-CODEX-2 | P2 | codex | index | I6 (edge) | internal/db/cjk_fts_search.go (jieba routing gated by HasCJKFTS); runtime: bare CJK query `日本語` → 0 rows in messages_fts; prefix `日本語*` and exact phrase work | Ingest a session containing CJK text into an archive where the jieba extension/sidecar is unavailable; query the bare CJK term via messages_fts: no hit, though prefix/phrase match succeeds. | Document that CJK recall requires the jieba sidecar (messages_cjk_fts); consider auto-creating it when CJK content is detected during ingest.
- A2-CODEX-3 | P2 | codex | delta | I11(e) (coverage gap) | internal/parser/codex_replay_simulator_test.go:19-22,112-115 (env-gated skip; append-only) | `go test ./internal/parser/ -run CodexCapturedFork` without `AGENTSVIEW_CODEX_REPLAY_ROOT` → both tests skip; no committed fixture exercises append+truncate replay. | Commit a small sanitized rollout fixture under testdata and add a truncate-and-rewrite sequence to the line-replay test so CI covers the append+truncate matrix.

No P0/P1 findings for the codex provider.

## 3. Unverified items

- **Watch-mode (daemon) reconciliation paths** — one-shot CLI sync was the
  runtime vehicle; `tombstoneMissingWatchSourcesLocked` and watch-root
  deletion tombstoning were verified statically only. Runtime watch testing
  was out of scope for the sandbox (no daemon).
- **codex_replay_simulator tests** — env-gated (`AGENTSVIEW_CODEX_REPLAY_ROOT`
  unset here); reviewed statically, not executed. Append path additionally
  corroborated by I2 runtime experiments.
- **S3 / remote codex sources** (`pathRewriter` branches) — declared
  fail-closed (checkpoint gate skipped for rewritten paths,
  checkpoint.go:113-118); not exercised.
- **Crash mid-publish** — crash-atomicity argued from the single-main-tx
  design (codex_staging.go:843-849) and sticky stageErr; no fault-injection
  run.

## 4. Notes for the aggregator

- Banner wording: "Sync complete: N sessions synced" counts only root
  (non-fork/non-subagent) sessions — a 43-session DB can legitimately print
  "9 sessions". Aggregator should not treat banner==DB-count mismatches as
  findings.
- Sandbox layout note: index discovery uses `codexSessionRootForIndex`
  (walks up to the `sessions` path segment) so sidecars live at the *parent*
  of the sessions root (`data/codex/session_index.jsonl`,
  `data/codex/state_5.sqlite`), not inside `sessions/`. Index merge across
  jsonl+sqlite is mtime-ordered, newest wins, and an empty title in the
  newest index masks older non-empty ones (verified; matches
  codex_metadata.go:59-110).
- Stored `session_name` for sessions whose newest index entry is an empty
  title cannot be re-emptied by a later sync: `codexIndexSessionNameState`
  treats "no non-empty index entry" as "no rename signal, not a rename to
  empty" (engine.go:16239-16246) — deliberate, avoids re-parse loops when
  producers stop writing session_index.jsonl. Observed live when restoring
  the renamed title: empty title in state_5.sqlite did not clear the stored
  name. This is documented behavior, not a finding.
- All runtime evidence (commands + outputs) is reproducible from sandbox
  `.test-data-review-sqlite-delta/a2-codex/`; probe program at
  `tmp/probe_title/`.
