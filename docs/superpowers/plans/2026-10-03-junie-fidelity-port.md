# Junie Parser Fidelity Port Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore the full-fidelity Junie session parser (tool calls, thinking, subagent child sessions, lifecycle/termination, sidecar metadata, usage dedup) on top of the current upstream discovery/index/fingerprint engine.

**Architecture:** The upstream Junie provider (`internal/parser/junie.go`, 449 lines) keeps only message-level parsing. We re-introduce the abandoned implementation's per-agent stream model inside the existing `junieParserState`, keeping every upstream improvement (index.jsonl cache, `os.Root` filesystem hardening, user-history events, strict cost parsing, `<!-- ANSWER -->` strip). Subagent streams emit synthesized `junie:<parent>--<agent>` child sessions via the existing multi-session plumbing (`ParseResult` slices, `VirtualSourcePath`, `RelSubagent`).

**Tech Stack:** Go 1.x, `gjson` for JSON access, testify for tests, `os.Root`-pinned file opens.

**Spec:** The abandoned implementation is the spec. It lives in the git object
store of this repo — read it with `git show 34cf5b44:internal/parser/junie.go`
(1502 lines) and `git show 34cf5b44:internal/parser/junie_provider.go` (115
lines). A checked-out copy may also exist at `/tmp/junie-mine/` (worktree at
commit `34cf5b44`). Test fixtures to adapt: `git show
34cf5b44:internal/parser/junie_test.go` (1018 lines) and
`git show 34cf5b44:internal/parser/junie_provider_test.go` (168 lines).
Author's issue notes: `git show
34cf5b44:.scratch/validate-indexing-ingesting-five-agents/issues/03-audit-junie-indexing-ingesting.md`
and `.../09-fix-junie-cancellation-and-subagent-linking.md`.

## Global Constraints

- Keep ALL current upstream behaviors — do not regress: `requestId` dedup +
  `UserMessagesCommittedToHistory`/`DroppedFromHistory`/`FailedInHistory`
  active-flag handling; `delivery:"Failed"` gating; `SessionTitleSetEvent`;
  `<!-- ANSWER -->` strip on `ResultBlockUpdatedEvent`; `SystemMessageEvent`
  `text\n\n details` merge; strict `money.ParseDollars` cost validation
  (negative/non-numeric cost must error); nonzero-usage-row filter; index.jsonl
  cache/diff/fingerprint machinery in `junie_provider.go`; `os.Root`-pinned
  opens + symlink rejection; recognized-stub exclusion vs unrecognized-rewrite
  preservation (`session()` empty-projection rules); `FirstMessage`
  newline-flatten/300-char truncate/sessionName fallback;
  `ExtractProjectFromCwdWithBranchContext` + `"junie"` project fallback + cwd
  shape validation; `SourceSessionID` population.
- Repo rules: read `docs/agents/testing.md` before touching tests;
  `docs/agents/storage.md` and `docs/agents/background-work.md` before the
  engine.go change. `go fmt ./... && go vet ./...` before committing.
  Conventional commit messages, no attribution footers.
- Commit on `main` directly (this repo's workflow commits each turn; do NOT
  create branches).
- Usage event naming stays CURRENT: `Source:"llm-response"`,
  `CostSource:"junie-model-usage"` — do not resurrect the old
  `"session"`/`"junie-reported"` names.
- Do NOT declare `Model` content capability (old declared it but never set
  `session.Model`).
- File layout: parser code stays in `internal/parser/junie.go`; the synthesized
  subagent finishing/linking code goes into a NEW file
  `internal/parser/junie_subagent.go` (matches `devin_subagent.go`,
  `antigravity_subagents.go` precedent); provider plumbing stays in
  `internal/parser/junie_provider.go`; new provider tests go in
  `internal/parser/junie_provider_test.go`.
- Reference old code by citing `old junie.go:LINE` (the 34cf5b44 copy); port
  adapted to the current `junieParserState`/`entries` model, not blindly.
- Verify each task with `go build ./... && go test ./internal/parser/
  -run 'Junie|junie' -count=1` plus the commands listed per task; run
  `go test ./internal/sync/ -run Junie -count=1` for the engine task.

## File Structure

| File | Responsibility |
|---|---|
| `internal/parser/junie.go` | Event consumption: stream routing, block upsert, message assembly, usage, termination, sidecar/meta reads, timestamp helpers |
| `internal/parser/junie_subagent.go` | NEW — `junieFinishSubagent`, `findJunieStream`, Task-call↔child-session linking, `junieBaseRawSessionID`/`junieSessionIDSourceFiles` |
| `internal/parser/junie_provider.go` | Source-set options (lookup, companions, descend), custom `Fingerprint` companion fold, `StoredSourceHintScopes`, capabilities |
| `internal/parser/junie_test.go` | Parser tests (extend existing) |
| `internal/parser/junie_provider_test.go` | NEW — provider-level tests (watch globs, companion mapping, raw-ID FindSource, fingerprint folds) |
| `internal/parser/types.go` | `AgentDef` Junie entry gains `Usage.NoPerMessageTokenData` |
| `internal/sync/engine.go` | Missing-members sweep arm for `AgentJunie` |
| `docs/internal/session-format-sources.md` | Junie section re-verified against ported behavior |
| `docs/changelog.md` | Changelog entry |

---

### Task 1: Per-agent stream routing + taskId-grouped assembly + thinking

Restructure `junieParserState` so every `SessionA2uxEvent` routes to a
per-`agent.id` stream holding last-wins `stepId` blocks; assemble one assistant
message per `taskId` carrying `ThinkingText`. Children are collected but NOT
emitted yet (Task 5). Tool calls not mapped yet (Task 2) — tool-ish blocks are
recorded but skipped by assembly.

**Files:**
- Modify: `internal/parser/junie.go` (state struct, `consumeA2UXEvent`, `session`, new `junieBlock`/`junieAgentStream`/assembly helpers)
- Test: `internal/parser/junie_test.go`

**Interfaces:**
- Produces (all package-private, used by Tasks 2–7):
  ```go
  type junieBlock struct { // last-wins snapshot of one agentEvent step
      kind, stepID, taskID, status, text, details, toolType, toolName,
          command, output, input, skillName, name, model, task, patch,
          title string
      files, changes, options []string
      seq  int
      ts   time.Time
      cancelled bool
  }
  type junieAgentStream struct { // one agent's view of the shared file
      id, name, kind, stepID, taskName, state, cwd, request string
      blocks   map[string]*junieBlock
      blockOrd []string
      usage    []ParsedUsageEvent
      usageSeen map[string]struct{}
      notes    []ParsedMessage
      requestTS, firstTS, lastTS time.Time
      cancelled, failed bool
  }
  func newJunieAgentStream(id string) *junieAgentStream
  func junieUpsertBlock(stream *junieAgentStream, ae gjson.Result, kind, taskID, eventState string, seq int, ts time.Time)
  func junieAssembleMessages(stream *junieAgentStream, extra []ParsedMessage) []ParsedMessage
  func junieAssistantMessage(blocks []*junieBlock, stream *junieAgentStream) (ParsedMessage, bool)
  ```

- [ ] **Step 1: Write the failing tests**

Append to `internal/parser/junie_test.go`. Fixture helpers follow the existing
file's style (`parseJunieProviderSession` discovers+ fingerprints+parses a
single session dir; extend it below to return all results in Task 5).

```go
func TestJunieThinkingAndTaskGrouping(t *testing.T) {
	root := t.TempDir()
	sessionID := "session-260101-120000-abcd"
	sessionDir := filepath.Join(root, sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Explain auth","timestampMs":1704067200000}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"AgentThoughtBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"step-think","text":"Reading auth"}},"timestampMs":1704067200100}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"state":"COMPLETED","agentEvent":{"kind":"AgentThoughtBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"step-think","text":"Read auth and traced login"}},"timestampMs":1704067200200}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"state":"COMPLETED","agentEvent":{"kind":"MarkdownBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"step-md","text":"Working on it"}},"timestampMs":1704067200300}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"state":"COMPLETED","agentEvent":{"kind":"ResultBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"step-res","result":"<!-- ANSWER -->Auth uses middleware."}},"timestampMs":1704067200400}` + "\n" +
			// A BuiltInAgent's block must NOT pollute the parent transcript.
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"MarkdownBlockUpdatedEvent","agent":{"kind":"BuiltInAgent","id":"agent-1","name":"auditor"},"stepId":"sub-1","text":"child working"}},"timestampMs":1704067200500}` + "\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))

	sess, messages := parseJunieProviderSession(t, path, "local")

	// One assistant message per taskId: thinking + markdown + result merged.
	var assistant []ParsedMessage
	for _, m := range messages {
		if m.Role == RoleAssistant {
			assistant = append(assistant, m)
		}
	}
	require.Len(t, assistant, 1)
	assert.True(t, assistant[0].HasThinking)
	assert.Equal(t, "Read auth and traced login", assistant[0].ThinkingText)
	assert.Equal(t, "Working on it\n\nAuth uses middleware.", assistant[0].Content)
	for _, m := range messages {
		assert.NotContains(t, m.Content, "child working")
	}
	assert.False(t, sess.IsTruncated)
}
```

