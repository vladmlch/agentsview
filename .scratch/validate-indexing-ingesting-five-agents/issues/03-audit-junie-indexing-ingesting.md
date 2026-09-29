# Audit Junie indexing and ingesting across raw disk, parser code, and database

Type: research
Status: open
Blocked by: 

## Question

How accurately does AgentsView discover, index, parse, and store JetBrains Junie sessions compared to the raw data on disk (`~/.junie/sessions/`) and the stored records in `~/.agentsview/sessions.db`?

Specifically investigate:
1. **Discovery & File Inventory**:
   - How many session folders and `events.jsonl` files exist in `~/.junie/sessions/`?
   - Compare with the 251 Junie sessions in `sessions.db`. Are any unindexed?
2. **Parser & Ingestion Logic (`internal/parser/junie*.go`)**:
   - How does Junie handle the block-update protocol (`IN_PROGRESS` vs `COMPLETED`, last-event-wins)?
   - How are `AgentThoughtBlockUpdatedEvent`, `ViewFilesBlockUpdatedEvent`, `TerminalBlockUpdatedEvent`, `McpBlockUpdatedEvent`, and subagents parsed?
3. **Database State & Quality (`sessions.db`)**:
   - Are Junie sessions complete with all turns, or are any stuck in partial states?
   - Check tool calls, terminal outputs, thinking blocks, and token usage extraction.
   - Verify FTS5 searchability.
4. **Anomalies & Gaps**:
   - Check for dropped blocks, missing outputs, or mismatched timestamps.
