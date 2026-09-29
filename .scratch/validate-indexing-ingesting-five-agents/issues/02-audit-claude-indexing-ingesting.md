# Audit Claude indexing and ingesting across raw disk, parser code, and database

Type: research
Status: open
Blocked by: 

## Question

How accurately does AgentsView discover, index, parse, and store Anthropic Claude Code sessions compared to the raw data on disk (`~/.claude/projects/`, subagent folders) and the stored records in `~/.agentsview/sessions.db`?

Specifically investigate:
1. **Discovery & File Inventory**:
   - How many session JSONL files exist across all projects in `~/.claude/projects/`?
   - Compare with the 431 Claude sessions stored in `sessions.db`. Are any projects or sessions missing?
2. **Parser & Ingestion Logic (`internal/parser/claude*.go`)**:
   - How does `claudeProvider` parse turns, tool call invocations, tool results, thinking blocks, and subagents?
   - How does lineage sniffing and parent turn caching operate?
3. **Database State & Quality (`sessions.db`)**:
   - Are subagents properly linked to their parent session via `parent_session_id`?
   - Are tool calls (bash commands, read/write tools, glob, grep) correctly recorded?
   - Check token counts (input, output, cache-creation, cache-read) and cost accuracy.
   - Are messages indexed in FTS5?
4. **Anomalies & Gaps**:
   - Check for empty sessions, truncated conversations, orphan subagents, or unparsed JSONL entries.
