# Audit Codex indexing and ingesting across raw disk, parser code, and database

Type: research
Status: open
Blocked by: 

## Question

How accurately does AgentsView discover, index, parse, and store OpenAI Codex sessions compared to the raw data on disk (`~/.codex/sessions`, `~/.codex/archived_sessions`, `.codex/state_*.db`) and the stored records in `~/.agentsview/sessions.db`?

Specifically investigate:
1. **Discovery & File Inventory**:
   - How many session files exist on disk under `~/.codex/` (sessions, archived_sessions, subdirectories)?
   - Compare disk count with the 2,099 Codex sessions in `sessions.db`. Are any sessions present on disk but missing from the database? Why (e.g. discovery rules, parse errors, file extensions)?
2. **Parser & Ingestion Logic (`internal/parser/codex*.go`)**:
   - How does the Codex rollout parser extract messages, turns, roles (`user`, `assistant`, `system`), tool calls, and results?
   - How does it handle incremental parsing and streaming updates?
3. **Database State & Quality (`sessions.db`)**:
   - Are there Codex sessions with 0 messages, missing titles, or null/future timestamps?
   - Are tool calls (`function_calls`, bash, file edits) captured with full input/output?
   - Are token usage (`input_tokens`, `output_tokens`, `cache_read_tokens`) and costs populated accurately?
   - Are messages properly indexed into `messages_fts`?
4. **Anomalies & Gaps**:
   - Identify any failed sessions, corrupted records, schema drift, or silent data loss.
