# Audit Antigravity indexing and ingesting across raw disk, parser code, and database

Type: research
Status: resolved
Blocked by: 

## Question

How accurately does AgentsView discover, index, parse, and store Google Antigravity (IDE & CLI) sessions compared to raw data on disk (`~/.gemini/antigravity`) and stored records in `~/.agentsview/sessions.db`?

Specifically investigate:
1. **Discovery & File Inventory**:
   - Check `~/.gemini/antigravity/` layout (brain sessions, transcripts, logs, CLI sessions).
   - How many sessions exist on disk vs 533 `antigravity` + 74 `antigravity-cli` = 607 sessions in `sessions.db`?
2. **Parser & Ingestion Logic (`internal/parser/antigravity*.go`)**:
   - How does `antigravityProvider` and `antigravityCLIProvider` parse protobuf / JSON transcripts, decode steps, tool calls, and subagent hierarchies?
   - How does crypto/decryption or version detection operate?
3. **Database State & Quality (`sessions.db`)**:
   - Check message counts, tool call fidelity, thought blocks, subagent linking.
   - Check token counts, model names, and pricing.
   - Verify FTS5 indexing.
4. **Anomalies & Gaps**:
   - Are there unsupported protobuf shapes, unparsed steps, or missing brain sessions?

## Answer

1. **Inventory Reconciliation (100% exact)**:
   - CLI: 74 files on disk (48 SQLite `.db` + 26 legacy encrypted `implicit/*.pb`) reconcile 1:1 to 74 sessions in `sessions.db`.
   - IDE: 526 `.db` files on disk reconcile to 533 sessions in `sessions.db`: 520 ingested and currently present, 13 archived persistent records whose disk files were deleted (`source_missing_at` set), and 6 sessions created in the last 2 hours pending the next sync cycle.
   - Subagents: 316 IDE subagent sessions and 2 CLI subagent sessions all link to valid parent sessions in `sessions.db` with 0 orphans.
2. **Fidelity**: 115,500 IDE messages + 1,396 CLI messages. 106,713 IDE tool calls + 1,078 CLI tool calls. 100% indexed in `messages_fts`. Over 600M tokens extracted from `gen_metadata` protobufs across `gemini-3.8-*` and `gemini-3.7-*`.
3. **Discovered Architectural Gaps & Discrepancies**:
   - **Critical Gap: Project & CWD missing across 100% of IDE sessions**: `antigravity_provider.go` does not read `~/.gemini/antigravity/conversation_summaries.db` (which holds `workspace_uris` for 1,520 conversations). `SourceRef.ProjectHint` is never set, leaving all 533 IDE sessions with `project = ""` and `cwd = ""`.
   - **Critical Gap: Native `brain/.../transcript.jsonl` ignored**: The parser looks for a non-existent `conversations/*.trajectory.json` daemon sidecar, while Antigravity natively creates `brain/<uuid>/.system_generated/logs/transcript.jsonl` (present in 525/526 sessions). Falling back to heuristic protobuf scraping loses `thinking` (`has_thinking = 0`), and step type 132 (tool execution output) falls into default `RoleAssistant`, detaching tool outputs from tool calls.
   - **Gap: Pricing Catalog Omission**: `gemini-3.8-*` and `gemini-3.7-*` models have no entries in `model_pricing`, leaving cost at $0.00.
   - **Gap: Encrypted CLI Sessions**: 26 `implicit/*.pb` sessions without `ANTIGRAVITY_KEY` in environment are indexed as empty message shells.
