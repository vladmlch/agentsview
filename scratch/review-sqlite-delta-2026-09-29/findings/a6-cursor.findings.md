# Comprehensive Comparative Audit: Cursor Provider Implementation (Go vs. Python)

**Reviewer:** Cursor Provider Specialist  
**Target Repository 1 (Go):** `agentsview` (`internal/parser/cursor*.go`, `cursor.go`, `cursor_provider.go`, `cursor_store.go`, `cursor_ide.go`, `cursor_ide_provider.go`, `cursor_paths.go`, `cursor_attribution.go`, test files)  
**Target Repository 2 (Python):** `claude_viewer` (`viewer/providers/cursor/` — `__init__.py`, `discovery.py`, `harvesting.py`, `delta.py`, `models.py`, `project.py`, test files)  
**Date:** September 2026  

---

## Executive Summary

Cursor presents a bifurcated architecture with two completely distinct session sources:
1. **Cursor Agent (CLI):** Plain text and JSONL transcripts stored under `~/.cursor/projects/<project>/agent-transcripts/` with subagent directories and optional per-session SQLite history stores at `~/.cursor/chats/<workspace-hash>/<agent-id>/store.db` (holding protobuf turn trees in `blobs` and `meta` tables).
2. **Cursor IDE (GUI):** A shared VS Code-style global-state SQLite database (`state.vscdb`) located under `globalStorage/`, where chat sessions reside as JSON blobs in `cursorDiskKV` (`composerData:<uuid>` and `bubbleId:<composerId>:<bubbleUuid>`) and session headers in `composerHeaders` or `ItemTable` (`composer.composerHeaders`).

### Core Architect Gap Summary:
- **Go (`agentsview`)**: Implements **two** separate providers: `AgentCursor` (`cursor`) for CLI transcripts + `store.db` protobuf enrichment, and `AgentCursorIDE` (`cursor-ide`) for `state.vscdb` multi-session containers. Go exhibits enterprise-grade container fingerprinting (header state hashing + inode/file index identity), robust snapshot isolation (`conn.BeginTx`), and an automatic scratch-copy fallback on `SQLITE_BUSY`. However, Go's `cursor-ide` implementation suffers from three critical omissions: (1) it only parses `toolFormerData` (singular object), completely missing `toolFormerCalls` (array of objects); (2) it completely ignores the `composerHeaders` table and `subagentComposerIds`, resulting in **zero** subagent DAG lineage for Cursor IDE; and (3) it strictly decodes `bubble.Type` as Go `int`, causing decoding failures on string types (`"user"`).
- **Python (`claude_viewer`)**: Only implements a provider for `state.vscdb` (`globalStorage`), completely lacking support for Cursor CLI transcripts and `store.db`. While Python excels at extracting subagent DAG lineage (from `composerHeaders` and `composerData.subagentComposerIds`) and thinking blocks, it suffers from critical database concurrency bugs: it queries `state.vscdb` in autocommit mode without transaction snapshots, issues N+1 separate bubble queries in a Python loop, lacks scratch-copy fallback on `SQLITE_BUSY`, scrambles bubble order when `fullConversationHeadersOnly` is missing (sorting UUID keys alphabetically), and has an inverted tool-call blind spot (parses `toolFormerCalls` but drops `toolFormerData`).

---

## Part A: 5-Dimensional Scorecard & Core Invariants

### 1. Offset & Resumption Integrity
| Dimension | Go (`agentsview`) | Python (`claude_viewer`) | Verdict & Discrepancy |
| :--- | :--- | :--- | :--- |
| **CLI Transcripts** | Bounded full re-read (`io.LimitReader` up to 10MB); re-parse gated by composite fingerprint (mtime + SHA256 of transcript + SQLite state hash of `store.db`). | **Unsupported.** Python has zero discovery or parser code for `agent-transcripts` or `store.db`. | **Major Scope Gap in Python.** |
| **IDE `state.vscdb` Container** | Composite whole-database mtime (`-wal`, `-shm`) + `cursorIDESQLiteStateHash` (100-byte SQLite header change counter + device/inode identity + WAL size/header). Skip cache bypasses full hashing. | `max(mtime_main, mtime_wal)`. No inode, no device, no WAL header hash. Vacuum or atomic restore with preserved mtime is missed. | **Go is strictly superior.** Python cache invalidation is vulnerable to stale reads. |
| **IDE Member Freshness** | `cursorIDEComposerDigest`: FNV-64a hash over raw `composerData` + all bubble keys and values in `bubbleId:<cid>:*`. Gated by `UnchangedResultMTimeAndHash`. | `str(int(header.last_updated_at))`. Only checks `last_updated_at` integer timestamp in memory cache. | **Go catches in-place edits and renames; Python misses any edit that doesn't bump `lastUpdatedAt`.** |
| **Incremental Delta Streaming** | No byte-offset streaming for SQLite. Member re-parse triggered atomically on digest change. | `read_delta_steps` returns empty `DeltaReadResult` for `db#cid` paths. Live watching cannot tail SQLite. | Neither streams deltas; Go manages member replacement cleanly. |