Existing `TestParseJunieSession` will now fail: the two no-`taskId` result
blocks (`step-1` "Done", `step-2` "Finished") merge into ONE assistant message
(grouped by empty taskId, joined `"\n\n"`), and the `step-1` markdown "thinking
aloud" joins too. Update its expectations: the assistant message content
becomes `"thinking aloud\n\nDone\n\nFinished"`, total message count drops by
one, and `messages[3]`/`[6]` indices shift accordingly. Keep every other
assertion (user-history flags, usage row, counts).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/parser/ -run 'TestJunieThinkingAndTaskGrouping|TestParseJunieSession' -count=1`
Expected: FAIL — `ThinkingText` empty, assistant len mismatch, `"child working"` present in a parent message, merged-content assertion fails.

- [ ] **Step 3: Implement the stream model**

In `internal/parser/junie.go`:

1. Add the `junieBlock`, `junieAgentStream`, `newJunieAgentStream` types
   (verbatim from old `junie.go:52-109`, minus the `taskSeq` field — it was
   write-only dead state).
2. Extend `junieParserState`:

   ```go
   type junieParserState struct {
       entries          []junieTranscriptMessage
       userMessages     map[string]int
       streams          map[string]*junieAgentStream
       streamOrder      []string
       lastTaskState    string
       sourceSessionID  string
       startedAt        time.Time // min timestampMs seen
       endedAt          time.Time // max timestampMs seen
       sessionName      string
       sessionNameFound bool
       malformedLines   int
       lastLineBad      bool
   }
   ```

   Delete `assistantMessages` and the state-level `usageEvents` slice (usage
   moves onto streams in Task 4 — for now keep `usageEvents` on state until
   Task 4 to keep the diff smaller? **No** — `consumeModelUsage` needs a stream
   now because routing happens in Task 1. Give `junieAgentStream` its
   `usage`/`usageSeen` fields immediately and move `consumeModelUsage` onto the
   stream in this task, stamping `SessionID` at finish.)
3. Initialize in `parseJunieSessionWithSummary`'s state literal:
   `streams: map[string]*junieAgentStream{"main": newJunieAgentStream("main")}`.
   Keep `streamOrder` empty (main is not ordered; children append in Task 1
   already — they are just not emitted).
4. Timestamp tracking becomes min/max: in `consumeEvent`, replace the
   first/last assignments with `if s.startedAt.IsZero() || timestamp.Before(s.startedAt) { s.startedAt = timestamp }; if timestamp.After(s.endedAt) { s.endedAt = timestamp }`. Track `lastLineBad` in the parse loop: set `state.lastLineBad = true` on each invalid line, `false` after each valid line.
5. Rewrite `consumeA2UXEvent` as the routing dispatcher (adapted from old
   `junieHandleAgentEvent`, `junie.go:632-776`):

   ```go
   func (s *junieParserState) consumeA2UXEvent(
       event gjson.Result, timestamp time.Time, lineNumber int,
   ) error {
       ae := event.Get("event.agentEvent")
       kind := ae.Get("kind").Str
       agentID := ae.Get("agent.id").Str
       if agentID == "" {
           agentID = ae.Get("agentId").Str
       }
       if agentID == "" {
           if ae.Get("agent.kind").Str == "BuiltInAgent" {
               agentID = firstNonEmptyJSONLString(
                   ae.Get("agent.name").Str,
                   strings.TrimSuffix(strings.TrimSuffix(
                       ae.Get("stepId").Str, "-request"), "-spawned"),
                   "subagent",
               )
           } else {
               agentID = "main"
           }
       }
       stream := s.streams[agentID]
       if stream == nil {
           stream = newJunieAgentStream(agentID)
           s.streams[agentID] = stream
           if agentID != "main" {
               s.streamOrder = append(s.streamOrder, agentID)
           }
       }
       if stream.name == "" {
           stream.name = firstNonEmptyJSONLString(
               ae.Get("agent.name").Str, ae.Get("name").Str)
       }
       if stream.kind == "" {
           stream.kind = ae.Get("agent.kind").Str
       }
       if !timestamp.IsZero() {
           if stream.firstTS.IsZero() || timestamp.Before(stream.firstTS) {
               stream.firstTS = timestamp
           }
           if timestamp.After(stream.lastTS) {
               stream.lastTS = timestamp
           }
       }
       if agentID != "main" {
           if rawStepID := ae.Get("stepId").Str; rawStepID != "" {
               if strings.HasSuffix(rawStepID, "-request") ||
                   strings.HasSuffix(rawStepID, "-spawned") {
                   stream.stepID = strings.TrimSuffix(strings.TrimSuffix(
                       rawStepID, "-request"), "-spawned")
               } else if stream.stepID == "" {
                   stream.stepID = rawStepID
               }
           }
       }
       taskID := event.Get("taskId").Str
       eventState := event.Get("event.state").Str
       if eventState == "CANCELLED" || eventState == "CANCELED" {
           if agentID == "main" {
               s.lastTaskState = "CANCELLED"
           } else {
               stream.cancelled = true
               stream.state = "CANCELLED"
           }
       } else if eventState == "FAILED" || eventState == "ERROR" {
           if agentID == "main" {
               if s.lastTaskState == "" || s.lastTaskState == "COMPLETED" {
                   s.lastTaskState = "FAILED"
               }
           } else {
               stream.failed = true
               stream.state = "FAILED"
           }
       }

       switch kind {
       case "AgentThoughtBlockUpdatedEvent",
           "ResultBlockUpdatedEvent",
           "MarkdownBlockUpdatedEvent",
           "ToolBlockUpdatedEvent",
           "TerminalBlockUpdatedEvent",
           "ViewFilesBlockUpdatedEvent",
           "FileChangesBlockUpdatedEvent",
           "McpBlockUpdatedEvent",
           "AgentPatchCreatedEvent",
           "AskAsyncRequestUpdatedEvent",
           "ChoiceRequestUpdatedEvent":
           junieUpsertBlock(stream, ae, kind, taskID, eventState, lineNumber, timestamp)
       case "CustomAgentBlockUpdatedEvent",
           "SubagentSpawnedEvent":
           if agentID == "main" {
               junieUpsertBlock(stream, ae, kind, taskID, eventState, lineNumber, timestamp)
           }
       case "AgentTaskNameUpdatedEvent":
           name := strings.TrimSpace(firstNonEmptyJSONLString(
               ae.Get("name").Str, ae.Get("taskName").Str))
           if agentID == "main" {
               if name != "" {
                   s.sessionName = name
                   s.sessionNameFound = true
               }
           } else if stream.taskName == "" && name != "" {
               stream.taskName = name
           }
       case "LlmResponseMetadataEvent":
           return s.consumeModelUsage(stream, ae, taskID, timestamp, lineNumber)
       case "CurrentDirectoryUpdatedEvent":
           if dir := ae.Get("currentDirectory").Str; dir != "" && stream.cwd == "" {
               stream.cwd = dir
           }
       case "AgentFailureEvent", "AgentTaskFailedEvent", "CancelAgentEvent":
           // Task 3 wires termination state; notes recorded there.
       default:
           // Unknown/future kinds carry no transcript content.
       }
       return nil
   }
   ```

   (`AgentTaskNameUpdatedEvent` on main keeps current last-write-wins +
   `name|taskName` fields; on a child it feeds `stream.taskName` first-wins.
   This fixes the child-renames-parent bug.)
6. Port `junieUpsertBlock` verbatim from old `junie.go:781-861` (it references
   `minInt` — add `minInt`/`firstString` helpers at file bottom from old
   `junie.go:1490-1502`). IMPORTANT: keep the `<!-- ANSWER -->` strip —
   in `junieAssistantMessage`, when appending a `ResultBlockUpdatedEvent`
   block's text, use `strings.TrimSpace(strings.TrimPrefix(b.text, "<!-- ANSWER -->"))`. Simplest faithful port: apply the trim inside `junieUpsertBlock` when filling `text` for kind `ResultBlockUpdatedEvent`: `text = strings.TrimPrefix(text, "<!-- ANSWER -->")`.
7. Change `consumeModelUsage` signature to
   `(s *junieParserState) consumeModelUsage(stream *junieAgentStream, event gjson.Result, taskID string, timestamp time.Time, lineNumber int) error`. Keep the entire current body (ParseDollars validation, nonzero filter, `timeString` fallback, `DedupKey` line:index format for now — Task 4 replaces it) but append to `stream.usage` instead of `s.usageEvents`, and set `SessionID` at finish time. Store rows with `SessionID` left empty; add
   `func junieRewriteUsageSession(stream *junieAgentStream, fullID string) []ParsedUsageEvent` (old `junie.go:1150-1158`) that stamps `SessionID` AND prefixes `DedupKey` with `fullID+":"`.
   For THIS task's DedupKey keep the current format but make it stream-aware:
   `fmt.Sprintf("%s:llm-response:%d:%d", stream.id, lineNumber, index)` — the
   full `"junie:<id>:"` prefix is applied by `junieRewriteUsageSession`.
   `OccurredAt` fallback keeps `s.startedAt`.
8. Port assembly verbatim from old `junie.go:1162-1242`:
   `junieAssembleMessages(stream, extra)` groups `blockOrd` by `taskID`
   (first-seen order), builds one `junieAssistantMessage` per task, appends
   `stream.notes`, `sort.SliceStable` by `Timestamp`, assigns `Ordinal`.
   In `junieAssistantMessage`, keep the switch but make the `default` arm a
   no-op for now (tool calls arrive in Task 2): `default: _ = b`.
9. Rewrite `session()`:

   ```go
   main := s.streams["main"] // always present
   var extra []ParsedMessage
   for _, entry := range s.entries {
       if !entry.active || strings.TrimSpace(entry.Content) == "" {
           continue
       }
       extra = append(extra, entry.ParsedMessage)
   }
   messages := junieAssembleMessages(main, extra)
   userCount := 0
   firstMessage := ""
   for _, m := range messages {
       if m.Role == RoleUser && !m.IsSystem {
           userCount++
           if firstMessage == "" {
               firstMessage = truncate(strings.ReplaceAll(m.Content, "\n", " "), 300)
           }
       }
   }
   hasActivity := len(main.blocks) > 0 || len(main.usage) > 0
   // recognized empty projections stay clearable; unrecognized empties preserve
   if len(messages) == 0 && len(s.entries) == 0 && !hasActivity && !s.sessionNameFound {
       return nil, nil, false, nil
   }
   if userCount == 0 && !hasActivity && !s.sessionNameFound && !summaryPresent {
       return nil, nil, true, nil
   }
   // summary merge, project/cwd, sessionName — keep current lines 374-400
   // cwd gains a stream.cwd fallback:
   projectDir := strings.TrimSpace(summary.projectDir)
   cwd := projectDir
   if cwd == "" {
       cwd = main.cwd // CurrentDirectoryUpdatedEvent
   }
   // then keep the existing filepath.IsAbs/looksLikeWindowsPath validation
   ```

   `UsageEvents: junieRewriteUsageSession(main, "junie:"+sourceSessionID)` and
   `applyUsageEventTokenTotals(session, session.UsageEvents)` — replace the
   `s.usageEvents` references. Set `IsTruncated: s.lastLineBad` on the session.
10. Update `session()`'s signature unchanged; `parseJunieSessionWithSummary`
    keeps returning `(*ParsedSession, []ParsedMessage, bool, error)` — children
    are not emitted until Task 5.

- [ ] **Step 4: Run tests**

Run: `go build ./... && go test ./internal/parser/ -run 'Junie|junie' -count=1`
Expected: new test PASS; `TestParseJunieSession` PASS after its expectation
update; all other junie tests PASS (discovery/stub/index/symlink tests are
unaffected). Also run `go test ./internal/parser/ -count=1` to catch
capability/registry cross-tests.

- [ ] **Step 5: Commit**

```bash
git add internal/parser/junie.go internal/parser/junie_test.go
git commit -m "feat(parser): route Junie a2ux events into per-agent streams with taskId-grouped assistant messages and thinking"
```

---

### Task 2: Tool-call extraction from tool-ish blocks

Map every tool-ish `*UpdatedEvent` block kind to `ParsedToolCall` inside the
per-task assistant message.

**Files:**
- Modify: `internal/parser/junie.go` (`junieAssistantMessage` default arm, new `junieToolCall`, `junieResultStatus`)
- Test: `internal/parser/junie_test.go`

**Interfaces:**
- Consumes: `junieBlock` fields + `junieAssistantMessage` from Task 1.
- Produces:
  ```go
  func junieToolCall(b *junieBlock) (ParsedToolCall, bool)
  func junieResultStatus(status string) string // COMPLETED/FINISHED→completed, FAILED/ERROR→error, CANCELLED/CANCELED→cancelled, IN_PROGRESS/STARTED→running, else strings.ToLower
  ```

- [ ] **Step 1: Write the failing test**

```go
func TestJunieToolCalls(t *testing.T) {
	root := t.TempDir()
	sessionID := "session-260101-120000-abcd"
	sessionDir := filepath.Join(root, sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Do the work","timestampMs":1704067200000}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"state":"COMPLETED","agentEvent":{"kind":"ViewFilesBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"sv","status":"COMPLETED","files":[{"relativePath":"src/auth.go"}],"details":"opened"}},"timestampMs":1704067200100}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"state":"COMPLETED","agentEvent":{"kind":"TerminalBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"st","status":"COMPLETED","command":"go test ./auth","output":"ok auth"}},"timestampMs":1704067200200}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"McpBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"sm","toolName":"ijproxy/search_text","input":"{\"q\":\"login\"}","status":"COMPLETED","details":"4 hits"}},"timestampMs":1704067200300}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"ToolBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"sg","text":"Search login","status":"COMPLETED","toolType":"Search"}},"timestampMs":1704067200400}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"ToolBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"ss","text":"Run lint","status":"COMPLETED","toolType":"Skill","skillName":"golangci-lint"}},"timestampMs":1704067200500}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"FileChangesBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"sf","status":"COMPLETED","changes":[{"afterRelativePath":"src/auth.go"}]}},"timestampMs":1704067200600}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"AgentPatchCreatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"sp","patch":"diff --git a/x b/x"}},"timestampMs":1704067200700}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"ChoiceRequestUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"sq","title":"Pick one","request":{"options":[{"title":"a"},{"title":"b"}]}}},"timestampMs":1704067200800}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"TerminalBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"srun","status":"IN_PROGRESS","command":"sleep 60"}},"timestampMs":1704067200900}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"ResultBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"sr","result":"Done."}},"timestampMs":1704067201000}` + "\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))

	_, messages := parseJunieProviderSession(t, path, "local")

	var assistant ParsedMessage
	for _, m := range messages {
		if m.Role == RoleAssistant {
			assistant = m
		}
	}
	assert.True(t, assistant.HasToolUse)
	require.Len(t, assistant.ToolCalls, 9)

	tc := assistant.ToolCalls
	assert.Equal(t, "ViewFiles", tc[0].ToolName)
	assert.Equal(t, "Read", tc[0].Category)
	assert.Equal(t, "src/auth.go", tc[0].FilePath)
	assert.Equal(t, "task-1:sv", tc[0].ToolUseID)

	assert.Equal(t, "Terminal", tc[1].ToolName)
	assert.Equal(t, "Bash", tc[1].Category)
	assert.Contains(t, tc[1].InputJSON, "go test ./auth")
	require.Len(t, tc[1].ResultEvents, 1)
	assert.Equal(t, "completed", tc[1].ResultEvents[0].Status)
	assert.Equal(t, "ok auth", tc[1].ResultEvents[0].Content)

	assert.Equal(t, "ijproxy/search_text", tc[2].ToolName)
	assert.Equal(t, "Tool", tc[2].Category)
	assert.Equal(t, "4 hits", tc[2].ResultEvents[0].Content)

	assert.Equal(t, "Search", tc[3].ToolName)
	assert.Equal(t, "Grep", tc[3].Category)

	assert.Equal(t, "Skill", tc[4].ToolName)
	assert.Equal(t, "Tool", tc[4].Category)
	assert.Equal(t, "golangci-lint", tc[4].SkillName)

	assert.Equal(t, "FileChanges", tc[5].ToolName)
	assert.Equal(t, "Edit", tc[5].Category)
	assert.Equal(t, "src/auth.go", tc[5].FilePath)

	assert.Equal(t, "Patch", tc[6].ToolName)
	assert.Equal(t, "Edit", tc[6].Category)
	assert.Equal(t, "completed", tc[6].ResultEvents[0].Status)

	assert.Equal(t, "AskUserQuestion", tc[7].ToolName)
	assert.Equal(t, "Tool", tc[7].Category)
	assert.Contains(t, tc[7].InputJSON, "Pick one")

	// An IN_PROGRESS call stays "running" — feeds tool_call_pending later.
	assert.Equal(t, "Terminal", tc[8].ToolName)
	assert.Equal(t, "running", tc[8].ResultEvents[0].Status)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/parser/ -run TestJunieToolCalls -count=1`
Expected: FAIL — `HasToolUse` false, `ToolCalls` empty.

- [ ] **Step 3: Implement `junieToolCall` + `junieResultStatus`**

Port verbatim from old `junie.go:1248-1391` (`junieToolCall` + `junieResultStatus`). Requires `encoding/json` import. In `junieAssistantMessage`, change the `default:` arm to:

```go
default:
    if call, ok := junieToolCall(b); ok {
        calls = append(calls, call)
    }
