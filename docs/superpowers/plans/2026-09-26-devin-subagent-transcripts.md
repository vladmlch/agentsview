# Devin Subagent Transcripts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Import transcript-backed Devin subagent trees as parent-linked child sessions so AgentsView's existing sidebar hierarchy can show and open them.

**Architecture:** Partition the Devin `message_nodes` forest by its observed subagent root markers. Parse each complete child tree into a session with an ID scoped by the parent session and root node ID, and emit it with the parent from the same complete parse result. Keep `run_subagent` and `read_subagent` calls as parent transcript events; do not count them as child sessions. Parent-scoped source ownership must include the child virtual paths so complete reparses reconcile children that disappear.

**Tech Stack:** Go parser and sync pipeline, SQLite archive, Markdown format evidence.

**Spec:** `docs/internal/session-format-sources.md#Devin-CLI` and the approved design: transcript-backed subagent trees become child sessions; tool-call-only activity stays in the parent transcript.

## Global Constraints

- Preserve SQLite archives; parser data-version changes use the existing non-destructive full-resync flow.
- Scope each child identity by its parent session and root Devin node ID.
- Keep IDs for child messages scoped to the parent Devin source session because node IDs are local to that source session.
- Preserve existing user changes outside the Devin parser, sync ownership, archive version, and Devin format-evidence files.

---

### Task 1: Partition Devin message-node trees

**Files:**
- Modify: `internal/parser/devin.go`
- Create: `internal/parser/devin_subagent.go`

**Interfaces:**
- Consumes: `devinMessageNodeRow`, the parent `DevinSessionMeta`, and existing node-message parsing.
- Produces: ordered child trees with root node IDs, plus the set of node IDs excluded from the parent transcript.

- [x] Detect root system nodes with the observed Devin subagent marker and collect their descendants through `parent_node_id`.
- [x] Treat summarizer roots as auxiliary nodes excluded from the parent transcript, not as subagent sessions.
- [x] Parse each complete child tree with the existing Devin message-node decoder; derive title and timestamps from its messages.
- [x] Remove subagent and summarizer tree nodes from parent-message fallback/recovered-era results so no child message is duplicated in the parent transcript.
- [x] Assign child IDs as `devin:<parent-session-id>:agent-<root-node-id>` and set `ParentSessionID`, `RelSubagent`, and a parent-nested virtual source path.

### Task 2: Emit child sessions and reconcile source ownership

**Files:**
- Modify: `internal/parser/devin_provider.go`
- Modify: `internal/sync/engine.go`

**Interfaces:**
- Consumes: the complete parent parse and its transcript-backed child sessions.
- Produces: parent and child `ParseResultOutcome` values whose virtual paths are scoped under the parent source.

- [x] Return the parent and every parseable child tree in one complete `ParseOutcome`.
- [x] Keep the parent provider source as the authority for parsing all its child trees.
- [x] Resolve a stored child virtual path or child session ID back to the parent Devin source for reparsing.
- [x] Declare parent source ownership over its nested child virtual paths.
- [x] On a complete Devin force-replace, compare all rows owned by that parent source against emitted parent/child IDs so removed child trees enter normal source-missing reconciliation without touching other Devin sessions.

### Task 3: Version and document the parser change

**Files:**
- Modify: `internal/db/db.go`
- Modify: `docs/internal/session-format-sources.md`

- [x] Increment the parser data version because existing archived Devin sessions lack child session rows.
- [x] Record the observed message-node root marker, subtree relationship, parent-scoped identity, and the limit that Devin has no published CLI database schema.

### Task 4: Build and live archive verification

**Files:** no additional source files.

- [x] Run `go fmt ./...` and `go vet ./...`.
- [x] Build AgentsView.
- [x] Restart the already-authorized local server and allow its non-destructive parser-version resync to complete.
- [x] Confirm archive data version 118, 81 linked child sessions with messages, and that a child ID opens through the existing session API.
- [x] Confirm the parent still contains its `run_subagent`/`read_subagent` events and the child-session count comes from transcript-backed trees.
