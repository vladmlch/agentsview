# Audit Junie indexing and ingesting across raw disk, parser code, and database

Type: research
Status: resolved
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

## Answer

1. **Inventory Reconciliation (100% exact)**: 834 directories in `~/.junie/sessions/`. 624 are 21-byte `transcript.md` stubs created before task initialization; 1 directory is empty; 209 contain `events.jsonl`. 11 directories contain only `SystemMessageEvent` (IDE update notices) and are correctly discarded by `junieFinishSession`. The remaining 198 root sessions + 53 synthesized subagents (`junie:<rawId>--<agentId>`) yield exactly 251 sessions in `sessions.db`.
2. **Fidelity**: 1,271 messages, 428 thinking blocks (86.8% of assistant messages), 32,813 tool calls (13,303 ViewFiles, 10,356 Terminal, 5,355 Search, 2,315 FileChanges, 247 MCP, 60 Task). Total output tokens (17,762,838) match `SUM(usage_events.output_tokens)` to the single token, billing $322.5362 USD. 100% indexed in `messages_fts`.
3. **Discovered Bugs & Gaps**:
   - **Bug: Dropped `CancelAgentEvent` & `AgentTaskFailedEvent`**: 96 `CancelAgentEvent` lines on disk are top-level events, but the parser's top-level switch drops them into `default:`, causing cancelled tasks to be classified as `awaiting_user` or `clean`.
   - **Bug: Subagent spawn link collision**: When multiple subagents share the same name (e.g. `junie-cli-docs`), the name-matching loop links all parent calls to `agent-1`, leaving `agent-2` unlinked.
   - **Bug: Self-referential task tool call**: `CustomAgentBlockUpdatedEvent` on the child stream creates an unlinked task tool call inside the child session.
   - **Gap: `UserAsyncResponseEvent` drops prompt**: Question text is discarded, keeping only the answer. Unhandled `TestRunBlockUpdatedEvent` ignores IDE test runs.
