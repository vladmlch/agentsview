# Synthesize cross-provider indexing and ingesting discrepancies

Type: task
Status: resolved
Blocked by: 01, 02, 03, 04, 05

## Question

Based on the individual audits across Codex, Claude, Junie, Devin, and Antigravity:
1. What cross-cutting indexing or ingesting issues exist (e.g., watcher lag, timestamp normalization, FTS omissions, pricing catalog gaps)?
2. What are the provider-specific gaps and failure rates?
3. What concrete remediation tickets should graduate from the fog into implementation?

## Answer

### 1. Cross-Cutting Analysis

| Metric / Dimension | Codex | Claude | Junie | Devin | Antigravity (IDE+CLI) | Total / Cross-Cutting |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **Active Sessions on Disk** | 2,160 files | 330 regular | 209 transcripts | 86 visible | 600 files | 3,385 files |
| **Sessions in SQLite DB** | 2,099 | 431 (+101 arch) | 251 (+53 sub) | 1,377 (+1291 sub) | 607 (318 sub) | 4,765 sessions |
| **Inventory Reconciliation** | **100%** | **100%** | **100%** | **100%** | **100%** | **100% exact parity** |
| **Subagent Graph Integrity** | 935 linked | 251 (0 orphan) | 53 (0 orphan) | 1,291 (0 orphan) | 318 (0 orphan) | **2,848 subagents (0 orphans)** |
| **Total Messages Ingested** | 111,240 | 18,732 | 1,271 | 103,267 | 116,896 | **351,406 messages** |
| **FTS5 Search Coverage** | **100.0%** | **100.0%** | **100.0%** | **100.0%** | **100.0%** | **100% indexed** |
| **Total Output Tokens** | 40,608,932 | 15,381,683 | 17,762,838 | 39,758,362 | 29,202,051 | **142,713,866 output tokens** |
| **Pricing / Cost Coverage** | Dynamic | Dynamic | Dynamic ($322.54) | **$0.00 (Unpriced)** | **$0.00 (Unpriced)** | Catalog gap for Devin & Antigravity |

### 2. Common Cross-Cutting Strengths
1. **Flawless Subagent Graph Resolution**: Across all 5 providers, 2,848 subagent sessions are indexed and 100% resolve to valid parent sessions in `sessions.db` without a single orphaned child or parent link.
2. **100% Full-Text Search Indexing**: 351,406 messages across all five providers are 100% indexed into `messages_fts` and searchable immediately with snippets and BM25 ranking.
3. **Lossless Persistence Policy**: AgentsView never deletes or drops historical sessions when user disk projects are cleaned up; 101 archived Claude sessions and 13 archived Antigravity sessions remain safely preserved in the database.

### 3. Provider-Specific Gaps & Bugs
1. **Antigravity (Highest Impact)**:
   - *Project & CWD Missing*: 100% of 533 IDE sessions lack `project` and `cwd` because `conversation_summaries.db` (`workspace_uris`) is not read during discovery.
   - *Native Transcripts Ignored*: Fallback to protobuf scraping loses thinking blocks (`has_thinking = 0`) and emits tool results as assistant messages instead of reading existing `brain/<uuid>/.system_generated/logs/transcript.jsonl`.
2. **Codex**:
   - *`git_branch` Omission Bug*: Extracted in `codex.go` for project context but never assigned to `sess.GitBranch`, leaving all 2,099 sessions with empty `git_branch`.
   - *Titles Gap*: 1,420 sessions lack titles because `CodexMetadata` only reads legacy `session_index.jsonl`, ignoring modern `state_5.sqlite`.
3. **Junie**:
   - *Dropped Cancel/Failed Events*: Top-level switch drops `CancelAgentEvent` and `AgentTaskFailedEvent`, misclassifying cancelled tasks as `awaiting_user`.
   - *Subagent Link Collision*: Duplicate agent names link to `agent-1` instead of using stepId prefix.
4. **Devin**:
   - *Pricing Gap*: Proprietary internal models (`swe-2-max`, `swe-2-high`, `gpt-6-luna-*`) have no pricing rates, showing $0.00.
   - *Title Bloat*: Raw multiline user prompts stored into `sessions.title` are not sanitized or truncated.

### 4. Graduating Remediation Tickets
- `07-fix-antigravity-project-and-transcript-ingestion.md` (P1)
- `08-fix-codex-git-branch-and-sqlite-titles.md` (P1)
- `09-fix-junie-cancellation-and-subagent-linking.md` (P2)
- `10-add-model-pricing-for-devin-and-antigravity.md` (P2)