```

(The full mapping table is old `junie.go:1281-1373`: Terminal→`Terminal`/`Bash`,
ViewFiles→`ViewFiles`/`Read` w/ `FilePath`, FileChanges→`FileChanges`/`Edit`,
Mcp→`toolName`/`Tool` raw `input`, ToolBlock→`toolType` Search→`Grep`,
Skill→`Tool` w/ `skillName`, else `Other`, CustomAgent/SubagentSpawned→`Task`,
AgentPatchCreated→`Patch`/`Edit`, AskAsync/ChoiceRequest→`AskUserQuestion`/`Tool`.
`ToolUseID = b.taskID + ":" + b.stepID`. Statuses via `junieResultStatus` +
`cancelled` override; instantaneous kinds (spawn/patch/ask) get `completed`
when no status.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/parser/ -run 'Junie|junie' -count=1`
Expected: PASS, including Task 1's tests.

- [ ] **Step 5: Commit**

```bash
git add internal/parser/junie.go internal/parser/junie_test.go
git commit -m "feat(parser): extract Junie tool calls from block-update events"
```

---

### Task 3: Lifecycle events + TerminationStatus + IsTruncated

Route `CancelAgentEvent`/`AgentTaskFailedEvent`/`AgentFailureEvent` (top-level
and inside `SessionA2uxEvent`) and `TaskState` to stream state + system notes
with real reason text; classify `TerminationStatus`; set `IsTruncated`.

