# Audit Codex indexing and ingesting across raw disk, parser code, and database

Type: research
Status: resolved
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

## Answer

1. **Inventory Reconciliation (100% exact)**: 2,160 raw `.jsonl` files on disk (2,153 in `sessions/`, 7 in `archived_sessions/`) reconcile mathematically to 2,099 sessions in `sessions.db`:
   - 35 files are continuation segment suffixes (`_<segment_uuid>`) mapping to 11 multi-segment sessions stitched by `resolveContinuationChain`.
   - 26 files are un-suffixed paginated continuation rollouts stitched to ancestor root sessions.
   - 3 files are indented multi-line JSON rollouts (`019d95fd...`, `019d9fe3...`, `019fc467...`) that fail the line reader.
   - Formula: `2160 - 35 - 26 = 2099` sessions in DB.
2. **Fidelity**: 111,240 messages, 76,066 tool calls across 58 tools, 100,903 assistant messages with exact `reasoning_effort` ratings. 40.6M output tokens and 10.82B context tokens captured. 100% indexed in `messages_fts`.
3. **Discovered Bugs & Gaps**:
   - **Bug: `git_branch` omitted**: `internal/parser/codex.go` extracts `payload.Get("git.branch")` for project extraction context but never assigns it to `sess.GitBranch`, leaving all 2,099 sessions with empty `git_branch`.
   - **Bug: Pretty-printed JSON failure**: 3 indented files fail the single-line JSONL reader, resulting in empty sessions.
   - **Gap: Missing titles from `state_5.sqlite`**: 1,420 sessions lack titles because `CodexMetadata` only reads legacy `session_index.jsonl` (826 lines), missing 2,043 titles in modern Codex's `.codex/state_5.sqlite`.
   - **Gap: Misdeclared `Thinking`**: Codex encrypts reasoning (`encrypted_content`), so plaintext thought text is never available, though `ReasoningEffort` is supported. `SourceVersion` is also unpopulated.
