# Audit Devin indexing and ingesting across raw disk, parser code, and database

Type: research
Status: open
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