**Files:**
- Modify: `internal/parser/junie.go` (`consumeEvent`, `consumeA2UXEvent` failure arm, `session()`)
- Modify: `internal/parser/junie_provider.go` (capabilities)
- Create: `internal/parser/junie_subagent.go` (only `findJunieStream` for now — used by cancel routing)
- Test: `internal/parser/junie_test.go`

**Interfaces:**
- Produces:
  ```go
  // in junie_subagent.go
  func findJunieStream(streams map[string]*junieAgentStream, targetAgent, stepID string) *junieAgentStream
  ```

- [ ] **Step 1: Write the failing tests**

```go
func TestJunieCancelAndFailedEvents(t *testing.T) {
	for _, tt := range []struct {
		kind, body, want string
	}{
		{"CancelAgentEvent", `"message":"Interrupted by user"`, "Interrupted by user"},
		{"AgentTaskFailedEvent", `"reason":"LLM rate limit"`, "LLM rate limit"},
		{"AgentFailureEvent", `"errorCode":"AUTH"`, "AUTH"},
	} {
		t.Run(tt.kind, func(t *testing.T) {
			root := t.TempDir()
			sessionDir := filepath.Join(root, "session-260101-120000-abcd")
			require.NoError(t, os.MkdirAll(sessionDir, 0o755))
			events := []byte(
				`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Hi","timestampMs":1704067200000}` + "\n" +
					`{"kind":"` + tt.kind + `",` + tt.body + `,"timestampMs":1704067200100}` + "\n",
			)
			path := filepath.Join(sessionDir, "events.jsonl")
			require.NoError(t, os.WriteFile(path, events, 0o600))

			sess, messages := parseJunieProviderSession(t, path, "local")

			var note *ParsedMessage
			for i := range messages {
				if messages[i].IsSystem && messages[i].SourceSubtype == tt.kind {
					note = &messages[i]
				}
			}
			require.NotNil(t, note)
			assert.Equal(t, tt.want, note.Content)
			assert.Equal(t, TerminationTruncated, sess.TerminationStatus)
		})
	}
}

func TestJunieTaskStateCompletedClassifiesAwaitingUser(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-260101-120000-abcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Hi","timestampMs":1704067200000}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"t1","event":{"agentEvent":{"kind":"ResultBlockUpdatedEvent","stepId":"s1","result":"ok"}},"timestampMs":1704067200100}` + "\n" +
			`{"kind":"TaskState","state":"COMPLETED","timestampMs":1704067200200}` + "\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))

	sess, _ := parseJunieProviderSession(t, path, "local")
	assert.Equal(t, TerminationAwaitingUser, sess.TerminationStatus)
}

func TestJunieCancelThenCompletedStaysTruncated(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-260101-120000-abcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Hi","timestampMs":1704067200000}` + "\n" +
			`{"kind":"CancelAgentEvent","message":"stop","timestampMs":1704067200100}` + "\n" +
			`{"kind":"TaskState","state":"COMPLETED","timestampMs":1704067200200}` + "\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))

	sess, _ := parseJunieProviderSession(t, path, "local")
	assert.Equal(t, TerminationTruncated, sess.TerminationStatus)
}

func TestJunieTruncatedFileMarksSession(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-260101-120000-abcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Hi","timestampMs":1704067200000}` + "\n" +
			"{\"kind\":\"UserPromptEvent\",\"prompt\":\"cut" + "\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))

	sess, _ := parseJunieProviderSession(t, path, "local")
	assert.True(t, sess.IsTruncated)
	assert.Equal(t, 1, sess.MalformedLines)
	assert.Equal(t, TerminationTruncated, sess.TerminationStatus)
}

