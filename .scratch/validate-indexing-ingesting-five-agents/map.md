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

<!-- the index: one line per closed ticket, enough to judge relevance, then zoom the link for the detail the ticket holds -->

## Not yet specified

- Remediation tickets for any discovered parser bugs or dropped session data (will graduate once audits complete).
- Long-term automated regression testing harness for live session formats.
- Vector search semantic indexing validation across these providers.

## Out of scope

- Writing unit/integration test suites during this initial audit phase (per user request).
- Modifying or migrating the production `sessions.db` database.
- Providers other than the requested five (Codex, Claude, Junie, Devin, Antigravity).