### 2. Compaction & File Rotation Detection
| Dimension | Go (`agentsview`) | Python (`claude_viewer`) | Verdict & Discrepancy |
| :--- | :--- | :--- | :--- |
| **Session Deletion in Cursor** | `cursorIDEBatchMemberPresent` checks `listCursorIDEComposerIDs`. Vanished composers emit tombstones, retiring sessions cleanly. | Discovery drops deleted ID from `list_conversation_ids()`. Pruning depends on outer indexer. | Both detect deletion; Go provides proactive container-level tombstones. |
| **DB Vacuum / Inode Rotation** | `sourceFileHandleIdentity(f)` embeds Unix inode+device or Windows file index+volume into the cache key. Rewrite forces full re-parse. | Relies on `stat().st_mtime`. Vacuum or file replacement leaving mtime unchanged fails to trigger reload. | **Go detects rotation/vacuum; Python does not.** |
| **Wiped / NULL Bubbles Guard** | Truncation Guard: missing or 0-byte/NULL bubbles flag `IsTruncated = true`. `dropShrinkingTruncatedCursorIDEResults` prevents truncated results from overwriting fuller archived transcripts. | Missing bubbles or NULL values are skipped with `continue`. Yields gapped transcripts without warning. | **Go prevents data loss from Cursor DB compaction/wipes; Python silently corrupts archive.** |

### 3. Late Tool Results & Out-of-Order Events
| Dimension | Go (`agentsview`) | Python (`claude_viewer`) | Verdict & Discrepancy |
| :--- | :--- | :--- | :--- |
| **CLI `[Tool result]`** | Result body attached to immediately preceding `[Tool call]` via indentation detection (`isBlockBodyEnd`). | Not supported. | Go handles sequential results; out-of-order CLI tool results unsupported by format. |
| **IDE In-Bubble Tool Results** | Stored inline on `bubble.toolFormerData.result`. Detected on next sync via `cursorIDEComposerDigest`. | Stored inline on `bubble.toolFormerCalls[].output`. Detected only if `lastUpdatedAt` moved. | Go reliably captures late tool results via digest; Python misses them if timestamp is unchanged. |
| **Tool Call Representation** | Emits `ParsedToolCall` + `ParsedToolResult` on the same assistant message. Double-encodes JSON string via `json.Marshal(text)`. | Emits separate `tool_calls` and `tool_outputs` in step dictionaries. | Python structure is cleaner; Go combines call and result on single turn. |

### 4. Subagent / DAG Lineage Resolution
| Dimension | Go (`agentsview`) | Python (`claude_viewer`) | Verdict & Discrepancy |
| :--- | :--- | :--- | :--- |
| **CLI Subagents** | Derived from directory hierarchy: `<parent>/subagents/<child_id>.ext`. Sets `ParentSessionID` and `RelationshipType: RelSubagent`. | Not supported. | Go fully resolves CLI subagent hierarchy. |
| **IDE Composer Subagents** | **Completely Unimplemented.** Ignores `composerHeaders` table (`isSubagent`, `parentComposerId`) and `subagentComposerIds`. | **Fully Implemented.** Queries `composerHeaders.parentComposerId`, `isSubagent`, and `cursorDiskKV`'s `composerData.subagentComposerIds`. | **Major Gap in Go; Python is strictly superior for IDE subagents.** |
| **Subagent Filtering** | No top-level filtering for IDE. | `list_conversation_ids(include_subagents=False)` hides child runs from root views. | Python provides better UX for multi-agent DAGs. |