func TestJunieCancelledResultBlockExcluded(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-260101-120000-abcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Hi","timestampMs":1704067200000}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"t1","event":{"agentEvent":{"kind":"ResultBlockUpdatedEvent","stepId":"s1","status":"CANCELLED","result":"partial draft"}},"timestampMs":1704067200100}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"t1","event":{"agentEvent":{"kind":"MarkdownBlockUpdatedEvent","stepId":"s2","text":"real answer"}},"timestampMs":1704067200200}` + "\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))

	_, messages := parseJunieProviderSession(t, path, "local")
	for _, m := range messages {
		assert.NotContains(t, m.Content, "partial draft")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/parser/ -run 'TestJunieCancel|TestJunieTaskState|TestJunieTruncated|TestJunieCancelledResult' -count=1`
Expected: FAIL — notes absent, `TerminationStatus`/`IsTruncated` empty, cancelled text present.

- [ ] **Step 3: Implement**

1. Create `internal/parser/junie_subagent.go` with `package parser` header and
   `findJunieStream` ported verbatim from old `junie.go:113-160`.
2. In `consumeEvent` (junie.go), handle the wrapper `event.state` inside
   `SessionA2uxEvent` — already done in Task 1 — plus add top-level cases:

   ```go
   case "CancelAgentEvent":
       s.consumeAgentLifecycleEvent(event, timestamp, "CancelAgentEvent")
   case "AgentTaskFailedEvent", "AgentFailureEvent":
       s.consumeAgentLifecycleEvent(event, timestamp, event.Get("kind").Str)
   case "TaskState":
       st := event.Get("state").Str
       if s.lastTaskState != "CANCELLED" && s.lastTaskState != "CANCELED" && s.lastTaskState != "FAILED" {
           s.lastTaskState = st
       } else if st == "CANCELLED" || st == "CANCELED" || st == "FAILED" {
           s.lastTaskState = st
       }
   ```

   Remove the old `case "AgentTaskFailedEvent": s.appendMessage(RoleSystem, "Agent task failed", timestamp)` arm.
   Update `TestParseJunieSession`'s expectation: the fixture's bare
   `AgentTaskFailedEvent` now emits content `"Task failed"` (fallback text)
   with `SourceSubtype:"AgentTaskFailedEvent"` — change the asserted content
   from `"Agent task failed"` to `"Task failed"`.
3. New helper (port of old `junie.go:293-369`, adapted):

   ```go
   func (s *junieParserState) consumeAgentLifecycleEvent(
       event gjson.Result, timestamp time.Time, kind string,
   ) {
       targetAgent := firstNonEmptyJSONLString(
           event.Get("agent.id").Str, event.Get("agentId").Str,
           event.Get("targetAgentId").Str, event.Get("agent.name").Str,
           event.Get("agentName").Str)
       stepID := event.Get("stepId").Str
       fallback := "Task failed"
       if kind == "CancelAgentEvent" {
           fallback = "Task cancelled"
       }
       text := firstNonEmptyJSONLString(
           event.Get("message").Str, event.Get("reason").Str,
           event.Get("errorCode").Str, fallback)
       msg := junieTranscriptMessage{ParsedMessage: ParsedMessage{
           Role: RoleSystem, IsSystem: true, Content: text,
           ContentLength: len(text), Timestamp: timestamp,
           SourceSubtype: kind}, active: strings.TrimSpace(text) != ""}
       st := findJunieStream(s.streams, targetAgent, stepID)
       switch {
       case st != nil && st.id != "main":
           if kind == "CancelAgentEvent" { st.cancelled = true; st.state = "CANCELLED" } else { st.failed = true; st.state = "FAILED" }
           st.notes = append(st.notes, msg.ParsedMessage)
       case targetAgent != "" && targetAgent != "main":
           st = newJunieAgentStream(targetAgent)
           if kind == "CancelAgentEvent" { st.cancelled = true; st.state = "CANCELLED" } else { st.failed = true; st.state = "FAILED" }
           s.streams[targetAgent] = st
           s.streamOrder = append(s.streamOrder, targetAgent)
           st.notes = append(st.notes, msg.ParsedMessage)
       default:
           if kind == "CancelAgentEvent" {
               s.lastTaskState = "CANCELLED"
           } else {
               s.lastTaskState = "FAILED"
           }
           s.entries = append(s.entries, msg)
       }
   }
   ```

   IMPORTANT — terminal-state protection: the `default` arm must not let a
   later `COMPLETED` overwrite — the `TaskState` case above already guards;
   `lastTaskState` assignments here are direct (CANCELLED/FAILED are terminal).
4. In `consumeA2UXEvent`, fill the deferred failure arm (old `junie.go:733-768`):

   ```go
   case "AgentFailureEvent", "AgentTaskFailedEvent", "CancelAgentEvent":
       fallback := "Task failed"
       if kind == "CancelAgentEvent" { fallback = "Task cancelled" }
       text := firstNonEmptyJSONLString(
           ae.Get("message").Str, ae.Get("reason").Str,
           ae.Get("errorCode").Str, fallback)
       stream.notes = append(stream.notes, ParsedMessage{
           Role: RoleSystem, IsSystem: true, Content: text,
           ContentLength: len(text), Timestamp: timestamp,
           SourceSubtype: kind})
       if kind == "CancelAgentEvent" {
           if agentID == "main" { s.lastTaskState = "CANCELLED" } else { stream.cancelled = true; stream.state = "CANCELLED" }
       } else {
           if agentID == "main" {
               if s.lastTaskState == "" || s.lastTaskState == "COMPLETED" { s.lastTaskState = "FAILED" }
           } else { stream.failed = true; stream.state = "FAILED" }
       }
   ```

5. `session()` termination block, before constructing `ParsedSession` (adapted
   from old `junie.go:964-1005`):

   ```go
   stopReason := ""
   switch {
   case s.lastTaskState == "COMPLETED":
       stopReason = "task_complete"
   case summary.lifecycle == "FINISHED" || summary.lifecycle == "COMPLETED":
       stopReason = "task_complete"
   }
   ```
   (`summary.lifecycle` does not exist until Task 6 — guard with the
   `s.lastTaskState` arm only for now, or add the field empty now and fill it
   in Task 6. Add `lifecycle string` to `junieSessionSummary` now, leave it
   unpopulated.)
   On the session:
   ```go
   sess.IsTruncated = s.lastLineBad
   isCancelledOrFailed := s.lastTaskState == "CANCELLED" || s.lastTaskState == "CANCELED" || s.lastTaskState == "FAILED" ||
       summary.lifecycle == "CANCELLED" || summary.lifecycle == "CANCELED" || summary.lifecycle == "FAILED" ||
       main.cancelled || main.failed || main.state == "CANCELLED" || main.state == "CANCELED" || main.state == "FAILED"
   if isCancelledOrFailed {
       sess.TerminationStatus = TerminationTruncated
   } else {
       sess.TerminationStatus = Classify(messages, stopReason, s.lastLineBad)
   }
   ```
6. `SystemMessageEvent`: add `SourceSubtype: event.Get("level").Str` on the
   system entry in `consumeSystemMessage`.
7. Cancelled block content exclusion is already in `junieAssistantMessage`
   (`!b.cancelled` guard on Result/Markdown) — Task 1's verbatim port covers
   it; the test proves it.
8. In `junieProviderCapabilities()` add `source.MultiSessionSource` NOT yet
   (Task 5) — here add only:
   `Content.TerminationStatus = CapabilitySupported`,
   `Content.TruncationStatus = CapabilitySupported`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/parser/ -run 'Junie|junie' -count=1`
Expected: PASS all.

- [ ] **Step 5: Commit**

```bash
git add internal/parser/junie.go internal/parser/junie_subagent.go internal/parser/junie_provider.go internal/parser/junie_test.go
git commit -m "feat(parser): classify Junie termination from cancel, failure, and task-state events"
```

---

### Task 4: Usage content-hash dedup

Junie re-emits identical `modelUsage` rows; dedup by normalized content hash
scoped per task+stream. Also stamps per-stream `SessionID` (children land in
Task 5; the rewrite already supports them).

**Files:**
- Modify: `internal/parser/junie.go` (`consumeModelUsage`, `junieRewriteUsageSession`)
- Test: `internal/parser/junie_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestJunieUsageReemissionDedup(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-260101-120000-abcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	row := `{"model":"gpt-4.1","cost":0.002,"inputTokens":100,"cacheInputTokens":10,"cacheCreateTokens":5,"outputTokens":12,"time":0}`
	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Hi","timestampMs":1704067200000}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"t1","event":{"agentEvent":{"kind":"LlmResponseMetadataEvent","agent":{"kind":"MainAgent","id":"main"},"modelUsage":[`+row+`]}},"timestampMs":1704067200100}` + "\n" +
			// byte-identical re-emission on a later line -> must dedup
			`{"kind":"SessionA2uxEvent","taskId":"t1","event":{"agentEvent":{"kind":"LlmResponseMetadataEvent","agent":{"kind":"MainAgent","id":"main"},"modelUsage":[`+row+`]}},"timestampMs":1704067200200}` + "\n" +
			// a DIFFERENT call: same model, different tokens -> kept
			`{"kind":"SessionA2uxEvent","taskId":"t1","event":{"agentEvent":{"kind":"LlmResponseMetadataEvent","agent":{"kind":"MainAgent","id":"main"},"modelUsage":[{"model":"gpt-4.1","cost":0.001,"inputTokens":50,"outputTokens":3,"time":0}]}},"timestampMs":1704067200300}` + "\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))

	sess, _ := parseJunieProviderSession(t, path, "local")
	require.Len(t, sess.UsageEvents, 2)
	assert.Equal(t, 100, sess.UsageEvents[0].InputTokens)
	assert.Equal(t, 50, sess.UsageEvents[1].InputTokens)
	assert.NotEqual(t, sess.UsageEvents[0].DedupKey, sess.UsageEvents[1].DedupKey)
	assert.True(t, sess.HasTotalOutputTokens)
	assert.Equal(t, 15, sess.TotalOutputTokens) // 12+3, not 12+12+3
}
```

Also update `TestParseJunieSession`'s DedupKey assertion: replace
`assert.Equal(t, "junie:"+sessionID+":llm-response:12:0", usage.DedupKey)`
with `assert.True(t, strings.HasPrefix(usage.DedupKey, "junie:"+sessionID+":llm-response:"))`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/parser/ -run 'TestJunieUsageReemissionDedup|TestParseJunieSession' -count=1`
Expected: FAIL — 3 usage events (double count), old DedupKey format mismatch.

- [ ] **Step 3: Implement content-hash dedup**

In `consumeModelUsage`, before building `parsed` (adapted from old
`junie.go:866-887`, keeping current field mapping/validation/filter):

```go
sum := sha256.Sum256([]byte(strings.Join([]string{
    strings.TrimSpace(usage.Get("model").Str),
    usage.Get("cost").Raw,
    usage.Get("inputTokens").Raw,
    usage.Get("cacheInputTokens").Raw,
    usage.Get("cacheCreateTokens").Raw,
    usage.Get("outputTokens").Raw,
    usage.Get("time").Raw,
}, "|")))
dedup := fmt.Sprintf("%s:%s:%x", taskID, stream.id, sum[:8])
if _, dup := stream.usageSeen[dedup]; dup {
    continue
}
stream.usageSeen[dedup] = struct{}{}
```

Set `parsed.DedupKey = dedup` (the `junie:<id>:` prefix is applied by
`junieRewriteUsageSession`: `out[i].DedupKey = fullID + ":" + out[i].DedupKey`
— update that function to prefix both `SessionID` and `DedupKey`; the stored
DedupKey is then stable across reparses AND position-independent). Add
`"crypto/sha256"` import (already imported? add if missing).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/parser/ -run 'Junie|junie' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/parser/junie.go internal/parser/junie_test.go
git commit -m "fix(parser): dedup Junie re-emitted model usage rows by content hash"
```

---

### Task 5: Subagent child sessions + Task-call linking + provider/engine wiring

Emit one synthesized `junie:<raw>--<agent>` `ParseResult` per non-main stream;
link parent `Task` calls to children; declare multi-session capabilities;
wire the engine's missing-members sweep.

**Files:**
- Modify: `internal/parser/junie.go` (emit children in `parseJunieSessionWithSummary` → change return shape; `session()` split into parent build)
- Modify: `internal/parser/junie_subagent.go` (`junieFinishSubagent`, `junieLinkSubagentCalls`, `junieBaseRawSessionID`, `junieSessionIDSourceFiles`)
- Modify: `internal/parser/junie_provider.go` (options, `StoredSourceHintScopes`, capabilities, `FindSource` passthrough is already there)
- Modify: `internal/sync/engine.go:12709-12715` (add Junie to the WithPreserved arm)
- Create: `internal/parser/junie_provider_test.go`
- Test: `internal/parser/junie_test.go`, `internal/sync/junie_integration_test.go`

**Interfaces:**
- Produces:
  ```go
  // junie_subagent.go
  func junieFinishSubagent(fullID, parentFullID, path string, info os.FileInfo,
      stream *junieAgentStream, summary junieSessionSummary, summaryPresent bool,
      machine, hash string) (*ParsedSession, []ParsedMessage, bool)
  func junieLinkSubagentCalls(results []ParseResult, streams map[string]*junieAgentStream, emitted map[string]bool, rawID string)
  func junieBaseRawSessionID(rawID string) string
  func junieSessionIDSourceFiles(roots []string, rawID string) []string
  // junie_provider.go
  func (s junieSourceSet) StoredSourceHintScopes(req ChangedPathRequest) []StoredSourceHintScope
  ```
- parseJunieSessionWithSummary return type changes:
  `(*ParsedSession, []ParsedMessage, bool, error)` →
  `([]ParseResult, []string /*excluded*/, error)` — update
  `indexCache.parseFile` accordingly (it already returns
  `([]ParseResult, []string, error)`).

- [ ] **Step 1: Write the failing tests**

Extend `parseJunieProviderSession` with a sibling helper returning the full
outcome (keep the old one for existing tests):

```go
func parseJunieProviderOutcome(
	t *testing.T, path, machine string,
) ParseOutcome {
	t.Helper()
	root := filepath.Dir(filepath.Dir(path))
	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	fingerprint, err := provider.Fingerprint(t.Context(), sources[0])
	require.NoError(t, err)
	outcome, err := provider.Parse(t.Context(), ParseRequest{
		Source: sources[0], Fingerprint: fingerprint, Machine: machine,
	})
	require.NoError(t, err)
	return outcome
}
```

```go
func junieSubagentFixture() string {
	return `{"kind":"UserPromptEvent","requestId":"r1","prompt":"Audit auth","timestampMs":1704067200000}` + "\n" +
		`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"SubagentSpawnedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"sub-step-1-spawned","name":"auth-auditor","task":"Audit the auth module for leaks"}},"timestampMs":1704067200100}` + "\n" +
		`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"MarkdownBlockUpdatedEvent","agent":{"kind":"BuiltInAgent","id":"agent-1","name":"auth-auditor"},"stepId":"sub-step-1-request","text":"Audit the auth module for leaks"}},"timestampMs":1704067200110}` + "\n" +
		`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"AgentThoughtBlockUpdatedEvent","agent":{"kind":"BuiltInAgent","id":"agent-1","name":"auth-auditor"},"stepId":"sub-think-1","text":"Scanning token handling"}},"timestampMs":1704067200120}` + "\n" +
		`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"ResultBlockUpdatedEvent","agent":{"kind":"BuiltInAgent","id":"agent-1","name":"auth-auditor"},"stepId":"sub-res-1","result":"No leaks found."}},"timestampMs":1704067200130}` + "\n" +
		`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"LlmResponseMetadataEvent","agent":{"kind":"BuiltInAgent","id":"agent-1"},"modelUsage":[{"model":"gpt-4.1-mini","cost":0.0003,"inputTokens":710,"outputTokens":13,"time":0}]}},"timestampMs":1704067200140}` + "\n" +
		`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"ResultBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"res-1","result":"Audited: clean."}},"timestampMs":1704067200200}` + "\n"
}

