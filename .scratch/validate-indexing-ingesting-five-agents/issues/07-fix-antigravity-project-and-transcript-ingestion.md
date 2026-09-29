# Fix Antigravity IDE project extraction and native transcript ingestion

Type: task
Status: resolved
Blocked by: 

## Question

How should AgentsView update `antigravity_provider.go` and `antigravity.go` to:
1. Read `~/.gemini/antigravity/conversation_summaries.db` to extract `workspace_uris` and populate `SourceRef.ProjectHint` and session `cwd`/`project` for all IDE sessions?
2. Ingest native structured logs from `brain/<session_id>/.system_generated/logs/transcript.jsonl` (present on 525/526 sessions) rather than falling back to low-fidelity protobuf scraping, recovering `thinking_text` and properly attaching tool results to tool calls?

## Answer

1. **Project & CWD Extraction**: `antigravity_provider.go` now opens and indexes `~/.gemini/antigravity/conversation_summaries.db`, extracting `workspace_uris` to populate `SourceRef.ProjectHint` and `CwdResolution`. On cache miss, `antigravitySummaryIndex.lookup` queries `lookupAntigravitySummary`. `WatchPlan` now includes `conversation_summaries.db*`, and `SourcesForChangedPath` routes summary modifications to trigger session updates. `ContentCapabilities.Cwd` is now declared supported.
2. **Native Transcript Ingestion**: `antigravity.go` now detects and parses native structured logs at `brain/<session_id>/.system_generated/logs/transcript.jsonl`. This recovers `thinking_text` (`has_thinking = 1`), attaches tool execution outputs directly to `tool_calls.result_content`, eliminates spurious assistant messages from step type 132, and links `invoke_subagent` calls directly to child subagent sessions via `"conversationId"`.