### 5. Token Usage, Metrics & Content Sanitization
| Dimension | Go (`agentsview`) | Python (`claude_viewer`) | Verdict & Discrepancy |
| :--- | :--- | :--- | :--- |
| **Context Token Distribution** | Largest Remainder (Hamilton): distributes `composerData.contextTokensUsed` proportional to `ContentLength`. Apportions to `ContextTokens` (context window). Sets `NoPerMessageTokenData: true`. | Largest Remainder (Hamilton): distributes `contextTokensUsed` proportional to text length. Apportions to `completion_tokens` on AI turns. | **Severe Metric Distortion in Python.** Treating prompt context tokens as generated completion tokens inflates cost. |
| **Empty Length Handling** | Assigns entire token total to `messages[0]` if `totalLen == 0`. Sum remains exact. | Distributes equally across all messages (`total_tokens // n`). Sum remains exact. | Both preserve sum invariant; Python equal split is slightly more intuitive for 0-length turns. |
| **Thinking / Reasoning Blocks** | CLI: Parses `[Thinking]`. Protobuf store: decodes field-8 `ReasoningText`. IDE: **Ignored.** | IDE: Parses `allThinkingBlocks`, `bubble.thinking`, and `capabilityType == 30`. | Python captures IDE model reasoning; Go drops it in `cursor-ide`. |
| **Content Limits** | CLI capped at 10MB via `LimitReader`. Memory-bounded stream parsing. | Reads all bubbles into memory with `cur.fetchall()` without row or byte limits. | Go enforces strict memory boundaries. |

---

## Part B: Qualitative Analysis & Deep Dive

### 1. Database Schema & Query Topology
Cursor IDE stores state in VS Code's standard SQLite format. Two tables are pivotal:
1. `composerHeaders`:
   - Columns: `composerId TEXT PRIMARY KEY, workspaceId TEXT, createdAt INTEGER, lastUpdatedAt INTEGER, isArchived INTEGER, isSubagent INTEGER, recency INTEGER, checkpointAt INTEGER, value TEXT, parentComposerId TEXT`
   - Role: Authoritative registry of all composer sessions. Contains flags for archiving, subagent hierarchy, and workspace binding.
2. `cursorDiskKV`:
   - Columns: `key TEXT PRIMARY KEY, value BLOB`
   - Key prefixes:
     - `composerData:<composerId>`: JSON metadata for the session. Contains `fullConversationHeadersOnly: [{bubbleId, type}]`, `name`, `createdAt`, `lastUpdatedAt`, `workspaceIdentifier`, `trackedGitRepos`, `contextTokensUsed`, `subagentComposerIds`.
     - `bubbleId:<composerId>:<bubbleUuid>`: JSON document for each turn.

#### Architectural Comparison of Database Readers:
- **Go (`internal/parser/cursor_ide.go`):**
  - **Single Query Surface:** Go *only* queries `cursorDiskKV`. It never queries `composerHeaders` or `ItemTable`.
  - **Transaction Snapshot:** Go opens a single deferred transaction (`conn.BeginTx`) via `beginCursorIDESnapshot`. Every read for a composer (`composerData`, individual bubbles, and the FNV-64a digest) occurs within this transaction, guaranteeing ACID snapshot isolation against live Cursor writes.
  - **Concurrency Resilience:** Wrapped in `WithSQLiteReadOnly`, which automatically intercepts `SQLITE_BUSY` and copies the SQLite database + WAL to a scratch directory (`os.MkdirTemp`) before retrying.
- **Python (`viewer/providers/cursor/discovery.py` & `harvesting.py`):**
  - **Multi-Table Harvesting:** Queries `composerHeaders`, `ItemTable`, and `cursorDiskKV`. Accurately resolves subagents and in-flight sessions.
  - **Autocommit & Lack of Isolation:** Connects with `open_ro_sqlite` without starting a transaction. In `harvesting.py:fetch_session_data`, it reads `composerData` and then runs an N+1 loop executing `SELECT "key", value FROM cursorDiskKV WHERE "key" = ?` for every bubble. If Cursor commits a write or checkpoint mid-loop, the snapshot tears.
  - **N+1 Query Overhead:** 1 query for metadata + N queries for N bubbles. For a session with 150 bubbles, Python performs 151 separate SQLite round-trips over autocommit connections.

### 2. JSON Decoding & Bubble Structural Differences

#### A. Tool Call Schema Blind Spot (`toolFormerData` vs `toolFormerCalls`)
This is the most critical functional discrepancy discovered between the two codebases:
- **Go (`cursor_ide.go:282-298`):**
  ```go
  type cursorIDEToolFormerData struct {
      ToolCallID string         `json:"toolCallId"`
      Name       string         `json:"name"`
      RawArgs    string         `json:"rawArgs"`
      Result     jsontext.Value `json:"result,omitzero"`
  }
  type cursorIDEBubble struct {
      ToolFormerData *cursorIDEToolFormerData `json:"toolFormerData"`
  }
  ```
  Go expects `bubble.toolFormerData` as a **singular object**. If a newer Cursor version writes `toolFormerCalls` (an array of calls), Go's JSON decoder ignores it, leaving `ToolFormerData == nil`. **All tool calls are silently dropped.**
