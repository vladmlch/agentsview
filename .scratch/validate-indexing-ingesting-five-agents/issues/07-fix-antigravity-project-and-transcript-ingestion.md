# Fix Antigravity IDE project extraction and native transcript ingestion

Type: task
Status: open
Blocked by: 

## Question

How should AgentsView update `antigravity_provider.go` and `antigravity.go` to:
1. Read `~/.gemini/antigravity/conversation_summaries.db` to extract `workspace_uris` and populate `SourceRef.ProjectHint` and session `cwd`/`project` for all IDE sessions?
2. Ingest native structured logs from `brain/<session_id>/.system_generated/logs/transcript.jsonl` (present on 525/526 sessions) rather than falling back to low-fidelity protobuf scraping, recovering `thinking_text` and properly attaching tool results to tool calls?
