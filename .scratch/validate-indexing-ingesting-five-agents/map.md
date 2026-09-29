# Validate indexing and ingesting across Codex, Claude, Junie, Devin, and Antigravity

## Destination

Produce an evidence-based audit report and discrepancy matrix comparing raw on-disk sessions, parser and sync implementation, and stored SQLite database records across Codex, Claude, Junie, Devin, and Antigravity. The audit must establish whether discovery, indexing, message extraction, tool call tracking, token usage/pricing, and change watching work correctly, pinpointing any missing sessions, dropped messages, schema drift, or silent parse failures.

## Notes

- Scope covers 5 providers:
  - Codex (`~/.codex`, `internal/parser/codex*.go`, 2099 sessions in DB)
  - Claude (`~/.claude`, `internal/parser/claude*.go`, 431 sessions in DB)
  - Junie (`~/.junie`, `internal/parser/junie*.go`, 251 sessions in DB)
  - Devin (`~/.devin`, `internal/parser/devin*.go`, 1377 sessions in DB)
  - Antigravity (`~/.gemini/antigravity`, `internal/parser/antigravity*.go`, 607 sessions in DB)
- Primary data sources:
  - Raw session artifacts in developer home directories
  - Live production database at `/Users/vladislav.molchanov/.agentsview/sessions.db` (read-only queries)
  - Ingestion and sync code in `internal/parser/` and `internal/sync/`
- Database reads must be strictly read-only (`sqlite3 -readonly`); never modify or truncate production data.
- Consult `mattpocock-skills:wayfinder` and `mattpocock-skills:dispatching-parallel-agents`.
- Testing/test suite creation is deferred: user explicitly requested deep analysis of real data, code, and DB state first.
- This map coordinates the audit; implementation tickets for fixes will graduate from the synthesized findings.

## Decisions so far

- [Audit Codex indexing and ingesting across raw disk, parser code, and database](issues/01-audit-codex-indexing-ingesting.md): 2,160 files reconcile 100% to 2,099 sessions (multi-segment rollouts stitched); uncovered omitted `git_branch` bug and 1,420 untitled sessions due to unread `state_5.sqlite`.
- [Audit Claude indexing and ingesting across raw disk, parser code, and database](issues/02-audit-claude-indexing-ingesting.md): 330 regular disk files are 100% indexed with exact token parity; 251 subagents have 0 orphan parents; identified attachment nodes in v2.1+ triggering DAG-to-linear fallback.
- [Audit Junie indexing and ingesting across raw disk, parser code, and database](issues/03-audit-junie-indexing-ingesting.md): 209 transcript dirs reconcile 100% to 251 sessions (198 root + 53 subagents) billing $322.54; uncovered dropped top-level `CancelAgentEvent` and duplicate subagent name collisions.
- [Audit Devin indexing and ingesting across raw disk, parser code, and database](issues/04-audit-devin-indexing-ingesting.md): 86 visible sessions in `~/.local/share/devin/cli/sessions.db` partition cleanly into 1,377 sessions (1,291 subagents, 0 orphans); proprietary models unpriced ($0.00) in catalog.
- [Audit Antigravity indexing and ingesting across raw disk, parser code, and database](issues/05-audit-antigravity-indexing-ingesting.md): 74 CLI and 533 IDE sessions reconcile 100%; discovered that 100% of IDE sessions lack `project`/`cwd` (unread `conversation_summaries.db`) and native `brain/.../transcript.jsonl` is ignored in favor of protobuf scraping.
- [Synthesize cross-provider indexing and ingesting discrepancies](issues/06-synthesize-cross-provider-indexing-discrepancies.md): Cross-provider audit confirmed 100% inventory reconciliation (4,765 sessions total), 100% FTS5 indexing (351,406 messages), and 0 orphan subagents (2,848 subagents), while graduating four focused remediation tickets.

## Not yet specified

- Long-term automated regression testing harness for live session formats.
- Vector search semantic indexing validation across these providers.
- Keychain-based auto-discovery of `ANTIGRAVITY_KEY` for legacy encrypted CLI transcripts.

## Out of scope

- Writing unit/integration test suites during this initial audit phase (per user request).
- Modifying or migrating the production `sessions.db` database.
- Providers other than the requested five (Codex, Claude, Junie, Devin, Antigravity).