- **Python (`harvesting.py:106-125`):**
  ```python
  raw_calls = bubble.get("toolFormerCalls")
  for idx, tc in enumerate(raw_calls):
      ...
  ```
  Python expects `bubble.toolFormerCalls` as a **plural array**. If an older Cursor version or specific tool write uses `toolFormerData` (as documented in Go's `testdata/cursor-ide-object-tool-result.json` and Issue #1798), Python's `bubble.get("toolFormerCalls")` returns `None`. **All tool calls are silently dropped.**

#### B. Bubble Type Decoding Rigidity
- **Go (`cursor_ide.go:293`):**
  `Type int json:"type"`. Go strictly expects a JSON number (`1` or `2`). If Cursor serializes `"type": "user"` or `"type": "1"` as a string, Go's standard unmarshaler fails with: `json: cannot unmarshal string into Go struct field cursorIDEBubble.type of type int`. The entire bubble is treated as malformed, returning an error.
- **Python (`harvesting.py:283-294`):**
  Python accepts integer `1`, integer `2`, string `"1"`, string `"2"`, string `"user"`, `"human"`, `"ai"`, and inspects fallback `role` and `sender` fields.

#### C. User Prompt Content Extraction (`rawText` vs `text`)
- **Go (`cursor_ide.go:294, 354`):**
  Go models only `Text string json:"text"`. In Cursor Composer, user prompt text is often placed in `rawText` while `text` remains empty. In Go, `content == ""` causes `cursorIDEMessageFromBubble` to return `ok = false`, dropping the user turn.
- **Python (`harvesting.py:277, 295`):**
  Python checks `bubble.get("rawText") or bubble.get("text") or ""`. User prompts are always preserved.

#### D. Bubble Ordering Fallback Corruption
- **Python (`harvesting.py:236-241`):**
  When `fullConversationHeadersOnly` is empty or missing, Python falls back to:
  `WHERE "key" >= ? AND "key" < ? ORDER BY "key" ASC`
  Keys are formatted as `bubbleId:<conversation_id>:<uuid>`. Sorting by `key ASC` orders by **random UUID string**, completely corrupting the dialogue order. User and assistant bubbles become intermingled at random.
- **Go (`cursor_ide.go:476-479`):**
  If `len(doc.Headers) == 0`, Go returns `nil, nil`, refusing to construct a corrupted dialogue without authoritative order headers.

---

## Part C: Actionable Discrepancy & Bug Register

| ID | Sev | Repo / Provider | Layer | Violated Invariant | Evidence (File:Line) | Repro Steps | Recommended Fix Sketch |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **BUG-01** | **P0** | Python (`cursor`) | delta / parse | Invariant D (Atomicity) | `viewer/providers/cursor/harvesting.py:193-257`, `discovery.py:68-84` | Open active Cursor IDE session. While agent runs a command, execute `CursorHarvester.harvest_records()`. | Wrap read in `cur.execute("BEGIN DEFERRED")` within `open_ro_sqlite`. Batch query bubbles via single range query or `WHERE key IN (...)` instead of N+1 loop. |
| **BUG-02** | **P1** | Go (`cursor-ide`) | parse | Invariant C (Tool Calls) | `internal/parser/cursor_ide.go:285-298, 370-398` | Ingest Cursor IDE bubble containing `toolFormerCalls: [...]` (array). | Add `ToolFormerCalls []cursorIDEToolCall json:"toolFormerCalls"` to `cursorIDEBubble`. If `len(ToolFormerCalls) > 0`, iterate calls; else fall back to `ToolFormerData`. |
| **BUG-03** | **P1** | Python (`cursor`) | parse | Invariant C (Tool Calls) | `viewer/providers/cursor/harvesting.py:106-116` | Ingest Cursor IDE bubble containing `toolFormerData: {...}` (singular object from Issue #1798). | In `extract_cursor_tool_calls`, check `bubble.get("toolFormerCalls")`; if None, check `bubble.get("toolFormerData")` and adapt single object into list. |
| **BUG-04** | **P1** | Go (`cursor-ide`) | delta / lineage | Invariant C (Subagents) | `internal/parser/cursor_ide.go:146-154, 584-606` | Sync Cursor session with subagent composer runs. | Query `composerHeaders` table during discovery. In `cursorIDEComposerDoc`, parse `ParentComposerId` and `SubagentComposerIds`. Populate `sess.ParentSessionID` and `sess.RelationshipType = RelSubagent`. |
| **BUG-05** | **P1** | Python (`cursor`) | discovery | Invariant A / Scope | `viewer/providers/cursor/__init__.py:51-69`, `discovery.py:37-57` | Point Python provider at `~/.cursor` containing `projects/*/agent-transcripts/`. | Port Go's `cursor.go` / `cursor_provider.go` parser for plain text and JSONL CLI transcripts into Python. |
| **BUG-06** | **P1** | Python (`cursor`) | parse | Invariant A (Fidelity) | `viewer/providers/cursor/harvesting.py:236-241` | Ingest legacy composer lacking `fullConversationHeadersOnly` where bubble UUIDs sort out-of-order. | Sort fetched bubbles by `bubble.get("createdAt")` / timestamp instead of SQLite B-Tree `key ASC`. |
| **BUG-07** | **P1** | Go (`cursor-ide`) | parse | Invariant A (Fidelity) | `internal/parser/cursor_ide.go:293, 340` | Ingest bubble JSON containing `"type": "user"` or `"type": "assistant"` string. | Change `cursorIDEBubble.Type` to custom type implementing `UnmarshalJSON` accepting both int and string representations. |
| **BUG-08** | **P1** | Go (`cursor-ide`) | parse | Invariant A (Fidelity) | `internal/parser/cursor_ide.go:294, 354, 363` | Ingest user bubble where `text == ""` and content is stored in `rawText`. | Add `RawText string json:"rawText"` to `cursorIDEBubble`. Use `firstNonEmpty(bubble.Text, bubble.RawText)`. |
| **BUG-09** | **P2** | Python (`cursor`) | ingest | Invariant E (Metrics) | `viewer/providers/cursor/harvesting.py:312-321` | Ingest session with `contextTokensUsed: 50000`. Inspect AI turns. | Change `completion_tokens: 0 if is_user else t_tokens` to allocate context tokens as prompt/context window tokens, not completion tokens. |
| **BUG-10** | **P2** | Python (`cursor`) | delta | Invariant A / Freshness | `viewer/providers/cursor/discovery.py:127-142` | Edit bubble in place or vacuum DB without updating `lastUpdatedAt` or mtime. | Port Go's `cursorIDESQLiteStateHash` (SQLite 100-byte header change counter + WAL header + inode/device) and compute member FNV-64a digest. |
| **BUG-11** | **P2** | Go (`cursor-ide`) | parse | Invariant A (Fidelity) | `internal/parser/cursor_ide.go:292-297` | Ingest Cursor IDE bubble containing reasoning in `allThinkingBlocks` or `thinking`. | Add thinking fields to `cursorIDEBubble` and populate `msg.HasThinking` and thinking blocks on `ParsedMessage`. |
| **BUG-12** | **P2** | Python (`cursor`) | delta / tools | Consistency | `viewer/providers/cursor/delta.py:27-35` vs `harvesting.py:18-31` | Run `parse_cursor_delta` vs `CursorHarvester.harvest_records` on same bash command. | Unify tool names: `delta.py` maps to lowercase (`bash`), while `harvesting.py` maps to TitleCase (`Bash`). Standardize on single canonical map. |

---

## Part D: Recommendations for Cross-Repository Convergence

1. **For Go (`agentsview`):**
   - **Adopt Hybrid Tool Decoding:** Update `cursorIDEBubble` to unmarshal both `toolFormerCalls` (array) and `toolFormerData` (object).
   - **Implement IDE Subagent Lineage:** Read `composerHeaders` table for `isSubagent` and `parentComposerId` so IDE subagent DAGs are represented in `agentsview`.
   - **Tolerate String Types and `rawText`:** Support string bubble types (`"user"`, `"ai"`) and fall back to `rawText` when `text` is empty.
   - **Extract Thinking Blocks in IDE:** Model `allThinkingBlocks` and `thinking` in `cursor_ide.go`.

2. **For Python (`claude_viewer`):**
   - **Implement ACID Read Snapshotting:** Open a read transaction (`BEGIN DEFERRED`) in `open_ro_sqlite` when harvesting bubbles to prevent torn reads and N+1 query races.
   - **Add Scratch Copy Fallback:** Port `WithSQLiteReadOnly` logic to copy `state.vscdb` + `-wal` to a temp directory on `SQLITE_BUSY`.
   - **Fix Chronological Fallback:** Sort bubbles by `createdAt` when `fullConversationHeadersOnly` is missing.
   - **Support `toolFormerData`:** Handle singular tool call objects alongside `toolFormerCalls` arrays.
   - **Correct Token Semantics:** Map `contextTokensUsed` to prompt/context window tokens, not completion tokens.
   - **Expand Scope to CLI Transcripts:** Build discovery and parsing for `agent-transcripts` and `store.db`.
