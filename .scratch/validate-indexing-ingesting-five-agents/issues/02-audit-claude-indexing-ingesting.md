# Audit Claude indexing and ingesting across raw disk, parser code, and database

Type: research
Status: resolved
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

## Answer

1. **Inventory Reconciliation (100% exact)**: 334 JSONL files exist in `~/.claude/projects/`. 4 files are symlinks to canonical subagent directories and correctly skipped by `IsRegularFile`. All 330 regular session files are 100% indexed in `sessions.db`. An additional 101 sessions in the database represent historical sessions preserved in SQLite's persistent archive after local projects were cleaned up.
2. **Fidelity**: 18,732 messages, 17,174 tool calls (99.07% paired with outputs; the only unpaired calls are 156 client-side `ToolSearch` and 4 aborted calls). 251 subagent sessions and 2 background continuations are stored with 0 orphaned parents. Context tokens (2,741,903,828) and output tokens (15,381,683) match raw transcripts down to the single token. 100% of messages indexed in `messages_fts`.
3. **Discovered Edge Cases & Gaps**:
   - **DAG Fallback from Attachment Nodes**: In Claude Code v2.1+, environment/hook events (`hook_success`, `deferred_tools_delta`) are written as `attachment` entries with their own UUIDs in the `parentUuid` chain. Because `dagEntry` filters for user/assistant messages, the parent chain across attachments appears broken, triggering fallback to `parseLinear`. Messages are not lost, but conversation branching is flattened.
   - **Archived Sessions Source State**: 101 sessions from cleaned-up projects have `file_path`s no longer on disk, but both `deleted_at` and `source_missing_at` remain NULL.