func TestJunieSubagentSession(t *testing.T) {
	root := t.TempDir()
	sessionID := "session-260101-120000-abcd"
	sessionDir := filepath.Join(root, sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(junieSubagentFixture()), 0o600))

	outcome := parseJunieProviderOutcome(t, path, "local")
	require.Len(t, outcome.Results, 2)

	parent := outcome.Results[0].Result
	sub := outcome.Results[1].Result.Session
	subMsgs := outcome.Results[1].Result.Messages

	assert.Equal(t, "junie:"+sessionID, parent.Session.ID)
	assert.Equal(t, "junie:"+sessionID+"--agent-1", sub.ID)
	assert.Equal(t, "junie:"+sessionID, sub.ParentSessionID)
	assert.Equal(t, RelSubagent, sub.RelationshipType)
	assert.Equal(t, "auth-auditor", sub.SessionName)
	assert.Equal(t, VirtualSourcePath(path, "agent-1"), sub.File.Path)

	require.Len(t, subMsgs, 2)
	assert.Equal(t, RoleUser, subMsgs[0].Role)
	assert.Equal(t, "Audit the auth module for leaks", subMsgs[0].Content)
	assert.Equal(t, RoleAssistant, subMsgs[1].Role)
	assert.Equal(t, "Scanning token handling", subMsgs[1].ThinkingText)
	assert.Equal(t, "No leaks found.", subMsgs[1].Content)

	require.Len(t, sub.UsageEvents, 1)
	assert.Equal(t, "junie:"+sessionID+"--agent-1", sub.UsageEvents[0].SessionID)

	// Parent Task call links to the child session.
	var taskCall *ParsedToolCall
	for i := range parent.Messages {
		for j := range parent.Messages[i].ToolCalls {
			if parent.Messages[i].ToolCalls[j].Category == "Task" {
				taskCall = &parent.Messages[i].ToolCalls[j]
			}
		}
	}
	require.NotNil(t, taskCall)
	assert.Equal(t, "auth-auditor", taskCall.ToolName)
	assert.Equal(t, sub.ID, taskCall.SubagentSessionID)

	// Child content stays out of the parent transcript.
	for _, m := range parent.Messages {
		assert.NotContains(t, m.Content, "No leaks found")
		assert.NotContains(t, m.ThinkingText, "Scanning token handling")
	}
}
```

```go
func TestJunieProviderFindSourceSubagent(t *testing.T) {
	root := t.TempDir()
	sessionID := "session-260101-120000-abcd"
	sessionDir := filepath.Join(root, sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(junieSubagentFixture()), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	source, found, err := provider.FindSource(t.Context(), FindSourceRequest{
		RawSessionID: sessionID + "--agent-1",
	})
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, path, source.DisplayPath)
}
```

```go
func TestJunieCapabilitiesDeclareSubagents(t *testing.T) {
	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{t.TempDir()}})
	require.True(t, ok)
	caps := provider.Capabilities()
	assert.Equal(t, CapabilitySupported, caps.Source.MultiSessionSource)
	assert.Equal(t, CapabilitySupported, caps.Source.ExcludedSessions)
	assert.Equal(t, CapabilitySupported, caps.Content.Subagents)
	assert.Equal(t, CapabilitySupported, caps.Content.Relationships)
	assert.Equal(t, CapabilitySupported, caps.Content.ToolCalls)
	assert.Equal(t, CapabilitySupported, caps.Content.Thinking)
}
```

Sync-level test (append to `internal/sync/junie_integration_test.go` —
follow that file's existing engine-fixture style): a session whose subagent
blocks are REMOVED in a rewrite tombstones the stored child row when the
parent file is reparsed. If the file's harness makes this awkward, the
missing-member sweep is still covered indirectly; write it as:

```go
func TestSyncJunieVanishedSubagentTombstones(t *testing.T) {
	// 1. sync a session containing a BuiltInAgent stream -> child row stored
	// 2. rewrite events.jsonl without the child blocks
	// 3. sync again -> child row marked source_missing (or deleted per policy)
	// Assert via the same helpers the other tests in this file use.
}
```

(Use the existing integration test's engine setup helpers; assert the
`source_missing_at` tombstone on `junie:<id>--agent-1`.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/parser/ -run 'TestJunieSubagent|TestJunieProviderFindSourceSubagent|TestJunieCapabilities' -count=1`
Expected: FAIL — 1 result, no child session, capabilities not supported.

- [ ] **Step 3: Implement**

