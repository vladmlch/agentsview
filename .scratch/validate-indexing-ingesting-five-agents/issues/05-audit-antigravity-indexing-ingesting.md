# Audit Antigravity indexing and ingesting across raw disk, parser code, and database

Type: research
Status: open
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
