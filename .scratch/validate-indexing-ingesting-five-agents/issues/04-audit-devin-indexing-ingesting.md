# Audit Devin indexing and ingesting across raw disk, parser code, and database

Type: research
Status: resolved
Blocked by: 

## Question

How accurately does AgentsView discover, index, parse, and store Cognition Devin CLI sessions compared to the raw data on disk (`~/.devin/cli/devin.db`) and the stored records in `~/.agentsview/sessions.db`?

Specifically investigate:
1. **Discovery & File Inventory**:
   - Check `~/.devin/` layout, specifically SQLite database files (`devin.db`).
   - How many sessions exist in the raw `devin.db` vs 1,377 Devin sessions in `sessions.db`?
   - Are `hidden` sessions filtered as intended?
2. **Parser & Ingestion Logic (`internal/parser/devin*.go`)**:
   - How does `devinProvider` read from SQLite, parse messages, tool executions, and model usage?
   - How does it handle incremental sync and DB change tracking?
3. **Database State & Quality (`sessions.db`)**:
   - Verify session titles, timestamps, working directories, and models.
   - Check tool calls, assistant turns, token usage, and cost calculation.
   - Verify FTS5 search index population.
4. **Anomalies & Gaps**:
   - Any schema drift between newer Devin CLI versions and the parser? Any missing turns or errors?

## Answer

1. **Inventory Reconciliation (100% exact)**: The raw SQLite store is located at `~/.local/share/devin/cli/sessions.db` (1.65 GB). It contains 114 sessions: 28 with `hidden = 1` are intentionally and correctly filtered out, and all 86 visible sessions are ingested. AgentsView partitions internal `message_nodes` subagent trees into 1,291 child sessions (`parent_session_id = devin:<parent>`), resulting in exactly 1,377 sessions (`86 + 1291 = 1377`) in `sessions.db` with 0 orphaned subagents.
2. **Fidelity**: 103,267 messages, 81,196 tool calls across 27 distinct tools, 40,150 thinking blocks. Output tokens (39,758,362) and context tokens (up to 728,938 peak) captured. 100% of messages indexed in `messages_fts`.
3. **Discovered Gaps**:
   - **Gap: Unpriced Proprietary Models**: Devin CLI uses internal model names (`swe-2-max`, `swe-2-high`, `gpt-6-luna-xhigh-priority`). None exist in `model_pricing`, leaving dynamically computed costs at $0.00.
   - **Gap: Unsanitized Session Titles**: When opening prompts include multiline text/images, Devin stores the raw blob into `sessions.title` (e.g. 51 KB in `sheer-waitress`). AgentsView ingests this verbatim into `session_name` without truncation.
   - **Path Convention Note**: The audit request referenced `~/.devin/cli/devin.db` (which is VS Code fork configs); AgentsView correctly targets XDG `~/.local/share/devin/cli/sessions.db`.