1. In `junie.go`, change `parseJunieSessionWithSummary` signature to return
   `([]ParseResult, []string, error)`. Restructure its tail:

   ```go
   return state.results(ctx, path, machine, rawID, info, summary, summaryPresent, reqFingerprintHash)
   ```

   Move the `session()` body into `state.parentResult(...)` returning
   `(*ParsedSession, []ParsedMessage, isStub bool)` (same logic as Task 3
   plus `File.Hash` stamping inside — keep the current `parseFile` hash/size/
   mtime override where it is in `indexCache.parseFile`). New `results`
   method (replaces `session()`'s caller):

   ```go
   func (s *junieParserState) results(
       ctx context.Context, path, machine, rawID string, info os.FileInfo,
       summary junieSessionSummary, summaryPresent bool,
   ) ([]ParseResult, []string, error) {
       fullID := "junie:" + rawID
       sess, msgs, isStub := s.parentResult(ctx, path, machine, rawID, info, summary, summaryPresent)
       var results []ParseResult
       var excluded []string
       if sess == nil {
           if isStub {
               excluded = append(excluded, fullID)
           }
       } else {
           results = append(results, ParseResult{
               Session: *sess, Messages: msgs, UsageEvents: sess.UsageEvents})
       }
       emitted := map[string]bool{}
       for _, agentID := range s.streamOrder {
           stream := s.streams[agentID]
           sub, subMsgs, ok := junieFinishSubagent(
               fullID+"--"+agentID, fullID, path, info,
               stream, summary, summaryPresent, machine, "")
           if !ok {
               continue
           }
           emitted[agentID] = true
           results = append(results, ParseResult{
               Session: *sub, Messages: subMsgs, UsageEvents: sub.UsageEvents})
       }
       if len(results) > 0 && results[0].Session.ID == fullID {
           junieLinkSubagentCalls(results, s.streams, emitted, rawID)
           if results[0].Session.TerminationStatus == TerminationToolCallPending {
               stopReason := ""
               switch {
               case s.lastTaskState == "COMPLETED":
                   stopReason = "task_complete"
               case summary.lifecycle == "FINISHED" || summary.lifecycle == "COMPLETED":
                   stopReason = "task_complete"
               }
               results[0].Session.TerminationStatus = Classify(
                   results[0].Messages, stopReason, s.lastLineBad)
           }
       }
       return results, excluded, nil
   }
   ```

   (`req.Fingerprint.Hash` stamping stays in `indexCache.parseFile` — apply
   it to EVERY result's `Session.File`, including children.)
2. In `junie_subagent.go` port from old code:
   - `junieFinishSubagent` (old `junie.go:1020-1146`) adapted: `meta` param →
     `summary junieSessionSummary, summaryPresent bool`; `meta.projectDir` →
     `summary.projectDir`; `meta.createdAt/updatedAt` → `summary.createdAt/
     updatedAt`; `project := filepath.Base(filepath.Clean(cwd))` — NO, keep
     the CURRENT project extraction for parity with parent:
     `project := ExtractProjectFromCwdWithBranchContext(WithoutFilesystemProjectDiscovery(ctx), cwd, "")` — pass ctx in. If `project == ""` leave empty for children? Parent defaults `"junie"` — use the same `"junie"` fallback for consistency.
   - `junieLinkSubagentCalls` — the 5-tier matcher (old `junie.go:431-615`)
     verbatim EXCEPT the candidate lists iterate only over `emitted` agents
     (fixes the old dangling-`SubagentSessionID` bug): replace
     `for _, agentID := range subIDs` loops with `for agentID := range emitted`
     or filter `subIDs` by `emitted`.
   - `junieBaseRawSessionID`, `junieSessionIDSourceFiles` (old
     `junie_provider.go:71-91` verbatim; uses `IsValidSessionID` from
     `discovery.go:907`).
3. `junie_provider.go`:
   - Add options in `newJunieSourceSetWithCache`:
     `WithRawSessionIDForLookup(junieBaseRawSessionID)`,
     `WithRawSessionIDSourceFiles(junieSessionIDSourceFiles)`,
     `WithProjectHint(func(root, path string) string { return "" })`,
     `WithDescendPath(func(root, path string) bool { return filepath.Dir(path) == root })`.
   - Add method:
     ```go
     func (s junieSourceSet) StoredSourceHintScopes(
         req ChangedPathRequest,
     ) []StoredSourceHintScope {
         path := req.Path
         if container, _, ok := ParseVirtualSourcePathForBase(path, "events.jsonl"); ok {
             path = container
         }
         if filepath.Base(path) != "events.jsonl" {
             return nil
         }
         dir := filepath.Dir(path)
         for _, root := range s.roots {
             if filepath.Dir(dir) == filepath.Clean(root) {
                 return []StoredSourceHintScope{{
                     Path: path, IncludeVirtualMembers: true,
                 }}
             }
         }
         return nil
     }
     ```
   - `junieProviderCapabilities()`: add `source.MultiSessionSource =
     CapabilitySupported`, `source.ExcludedSessions = CapabilitySupported`;
     Content: `Relationships`, `Subagents`, `ToolResults`,
     `ToolResultEvents` = Supported (Thinking/ToolCalls/TerminationStatus/
     TruncationStatus landed in Tasks 1–3; ensure they're all listed).
4. `internal/sync/engine.go` around line 12709: extend the condition

   ```go
   if (file.Agent == parser.AgentKiro ||
       file.Agent == parser.AgentCline ||
       (file.Agent == parser.AgentDevin && outcome.ForceReplace) ||
       (file.Agent == parser.AgentOmnigent && outcome.ForceReplace) ||
       (file.Agent == parser.AgentCursorIDE && outcome.ForceReplace) ||
       (file.Agent == parser.AgentJunie && outcome.ForceReplace) ||   // NEW
       (file.Agent == parser.AgentTrae && !e.forceParse)) &&
   ```

   This arm calls `providerSourceMissingSessionOwnershipsForCompleteResultWithPreserved`
   → `providerSourceSessionOwnershipsForForceReplace`, which consults the
   provider's `StoredSourceHintScopes` — our `IncludeVirtualMembers` scope
   makes stored `events.jsonl#agent-N` rows visible to the sweep, so a
   vanished subagent tombstones instead of lingering. The
   `MultiSessionSource` capability (step 3) already makes
   `applyProviderFilePathPolicies` skip the single-session stale-row cleanup
   (`engine.go:13714`), which would otherwise delete children sharing the
   path.
   **Before this edit read `docs/agents/storage.md` and `docs/agents/background-work.md`.**

- [ ] **Step 4: Run tests**

Run: `go build ./... && go test ./internal/parser/ -run 'Junie|junie' -count=1 && go test ./internal/sync/ -run 'Junie' -count=1 && go test ./internal/sync/ -run 'TestProcess|MissingMember' -count=1`
Expected: PASS. Watch for regressions in `engine_test.go` arms touching the
modified condition.

- [ ] **Step 5: Commit**

```bash
git add internal/parser/junie.go internal/parser/junie_subagent.go internal/parser/junie_provider.go internal/parser/junie_provider_test.go internal/parser/junie_test.go internal/sync/engine.go internal/sync/junie_integration_test.go
git commit -m "feat(parser): synthesize Junie BuiltInAgent streams into linked subagent sessions"
```

---

### Task 6: summary.json/state.json sidecars + companion freshness

`summary.json` overrides index metadata and carries `lifecycle`;
`state.json`'s blob supplies cwd/title fallbacks; both fold into freshness.

**Files:**
- Modify: `internal/parser/junie.go` (`readJunieSidecarFile`, `junieLoadSidecarMeta`, `junieStateMeta`, `junieSessionSummary.lifecycle`, summary merge)
- Modify: `internal/parser/junie_provider.go` (companion options, `WithContentHashing`, fingerprint fold)
- Test: `internal/parser/junie_test.go`, `internal/parser/junie_provider_test.go`

**Interfaces:**
- Produces:
  ```go
  func readJunieSidecarFile(sessionDir, name string, openRoot junieRootOpener) ([]byte, error)
  func junieLoadSidecarMeta(sessionDir string, openRoot junieRootOpener, summary *junieSessionSummary) // mutates: summary.json overrides
  func junieStateMeta(raw []byte) (cwd, taskName string)
  func junieCompanionFiles(transcriptPath string) []string
  func junieCompanionTranscript(companionPath string) (string, bool)
  ```

- [ ] **Step 1: Write the failing tests**

```go
func TestJunieSummaryJSONLifecycleCancelled(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-260101-120000-abcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Hi","timestampMs":1704067200000}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"t1","event":{"agentEvent":{"kind":"ResultBlockUpdatedEvent","stepId":"s1","result":"ok"}},"timestampMs":1704067200100}` + "\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.json"),
		[]byte(`{"taskName":"Sidecar title","lifecycle":"CANCELLED"}`), 0o600))

	sess, _ := parseJunieProviderSession(t, path, "local")
	assert.Equal(t, "Sidecar title", sess.SessionName)
	assert.Equal(t, TerminationTruncated, sess.TerminationStatus)
}

func TestJunieStateJSONCwdFallback(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-260101-120000-abcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Hi","timestampMs":1704067200000}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"t1","event":{"agentEvent":{"kind":"ResultBlockUpdatedEvent","stepId":"s1","result":"ok"}},"timestampMs":1704067200100}` + "\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "state.json"),
		[]byte(`{"event":{"agentEvent":{"blob":"{\"currentDirectory\":\"/work/stateproj\",\"lastAgentState\":{\"issue\":{\"name\":\"State title\"}}\"}"}}}`), 0o600))

	sess, _ := parseJunieProviderSession(t, path, "local")
	assert.Equal(t, "/work/stateproj", sess.Cwd)
	assert.Equal(t, "stateproj", sess.Project)
	assert.Equal(t, "State title", sess.SessionName)
}
```

```go
func TestJunieFingerprintFoldsCompanions(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-260101-120000-abcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Hi","timestampMs":1704067200000}`+"\n"), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	fp1, err := provider.Fingerprint(t.Context(), sources[0])
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.json"),
		[]byte(`{"taskName":"t","lifecycle":"COMPLETED"}`), 0o600))
	fp2, err := provider.Fingerprint(t.Context(), sources[0])
	require.NoError(t, err)
	assert.NotEqual(t, fp1.Hash, fp2.Hash)
}

func TestJunieCompanionChangedPathMapsToTranscript(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-260101-120000-abcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Hi","timestampMs":1704067200000}`+"\n"), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{
		Path: filepath.Join(sessionDir, "summary.json"),
	})
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, path, sources[0].DisplayPath)
}

func TestJunieWatchPlanIncludesSidecars(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-260101-120000-abcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "events.jsonl"), []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Hi","timestampMs":1704067200000}`+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.json"), []byte(`{}`), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	plan, err := provider.WatchPlan(t.Context())
	require.NoError(t, err)
	require.Len(t, plan.Roots, 1)
	assert.Contains(t, plan.Roots[0].IncludeGlobs, "summary.json")
	assert.Contains(t, plan.Roots[0].IncludeGlobs, "state.json")
}
```

(The provider tests go into the NEW `internal/parser/junie_provider_test.go`.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/parser/ -run 'TestJunieSummary|TestJunieState|TestJunieFingerprintFolds|TestJunieCompanion|TestJunieWatchPlan' -count=1`
Expected: FAIL — sidecars unread, globs missing.

- [ ] **Step 3: Implement**

1. `junie.go` — root-pinned sidecar read (consistent with existing hardening):

   ```go
   func readJunieSidecarFile(
       sessionDir, name string, openRoot junieRootOpener,
   ) ([]byte, error) {
       root, err := openRoot(filepath.Dir(sessionDir))
       if err != nil {
           return nil, err
       }
       defer root.Close()
       rel := filepath.Join(filepath.Base(sessionDir), name)
       info, err := root.Lstat(rel)
       if err != nil {
           return nil, err
       }
       if !info.Mode().IsRegular() {
           return nil, errors.New("junie sidecar is not a regular file")
       }
       f, err := openJuniePinnedFile(root, rel, info)
       if err != nil {
           return nil, err
       }
       defer f.Close()
       return io.ReadAll(f)
   }
   ```

   (needs `"io"` import)
2. `junieLoadSidecarMeta` — merge `summary.json` over the index-derived
   `junieSessionSummary` (old `junie.go:1413-1432` semantics): taskName,
   projectDir, `lifecycle|status|state` → `summary.lifecycle`, createdAt,
   updatedAt — each only when the sidecar field is non-empty/non-zero.
   `junieStateMeta(raw []byte)` — old `junie.go:1440-1452` body verbatim.
3. In `parseJunieSessionWithSummary`: after opening the stream, call
   `junieLoadSidecarMeta(sessionDir, openRoot, &summary)` (ignore os.IsNotExist
   errors, propagate real errors) and read `state.json` → `stateCwd`,
   `stateTaskName`. Cwd chain becomes:
   `summary.projectDir → main.cwd → stateCwd`; name chain:
   `s.sessionName/main taskName → summary.taskName → stateTaskName`.
   Also add `Status string` to `junieIndexSummary` (json `status,omitempty`)
   and map it into `junieSessionSummary.lifecycle` in `sessionSummary()` —
   index `status:"completed"` then feeds the `stopReason` switch.
4. `junie_provider.go` — add options:
   `WithContentHashing()`, `WithCompanionFiles(junieCompanionFiles)`,
   `WithCompanionTranscript(junieCompanionTranscript)`,
   `WithRejectSymlinkCompanions()` — and in `junieSourceSet.Fingerprint`,
   after the transcript hash is computed and BEFORE the index-summary fold,
   call `s.JSONLSourceSet.foldCompanionFingerprint(src.Path, &fingerprint)`
   (method exists at `jsonl_source_set.go:480`; it mixes companion size/mtime
   into `Size`/`MTimeNS` and contents into `Hash` because
   `options.Hash` is now true).
   Add the two helper funcs (old `junie_provider.go:51-67` verbatim, using
   constants `junieEventsFile = "events.jsonl"`, `junieStateFile =
   "state.json"`, `junieSummaryFile = "summary.json"` — define the consts in
   junie.go).
5. `SourcesForChangedPath`/`WatchPlan` get companion behavior free from the
   embedded `JSONLSourceSet` (changed-path arm delegates to
   `s.JSONLSourceSet.SourcesForChangedPath` — `junie_provider.go:135`; watch
   globs via `companionGlobs` — `jsonl_source_set.go:219`).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/parser/ -run 'Junie|junie' -count=1 && go test ./internal/sync/ -run 'Junie' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/parser/junie.go internal/parser/junie_provider.go internal/parser/junie_test.go internal/parser/junie_provider_test.go
git commit -m "feat(parser): read Junie summary/state sidecars and fold them into source freshness"
```

---

### Task 7: Timestamp fallbacks + UX polish + AgentDef usage flag

**Files:**
- Modify: `internal/parser/junie.go` (`junieTimestampFromID`, `isDigit2`, mtime fallbacks, zero-ts backfill, `SourceUUID`)
- Modify: `internal/parser/types.go` (Junie `AgentDef` `Usage.NoPerMessageTokenData`)
- Test: `internal/parser/junie_test.go`, `internal/parser/types_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestJunieFallsBackToIDTimestampWhenTimestampMsMissing(t *testing.T) {
	root := t.TempDir()
	sessionID := "session-260423-110831-1hhe"
	sessionDir := filepath.Join(root, sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Hi"}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"t1","event":{"agentEvent":{"kind":"ResultBlockUpdatedEvent","stepId":"s1","result":"ok"}}}` + "\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))

	sess, messages := parseJunieProviderSession(t, path, "local")
	want := time.Date(2026, 4, 23, 11, 8, 31, 0, time.UTC)
	assert.Equal(t, want, sess.StartedAt.UTC())
	for _, m := range messages {
		assert.Equal(t, want, m.Timestamp.UTC(), "zero-ts messages backfill to session start")
	}
}

func TestJunieFallsBackToFileMtime(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-notimestamp")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Hi"}`+"\n"), 0o600))
	mtime := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(path, mtime, mtime))

	sess, _ := parseJunieProviderSession(t, path, "local")
	assert.Equal(t, mtime, sess.StartedAt.UTC())
}

func TestJunieUserPromptSourceUUID(t *testing.T) {
	// extend an existing fixture or standalone:
	// assert messages[0].SourceUUID == its requestId
}
```

In `internal/parser/types_test.go` add a Junie registry assertion matching
the convention (see `TestStepCodeRegistryEntry`-style tests): Junie def has
`Usage.NoPerMessageTokenData == true`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/parser/ -run 'TestJunieFallsBack|TestJunieUserPromptSourceUUID|Junie' -count=1`
Expected: FAIL — zero `StartedAt`/`Timestamp`, `SourceUUID` empty, flag unset.

- [ ] **Step 3: Implement**

1. `junie.go` — port `junieTimestampFromID` + `isDigit2` verbatim (old
   `junie.go:1457-1480`).
2. `parentResult`/`junieFinishSubagent` fallback chains (old
   `junie.go:935-956`, `1049-1070`): `startedAt` ← min ts → summary.createdAt
   → `junieTimestampFromID(rawID)` → file mtime; `endedAt` ← max ts →
   summary.updatedAt → mtime clamp. Backfill zero message timestamps to
   `startedAt` (old `junie.go:958-962`, `1072-1076`).
3. `consumeUserPrompt`: set `SourceUUID: requestID` on the appended/updated
   entry (old `junie.go:236`).
4. `types.go` Junie `AgentDef` — add:

   ```go
   Usage: UsageCapabilities{
       NoPerMessageTokenData: true,
   },
   ```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/parser/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/parser/junie.go internal/parser/types.go internal/parser/junie_test.go internal/parser/types_test.go
git commit -m "feat(parser): restore Junie timestamp fallbacks, prompt UUIDs, and usage capability flags"
```

---

### Task 8: Documentation + changelog

Re-verify the Junie entry in `docs/internal/session-format-sources.md`
(required by the AGENTS.md provider-format-provenance rule) and add a
changelog entry.

**Files:**
- Modify: `docs/internal/session-format-sources.md` (Junie section ~line 3938)
- Modify: `docs/changelog.md`
- Check: `README.md` Junie blurb (~line 445) — update if it lists features
  inaccurately

- [ ] **Step 1: Reconcile the format doc**

The current entry (`session-format-sources.md:3938-3998`) already describes
the OLD behavior — after the port most of it is accurate again. Verify each
claim against the NEW code and fix the deltas:
- `subagents/*.md` and `task-*/terminal-output/*.txt` were never read by ANY
  implementation — keep them in the on-disk inventory but mark clearly they
  are not parsed (or drop from parser-behavior claims).
- `status` on index rows now feeds `lifecycle` (Task 6).
- Usage dedup is content-hash based; `DedupKey` format changed — describe it.
- Evidence line stays `no-public-source` unless the port uncovered a public
  source — update per the provenance rule.
Format with `mdformat --wrap 80` if available.

- [ ] **Step 2: Changelog entry**

Add under the unreleased section of `docs/changelog.md`, plain-language
outcome first, e.g. "Junie sessions now show thinking, tool calls, and
subagent runs as linked sessions, and mark cancelled or failed tasks
correctly. Provider-reported usage no longer double-counts rows Junie
re-emits."

- [ ] **Step 3: Run checks**

Run: `go fmt ./... && go vet ./... && go build ./... && go test ./internal/parser/ -count=1`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add docs/internal/session-format-sources.md docs/changelog.md README.md
git commit -m "docs: realign Junie format notes with restored parser fidelity"
```

---

## Self-Review Notes

- **Spec coverage:** every HIGH/MEDIUM port candidate from the analysis is
  mapped: streams/grouping/thinking (T1), tool calls (T2), cancel/fail/TaskState
  + termination/IsTruncated (T3), usage dedup + per-agent attribution (T4+T5),
  child sessions + linking + lookup + engine sweep (T5), sidecars + companion
  freshness + index `status`→lifecycle (T6), ID/mtime timestamps + SourceUUID +
  `NoPerMessageTokenData` + cancelled-content exclusion (T3+T7), docs (T8).
  Low items deliberately folded in: missing-stepId dedup (inside
  `junieUpsertBlock`, T1), `SourceSubtype` on system notes (T3),
  `UserAsyncResponseEvent` richer question fallbacks — SKIP (current join is
  adequate; flag if desired).
- **Not ported by design:** `WithSymlinkFollowing`, `session`/`junie-reported`
  usage names, `Model` capability, index `session-`-prefix-tolerant match,
  whole-index-per-parse reads, zero-usage rows, `taskSeq` dead state,
  dangling subagent links (gated on `emitted` now).
- **Order rationale:** Task 5's engine wiring depends on the
  `MultiSessionSource` capability; Task 4's per-stream usage assumes streams
  from Task 1; Task 6's `summary.lifecycle` is declared in Task 3 and
  populated in Task 6 — tests only join in T6.
- **Type consistency:** `junieSessionSummary` gains `lifecycle string` (T3
  declares, T6 fills); `parseJunieSessionWithSummary` returns
  `([]ParseResult, []string, error)` from Task 5 on — Task 6 edits keep that
  signature; `junieTranscriptMessage` stays the wrapper for top-level
  entries only.
