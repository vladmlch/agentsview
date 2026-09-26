# Assistant Turn Collapse Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` to implement this plan task-by-task.
> Use a fresh subagent for each task, then run the two-stage review before
> starting the next task. Tasks are sequential because several share
> `MessageList.svelte` and the transcript projection. Steps use checkbox
> (`- [ ]`) syntax for tracking.

**Goal:** Make transcripts scannable by collapsing assistant turns and
intermediate events while keeping prompts and final answers accessible.

**Architecture:** Keep messages flat in storage and the API. Build a typed
assistant-turn tree in the shared frontend session projection, then flatten its
visible headers, event rows, and final-output rows into the existing virtualized
list. Keep search membership filter-independent and apply temporary type
visibility through an ephemeral search overlay.

**Tech Stack:** Svelte 5, TypeScript, TanStack Virtual, Vite+ / Vitest,
Paraglide JS, Playwright.

**Spec:** `docs/superpowers/specs/2026-09-25-assistant-turn-collapse-design.md`

## Global Constraints

- Do not change the backend, database schema, message API, or archive contents.
- Preserve focused transcript mode; turn grouping applies to normal mode in all
  message layouts (`default`, `compact`, `stream`, and `skim`).
- Persist only the global auto-collapse preference and block-visibility choices.
  Turn, event, drawer, and prompt disclosure state resets on reload or session
  switch.
- Keep temporary search visibility out of persistent `ui.visibleBlocks`.
- Use the numeric `DbMessage.id` plus session ID for source event identity;
  message ordinals remain the keyboard-navigation and read-progress identity.
- Count prompt and text/thinking preview lengths in Unicode code points, not
  UTF-16 code units or bytes; never split a surrogate pair.
- Preserve source-message Copy, Pin, and Fork behavior. Show these actions once
  on the first rendered child event from that source message; Copy copies the
  full source message and Pin/Fork use its original ordinal.
- Error tool-output drawers default open regardless of auto-collapse. Explicit
  bulk/manual state can still close them.
- Add every user-facing string to all locale files in `frontend/messages/` and
  run `npm run i18n:compile`.
- Add colocated frontend tests. Every task follows RED → GREEN and commits
  only its task files plus this plan's completed checkboxes; do not add
  dependencies, change branches, or push.
- Read `frontend/AGENTS.md`, `DESIGN.md`, and `docs/agents/testing.md` before
  frontend edits. Use the shared control vocabulary and Paraglide patterns.
- The last repository-wide `vp check` reported 21 unrelated formatting files.
  Re-run it for evidence, but do not fix unrelated formatting. Run focused
  checks on every changed frontend file and use `vp check --no-fmt` for
  repository-wide lint/type checking.

---

### Task 1: Project Typed Assistant-Turn Events

**Files:**

- Create: `frontend/src/lib/utils/assistant-turns.ts`
- Test: `frontend/src/lib/utils/assistant-turns.test.ts`
- Read/consume: `frontend/src/lib/utils/display-items.ts`
- Read/consume: `frontend/src/lib/utils/content-parser.ts`
- Read/consume: `frontend/src/lib/utils/messages.ts`

**Interfaces:**

- Consumes: `DisplayItem[]` and `MessageItem` from `buildDisplayItems`,
  `DbMessage`, `ContentSegment`, and `DbToolCall`.
- Produces:

  ```ts
  export type TurnEventKind =
    | "message"
    | "thinking"
    | "tool"
    | "skill"
    | "tool-rollup"
    | "system";

  export interface TurnEvent {
    key: string;
    kind: TurnEventKind;
    message: DbMessage;
    ordinals: number[];
    segmentIndex?: number;
    segments?: ContentSegment[];
    toolMessages?: DbMessage[];
    toolCalls?: DbToolCall[];
    label?: string;
  }

  export interface AssistantTurnItem {
    kind: "assistant-turn";
    key: string;
    firstMessageId: number;
    messages: DbMessage[];
    events: TurnEvent[];
    ordinals: number[];
    finalOutput: TurnEvent | null;
    model: string | null;
    timestamp: string;
  }

  export interface PromptNode {
    kind: "prompt";
    item: MessageItem;
    ordinals: number[];
  }

  export interface StandaloneNode {
    kind: "standalone";
    item: DisplayItem;
    ordinals: number[];
  }

  export type TranscriptNode = PromptNode | StandaloneNode | AssistantTurnItem;

  export function buildTranscriptNodes(
    items: readonly DisplayItem[],
    sessionId: string,
  ): TranscriptNode[];
  ```

- Turn keys use `${sessionId}:turn:${firstAssistantMessage.id}`. Event keys use
  `${sessionId}:${message.id}:${event.kind}:${eventIndex}`; tool-rollup keys use
  the first tool message ID.

- [x] **Step 1: Write the failing projection test**

  In `assistant-turns.test.ts`, create messages for a user prompt, an assistant
  message with thinking and text, consecutive tool-only messages, a
  `task_notification`, a final assistant message, a `continuation` card, and a
  second user/assistant exchange. Assert top-level kinds are `prompt`,
  `assistant-turn`, `standalone`, `prompt`, `assistant-turn`. Assert the first
  turn key is `s1:turn:11`, its event kinds are `thinking`, `message`,
  `tool-rollup`, `system`, `message`, and the final output comes from message
  ID 15. Also assert that compact boundaries and
  `continuation`/`resume`/`interrupted` flush a group while `task_notification`
  and `stop_hook` do not.

- [x] **Step 2: Run the projection test and verify RED**

  Run from `frontend/`:

  ```bash
  ./node_modules/.bin/vp test run src/lib/utils/assistant-turns.test.ts
  ```

  Expected: FAIL because `buildTranscriptNodes` and the assistant-turn event
  types do not exist.

- [x] **Step 3: Implement the pure event projection**

  Implement `buildTranscriptNodes` without store imports. It must:

  - Keep user messages and standalone display items in their source order.
  - Use `isSystemBoundaryMessage` and `isSystemMessage` before treating a
    role-user row as a prompt.
  - Emit `task_notification`/`stop_hook` as child system events in the current
    turn; emit `continuation`/`resume`/`interrupted` and compact dividers as
    standalone turn-ending nodes.
  - Split each source assistant message into ordered thinking, text/code,
    skill, and tool events. Keep adjacent text/code segments in one message
    event. Preserve structured tool-call associations and render parallel calls
    with the existing `ParallelGroup` path.
  - Keep `ToolGroupItem` as one `tool-rollup` child event and include sidechain
    messages in their parent assistant turn.
  - Start a standalone assistant turn for pre-prompt assistant items. Select the
    last non-empty user-facing message event as `finalOutput`; later tool,
    thinking, and system events do not replace it.

- [x] **Step 4: Run focused projection and existing display-item tests**

  ```bash
  ./node_modules/.bin/vp test run \
    src/lib/utils/assistant-turns.test.ts \
    src/lib/utils/display-items.test.ts \
    src/lib/utils/transcript-mode.test.ts
  ./node_modules/.bin/vp check \
    src/lib/utils/assistant-turns.ts \
    src/lib/utils/assistant-turns.test.ts
  ```

  Expected: tests and focused format/lint/type checks pass; existing tool-rollup
  and transcript-mode behavior remains unchanged.

- [x] **Step 5: Commit the projection task**

  ```bash
  git add \
    frontend/src/lib/utils/assistant-turns.ts \
    frontend/src/lib/utils/assistant-turns.test.ts \
    docs/superpowers/plans/2026-09-25-assistant-turn-collapse.md
  git commit -m "feat(frontend): project assistant turn events"
  ```

### Task 2: Use One Session Projection for Turns and Search

**Files:**

- Modify: `frontend/src/lib/search/session-scope.ts`
- Test: `frontend/src/lib/search/session-scope.test.ts`
- Modify: `frontend/src/lib/search/session-index.ts`
- Test: `frontend/src/lib/search/session-index.test.ts`
- Modify: `frontend/src/lib/stores/inSessionSearch.svelte.ts`
- Test: `frontend/src/lib/stores/inSessionSearch.test.ts`
- Test: `frontend/src/lib/stores/inSessionSearch-regression.test.ts`
- Modify fixtures: `frontend/src/lib/stores/__fixtures__/search-state.svelte.ts`
- Test: `frontend/src/lib/components/content/MessageList-search.test.ts`

**Interfaces:**

- Consumes: `TranscriptNode[]` from Task 1.
- Produces this shared scope contract:

  ```ts
  export interface SessionScope {
    items: TranscriptNode[];
    normalItems: DisplayItem[];
    messages: Message[];
    allowsBlock(message: Message, kind: SearchBlockKind): boolean;
  }
  ```

  `items` carries full group membership before block-type visibility filtering;
  `messages` contains eligible source messages for the selected transcript mode
  regardless of block-type filters. `normalItems` retains the existing flat
  normal-mode projection for mode-switch and user-prompt navigation. Focused
  mode continues to use its current message-level projection, so hidden
  intermediate focused-mode messages are not added to search.
- `Match` adds `kind: SearchBlockKind` and `role: DbMessage["role"]`, allowing
  search matches to map back to the user/assistant/thinking/tool/code filter
  without parsing `blockKey` strings.
- `InSessionSearchStore` exposes derived
  `revealedBlockTypes: ReadonlySet<BlockType>` and
  `noteManualBlockFilterChange(type: BlockType, visible: boolean)`. Hiding a
  temporarily revealed type suppresses that reveal for the open search view;
  re-enabling it clears suppression.

- [x] **Step 1: Write failing session-scope and index tests**

  Add a fixture with one user prompt and one assistant message whose content is
  `"[Thinking]\nneedle\n[/Thinking]"`. With only `user` and `assistant` block
  types visible, assert that `SessionScope.messages` still contains the source
  assistant message, `allowsBlock(message, "thinking")` is false, and
  `buildSessionIndex(scope.messages, "needle")` returns the thinking match.
  Add a focused-mode case proving intermediate assistant messages remain absent.

- [x] **Step 2: Run focused scope/index tests and verify RED**

  ```bash
  ./node_modules/.bin/vp test run \
    src/lib/search/session-scope.test.ts \
    src/lib/search/session-index.test.ts \
    src/lib/stores/inSessionSearch.test.ts
  ```

  Expected: FAIL because scope membership is currently filtered before search
  and `Match` has no block-kind/role metadata.

- [x] **Step 3: Separate source membership from rendered visibility**

  Update `projectSessionScope` to construct the transcript tree before applying
  child-event visibility. Return all eligible source messages for the selected
  transcript mode, and retain `allowsBlock` for rendered events. Preserve
  `isSystemMessage`, compact-boundary, mid-turn-system, and focused-mode rules.

- [x] **Step 4: Index all block types in the current transcript mode**

  Update `buildSessionIndex` to include each searchable block's `kind` and its
  source message role in `Match`. Build the active search index from the
  filter-independent `SessionScope.messages` and do not pass the current block
  visibility predicate to `buildSessionIndex`.

- [x] **Step 5: Derive ephemeral matched-type reveals**

  Map match kinds to the owning block type (`thinking`, `code`, `tool`, `user`,
  or `assistant`). Reveal only types hidden by the saved filter. Do not write
  those temporary reveals to `ui.visibleBlocks` or local storage. While search
  is active, watch persistent filter changes: a manually hidden type enters
  `suppressedTypes` and stays hidden until manually re-enabled or the search
  view closes. Re-enabling it removes suppression. Clearing the query removes
  active type reveals but keeps suppression while the search view remains open.
  Closing search clears suppression and reveals without changing the user's
  saved filter.

- [x] **Step 6: Update search regressions and verify GREEN**

  Update `MessageList-search.test.ts` so normal-mode search finds hidden
  thinking and tool-output matches, displays matching hidden types temporarily,
  and leaves `ui.visibleBlocks` unchanged. Add a test that manually turns off a
  revealed type and confirms it stays hidden across query changes while the find
  view remains open, then auto-reveals after the find view is closed and
  reopened. Keep the existing focused-mode filtering tests.

  Run:

  ```bash
  ./node_modules/.bin/vp test run \
    src/lib/search/session-scope.test.ts \
    src/lib/search/session-index.test.ts \
    src/lib/stores/inSessionSearch.test.ts \
    src/lib/stores/inSessionSearch-regression.test.ts \
    src/lib/components/content/MessageList-search.test.ts
  ./node_modules/.bin/vp check \
    src/lib/search/session-scope.ts \
    src/lib/search/session-index.ts \
    src/lib/stores/inSessionSearch.svelte.ts \
    src/lib/components/content/MessageList-search.test.ts
  ```

  Expected: tests and focused checks pass, including hidden-type search and
  saved-filter preservation.

- [x] **Step 7: Commit the scope/search task**

  ```bash
  git add \
    frontend/src/lib/search/session-scope.ts \
    frontend/src/lib/search/session-scope.test.ts \
    frontend/src/lib/search/session-index.ts \
    frontend/src/lib/search/session-index.test.ts \
    frontend/src/lib/stores/inSessionSearch.svelte.ts \
    frontend/src/lib/stores/inSessionSearch.test.ts \
    frontend/src/lib/stores/inSessionSearch-regression.test.ts \
    frontend/src/lib/stores/__fixtures__/search-state.svelte.ts \
    frontend/src/lib/components/content/MessageList-search.test.ts \
    docs/superpowers/plans/2026-09-25-assistant-turn-collapse.md
  git commit -m "feat(frontend): search through filtered transcript events"
  ```

### Task 3: Add Session-Scoped Turn Expansion State

**Files:**

- Create: `frontend/src/lib/stores/turn-collapse.svelte.ts`
- Test: `frontend/src/lib/stores/turn-collapse.test.ts`
- Modify: `frontend/src/lib/stores/ui.svelte.ts`
- Test: `frontend/src/lib/stores/ui.test.ts`

**Interfaces:**

- `TurnCollapseStore` owns one active `sessionId`, an optional bulk baseline
  (`"expand" | "collapse" | null`), and maps for manual turn, event, nested
  tool-output/history, and user-prompt overrides.
- Public methods:

  ```ts
  activateSession(sessionId: string | null): void;
  isTurnExpanded(key: string, defaultExpanded: boolean): boolean;
  isEventExpanded(key: string, defaultExpanded: boolean): boolean;
  isToolSectionExpanded(key: string, defaultExpanded: boolean): boolean;
  isUserPromptExpanded(messageId: number): boolean;
  setTurnExpanded(key: string, expanded: boolean): void;
  setEventExpanded(key: string, expanded: boolean): void;
  setToolSectionExpanded(key: string, expanded: boolean): void;
  setUserPromptExpanded(messageId: number, expanded: boolean): void;
  expandAll(): void;
  collapseAll(): void;
  migrateTurnKey(oldKey: string, newKey: string): void;
  ```

- Resolution order is explicit manual state, then session bulk baseline, then
  the caller's default. `expandAll`/`collapseAll` replace the bulk baseline and
  clear prior turn/event/tool-section overrides. They do not change user-prompt
  disclosures. A later manual click creates a new override. `activateSession`
  clears all per-session state, including prompt disclosures, when the session
  changes.
- Add `ui.autoCollapseAssistantTurns`, default `true`, with
  `ui.setAutoCollapseAssistantTurns(enabled: boolean)`. Back it with
  `agentsview-auto-collapse-turns` through the existing `readStoredBool` and UI
  store persistence effect. It defaults both assistant turn headers and event
  rows to collapsed; the Appearance checkbox UI is added in Task 7.

- [x] **Step 1: Write failing state tests**

  Add tests proving default resolution, manual overrides, bulk overrides across
  keys not yet loaded, manual exceptions after a bulk action, reset on session
  change, key migration, and reset of exceptions on the next bulk action. Also
  assert that expand/collapse all never changes user-prompt disclosure state.
  Include an exact assertion that a manual event override wins over the bulk
  baseline until the next call to `expandAll` or `collapseAll`.

- [x] **Step 2: Run focused state tests and verify RED**

  ```bash
  ./node_modules/.bin/vp test run src/lib/stores/turn-collapse.test.ts
  ```

  Expected: FAIL because the turn expansion store does not exist.

- [x] **Step 3: Implement the state resolver and test its transitions**

  Implement the maps and methods above as a pure session-scoped state module
  using Svelte `$state` only for observable fields. Do not persist group, event,
  output, or prompt overrides.

- [x] **Step 4: Add the persisted UI preference test first**

  In `ui.test.ts`, assert `autoCollapseAssistantTurns` defaults to `true`, can
  be set to `false`, and persists `"false"` under
  `agentsview-auto-collapse-turns` after a Svelte tick.

- [x] **Step 5: Implement the UI preference**

  Add a constant key, `readStoredBool(key, true)` initialization, setter, and a
  `$effect` local-storage write following the existing follow-latest preference
  pattern.

- [x] **Step 6: Run focused store tests and verify GREEN**

  ```bash
  ./node_modules/.bin/vp test run \
    src/lib/stores/turn-collapse.test.ts \
    src/lib/stores/ui.test.ts
  ./node_modules/.bin/vp check \
    src/lib/stores/turn-collapse.svelte.ts \
    src/lib/stores/turn-collapse.test.ts \
    src/lib/stores/ui.svelte.ts \
    src/lib/stores/ui.test.ts
  ```

  Expected: tests and focused format/lint/type checks pass for all state and
  persistence cases.

- [x] **Step 7: Commit the state task**

  ```bash
  git add \
    frontend/src/lib/stores/turn-collapse.svelte.ts \
    frontend/src/lib/stores/turn-collapse.test.ts \
    frontend/src/lib/stores/ui.svelte.ts \
    frontend/src/lib/stores/ui.test.ts \
    docs/superpowers/plans/2026-09-25-assistant-turn-collapse.md
  git commit -m "feat(frontend): add assistant turn expansion state"
  ```

### Task 4: Render Turn Headers and Typed Event Rows

**Files:**

- Create: `frontend/src/lib/components/content/AssistantTurn.svelte`
- Create: `frontend/src/lib/components/content/AssistantTurnEventRow.svelte`
- Create: `frontend/src/lib/components/content/AssistantTurnOutput.svelte`
- Create: `frontend/src/lib/components/content/MessageSourceActions.svelte`
- Modify: `frontend/src/lib/components/content/MessageContent.svelte`
- Modify: `frontend/src/lib/components/content/ThinkingBlock.svelte`
- Modify: `frontend/src/lib/components/content/SkillBlock.svelte`
- Modify: `frontend/src/lib/components/content/ToolBlock.svelte`
- Modify: `frontend/src/lib/components/content/ToolCallGroup.svelte`
- Modify: `frontend/src/lib/components/content/ParallelGroup.svelte`
- Modify: `frontend/messages/*.json`
- Test: `frontend/src/lib/components/content/MessageContent.test.ts`
- Test: `frontend/src/lib/components/content/AssistantTurn.test.ts`
- Test: `frontend/src/lib/components/content/AssistantTurnEventRow.test.ts`
- Test: `frontend/src/lib/components/content/MessageSourceActions.test.ts`
- Test: `frontend/src/lib/components/content/ThinkingBlock.test.ts` (new)
- Test: `frontend/src/lib/components/content/ParallelGroup.test.ts`
- Test: `frontend/src/lib/components/content/ToolCallGroup.test.ts`
- Test: `frontend/src/lib/components/content/ToolBlock.test.ts`

**Interfaces:**

- `AssistantTurn` consumes `AssistantTurnItem` and `TurnCollapseStore`. It
  renders the group header, child event disclosures, and the always-visible
  final-output row.
- `AssistantTurnEventRow` consumes one `TurnEvent`, a stable turn/event key,
  search state, and a boolean `ownsSourceActions`, true on the first currently
  rendered child event for each source message. Text/thinking previews use the
  first line up to 80 Unicode code points; tool events use structured summaries.
  It passes the original `segmentIndex` through to search-key generation instead
  of reindexing the event's filtered segment subset.
- `MessageSourceActions` consumes a `DbMessage` and optional `allowMutations`
  flag. It preserves full-message Copy, Pin, and Fork behavior and renders only
  on the first rendered child event from a source message.
- `MessageContent` gains optional `eventSegments?: readonly ContentSegment[]`
  and `hideMessageHeader?: boolean` (default `false`). Without `eventSegments`,
  it preserves full-message behavior. Turn-event rendering passes only the
  selected message/code segments and sets `hideMessageHeader`; the event row
  owns the disclosure header and renders source actions only on its first
  visible event for that source message.

- [x] **Step 1: Add failing component tests for one message split into events**

  Build a message with this exact content:

  ```ts
  const content = [
    "[Thinking]",
    "plan",
    "[/Thinking]",
    "",
    "Visible answer.",
    "",
    "```ts",
    "const n = 1;",
    "```",
  ].join("\n");
  ```

  Assert the assistant turn has a separate Thinking row and Message row, the
  message row preview starts with `Visible answer.`, and the code fence appears
  only inside the expanded Message row. Assert the final answer remains visible
  when the turn is collapsed and its child row is a separate collapsed preview
  when the turn is expanded. Add 80/81-code-point preview cases containing an
  emoji to prove one emoji counts as one code point. Add ToolBlock cases proving
  error output defaults open independently of the turn preference while normal
  output/history drawers stay collapsed.

- [x] **Step 2: Run the focused component tests and verify RED**

  ```bash
  ./node_modules/.bin/vp test run \
    src/lib/components/content/AssistantTurn.test.ts \
    src/lib/components/content/MessageContent.test.ts
  ```

  Expected: FAIL because the turn/event renderers and segment override do not
  exist.

- [x] **Step 3: Extract source-message actions without changing behavior**

  Move the existing Copy, Pin, and Fork controls and handlers into
  `MessageSourceActions.svelte`. Keep copy formatting, mutation guards,
  feedback, and original source ordinal unchanged. Make `MessageContent` use the
  extracted actions so existing standalone and focused-mode messages retain the
  same controls.

- [x] **Step 4: Implement the event-row renderer**

  Render text/code events with the segment override, thinking and skill events
  with their existing disclosure components, tool events with `ToolBlock` or
  `ParallelGroup`, tool rollups with `ToolCallGroup`, and system child events
  with `SystemBoundaryCard`. Pass a stable disclosure key to each row; do not
  nest another disclosure around an existing event disclosure.

- [x] **Step 5: Render source actions once per source message**

  Show `MessageSourceActions` on the first rendered child event for that source
  message. Copy still copies the whole `DbMessage`; Pin/Fork still target that
  message's ordinal. Do not repeat timestamp/model metadata on every event row;
  the turn header owns the model, counts, timestamp, token, and context summary.

- [x] **Step 6: Add event-row locale messages**

  Add `assistant_turn_event_message`, `assistant_turn_message_count`, and
  `assistant_turn_tool_call_count` to every locale. Use Paraglide plural
  variants for both counts and pass numeric `count` plus a locale-formatted
  `countLabel` at the call site.

- [x] **Step 7: Compile locales and run event-rendering tests**

  Run:

  ```bash
  npm run i18n:compile
  ./node_modules/.bin/vp test run \
    src/lib/components/content/AssistantTurn.test.ts \
    src/lib/components/content/AssistantTurnEventRow.test.ts \
    src/lib/components/content/MessageSourceActions.test.ts \
    src/lib/components/content/MessageContent.test.ts \
    src/lib/components/content/ThinkingBlock.test.ts \
    src/lib/components/content/SkillBlock.test.ts \
    src/lib/components/content/ToolBlock.test.ts \
    src/lib/components/content/ToolCallGroup.test.ts \
    src/lib/components/content/ParallelGroup.test.ts
  ./node_modules/.bin/vp check \
    src/lib/components/content/AssistantTurn.svelte \
    src/lib/components/content/AssistantTurnEventRow.svelte \
    src/lib/components/content/AssistantTurnOutput.svelte \
    src/lib/components/content/MessageSourceActions.svelte \
    src/lib/components/content/MessageContent.svelte \
    src/lib/components/content/ThinkingBlock.svelte \
    src/lib/components/content/SkillBlock.svelte \
    src/lib/components/content/ToolBlock.svelte \
    src/lib/components/content/ToolCallGroup.svelte \
    src/lib/components/content/ParallelGroup.svelte
  ```

  Expected: Paraglide compiles, event-rendering tests pass, and focused checks
  pass, including existing copy/pin/fork, role, code, tool, skill, thinking,
  result-history, and parallel-call behavior.

- [x] **Step 8: Commit the rendering task**

  ```bash
  git add \
    frontend/src/lib/components/content/AssistantTurn.svelte \
    frontend/src/lib/components/content/AssistantTurnEventRow.svelte \
    frontend/src/lib/components/content/AssistantTurnOutput.svelte \
    frontend/src/lib/components/content/MessageSourceActions.svelte \
    frontend/src/lib/components/content/MessageContent.svelte \
    frontend/src/lib/components/content/ThinkingBlock.svelte \
    frontend/src/lib/components/content/SkillBlock.svelte \
    frontend/src/lib/components/content/ToolBlock.svelte \
    frontend/src/lib/components/content/ToolCallGroup.svelte \
    frontend/src/lib/components/content/ParallelGroup.svelte \
    frontend/src/lib/components/content/AssistantTurn.test.ts \
    frontend/src/lib/components/content/AssistantTurnEventRow.test.ts \
    frontend/src/lib/components/content/MessageSourceActions.test.ts \
    frontend/src/lib/components/content/MessageContent.test.ts \
    frontend/src/lib/components/content/ThinkingBlock.test.ts \
    frontend/src/lib/components/content/SkillBlock.test.ts \
    frontend/src/lib/components/content/ToolBlock.test.ts \
    frontend/src/lib/components/content/ToolCallGroup.test.ts \
    frontend/src/lib/components/content/ParallelGroup.test.ts \
    frontend/messages/*.json \
    docs/superpowers/plans/2026-09-25-assistant-turn-collapse.md
  git commit -m "feat(frontend): render collapsible assistant events"
  ```

### Task 5: Flatten Turn Rows into the Virtual Transcript

**Files:**

- Create: `frontend/src/lib/utils/transcript-rows.ts`
- Test: `frontend/src/lib/utils/transcript-rows.test.ts`
- Modify: `frontend/src/lib/components/content/MessageList.svelte`
- Modify: `frontend/src/lib/components/content/SessionFindView.svelte`
- Modify: `frontend/src/lib/components/content/FindOverviewRail.svelte`
- Test: `frontend/src/lib/components/content/MessageList.test.ts`
- Test: `frontend/src/lib/components/content/FindOverviewRail.test.ts`

**Interfaces:**

- Define these row variants in `transcript-rows.ts`:

  ```ts
  export type TranscriptRow =
    | {
        kind: "display";
        key: string;
        item: DisplayItem;
        ordinals: number[];
        progressOrdinals: number[];
      }
    | {
        kind: "turn-header";
        key: string;
        turn: AssistantTurnItem;
        ordinals: number[];
        progressOrdinals: [];
      }
    | {
        kind: "turn-event";
        key: string;
        turn: AssistantTurnItem;
        event: TurnEvent;
        ordinals: number[];
        progressOrdinals: number[];
      }
    | {
        kind: "final-output";
        key: string;
        turn: AssistantTurnItem;
        event: TurnEvent;
        ordinals: number[];
        progressOrdinals: number[];
      };

  export function flattenTranscriptRows(
    nodes: readonly TranscriptNode[],
    isTurnExpanded: (key: string) => boolean,
    isEventVisible: (event: TurnEvent) => boolean,
    newestFirst: boolean,
  ): TranscriptRow[];
  ```

- `flattenTranscriptRows` returns source rows plus one turn-header row, child
  event rows only when expanded, and a separate final-output row whenever the
  assistant-text filter allows it. In newest-first mode, reverse top-level nodes
  and child events, keep each turn header first, and place its final-output row
  immediately after the header.
- A collapsed turn header retains all member ordinals for click/selection
  lookup, but has an empty `progressOrdinals` array. Visible event/final rows
  supply their own source ordinals. Tool rollups use the existing
  `[data-message-ordinal]` measurement behavior to count only visible calls.

- [x] **Step 1: Write failing flattening and read-progress tests**

  Test one collapsed turn with source ordinals `[1, 2, 3]` and a final output at
  ordinal `3`: chronological rows must be `turn-header`, child events, and
  `final-output`; the header has `ordinals: [1, 2, 3]` and
  `progressOrdinals: []`, and the output row has `ordinals: [3]` and
  `progressOrdinals: [3]`. After expanding the turn, assert one child row per
  event appears between header and final output. In newest-first mode, assert
  the header stays first, the final-output row follows it, and child events
  reverse.

- [x] **Step 2: Run the focused row tests and verify RED**

  ```bash
  ./node_modules/.bin/vp test run src/lib/utils/transcript-rows.test.ts
  ```

  Expected: FAIL because `flattenTranscriptRows` and `TranscriptRow` do not
  exist.

- [x] **Step 3: Implement the pure flattening function**

  Preserve source keys. Use `${sessionId}-th-${turn.key}` for headers,
  `${sessionId}-te-${event.key}` for child events, and
  `${sessionId}-fo-${turn.key}` for final-output rows. When `sortNewestFirst` is
  enabled, reverse top-level nodes and child events but keep each turn header
  first and its full final-output row immediately after the header.

- [x] **Step 4: Replace `MessageList`'s virtual source with transcript rows**

  Use `TranscriptRow.key` for `getItemKey`, retain the session measurement
  cache, and render the correct row component by `kind`. The existing
  `getDisplayItems` and `getNormalDisplayItems` methods continue returning flat
  `DisplayItem[]` for `App.svelte` and mode auto-switch logic. Add
  `getNavigableOrdinals()` that returns unique visible source-message ordinals
  in transcript order.

- [x] **Step 5: Preserve search overview geometry and read progress**

  Pass virtual transcript rows to `SessionFindView` and `FindOverviewRail`,
  mapping each match ordinal to its visible child/final row offset. Compute
  `displayedOrdinals`, `latestDisplayedOrdinal`, and the unread boundary from
  `progressOrdinals`, not group membership. Keep the final-output source ordinal
  included only while its separate row is visible.

- [x] **Step 6: Run focused virtual-list tests and verify GREEN**

  ```bash
  ./node_modules/.bin/vp test run \
    src/lib/utils/transcript-rows.test.ts \
    src/lib/components/content/MessageList.test.ts \
    src/lib/components/content/FindOverviewRail.test.ts
  ./node_modules/.bin/vp check \
    src/lib/utils/transcript-rows.ts \
    src/lib/components/content/MessageList.svelte \
    src/lib/components/content/SessionFindView.svelte \
    src/lib/components/content/FindOverviewRail.svelte
  ```

  Expected: tests and focused checks pass for collapsed/expanded row shape,
  ordinal selection, read progress, measured scrolling, and find overview
  offsets.

- [x] **Step 7: Commit the virtual-row task**

  ```bash
  git add \
    frontend/src/lib/utils/transcript-rows.ts \
    frontend/src/lib/utils/transcript-rows.test.ts \
    frontend/src/lib/components/content/MessageList.svelte \
    frontend/src/lib/components/content/SessionFindView.svelte \
    frontend/src/lib/components/content/FindOverviewRail.svelte \
    frontend/src/lib/components/content/MessageList.test.ts \
    frontend/src/lib/components/content/FindOverviewRail.test.ts \
    docs/superpowers/plans/2026-09-25-assistant-turn-collapse.md
  git commit -m "feat(frontend): virtualize assistant turn rows"
  ```

### Task 6: Wire Search, Navigation, Pagination, and Bulk Controls

**Files:**

- Modify: `frontend/src/lib/components/content/MessageList.svelte`
- Modify: `frontend/src/lib/components/layout/AppHeader.svelte`
- Modify: `frontend/src/App.svelte`
- Modify: `frontend/messages/*.json`
- Test: `frontend/src/lib/components/content/MessageList-search.test.ts`
- Test: `frontend/src/lib/components/content/MessageList.test.ts`
- Test: `frontend/src/App.test.ts`
- Test: `frontend/src/lib/components/layout/AppHeader.test.ts`

**Interfaces:**

- `MessageList.scrollToOrdinal(ordinal)` resolves the owning event/turn,
  expands a collapsed turn, waits for virtualizer measurement, then scrolls to
  the source-message event row.
- `getNavigableOrdinals()` returns each visible source message ordinal once.
  `App.navigateMessage` uses that list for `j`/`k`; turn headers never add a
  step. `findUserPromptOrdinal` remains user-message based.
- `TurnCollapseStore.expandAll()`/`collapseAll()` provide the transcript-strip
  buttons and affect every assistant turn/event/nested output across unloaded
  pages and current filters. Hide these buttons in focused mode.

- [x] **Step 1: Write failing search-reveal integration tests**

  Update `MessageList-search.test.ts`: with only user blocks visible, normal
  mode search for a thinking/tool-output needle must return its match,
  temporarily render its block type, expand the matching assistant turn/event,
  and reveal the exact `[data-search-block]`. Assert `ui.visibleBlocks` and
  local storage are unchanged. Toggle that type off from the filter while search
  is active; assert the manual choice hides it immediately and remains after
  closing search.

- [x] **Step 2: Run the focused search test and verify RED**

  ```bash
  ./node_modules/.bin/vp test run \
    src/lib/components/content/MessageList-search.test.ts
  ```

  Expected: FAIL because current search indexing excludes filter-hidden blocks
  and the virtualizer cannot expand a parent turn.

- [x] **Step 3: Expand the owning turn before ordinal navigation**

  In `scrollToOrdinal`, search reveal, external pending-scroll, and `j`/`k`
  navigation, look up the source message's `AssistantTurnItem`. Set that turn's
  event state to expanded before computing the destination virtual-row index.
  Preserve existing delayed-load and request-cancellation checks.

- [x] **Step 4: Add older-page key migration and streaming assertions**

  On each new `TranscriptNode` projection, compare the prior and current leading
  partial turn keys. Call `turnCollapse.migrateTurnKey(oldKey, newKey)` when
  `loadOlder` extends it. Add tests proving a manually expanded partial group
  stays expanded after `loadOlder` and the active final-output row updates as
  `content_length` grows while the turn stays collapsed by default.

- [x] **Step 5: Add `j`/`k` and prompt-jump tests**

  In `App.test.ts`, assert that multiple child events from one `DbMessage`
  still contribute one navigation ordinal; a collapsed event target expands its
  turn; turn-header rows are not navigation stops; and Shift+J/K continues to
  skip system-boundary rows and respect the user-block filter.

- [x] **Step 6: Add transcript-strip bulk controls and filter interaction**

  In `AppHeader.svelte`, add one toggle button to `.transcript-strip` in normal
  mode only. Label it `Expand all` unless every visible eligible assistant
  event is expanded; then label it `Collapse all`. Wire both actions to the
  session-scoped bulk baseline so hidden and unloaded events also change.

  Display each block-filter checkbox as checked when it is either persistently
  visible or temporarily search-revealed. When a filter is manually changed,
  call `inSessionSearch.noteManualBlockFilterChange(type, nextVisible)` before
  `ui.setBlockVisible`. The `Show all` action sets all types visible and clears
  search suppressions. Test the button states, focused-mode absence, and
  manual filter override in `AppHeader.test.ts` and
  `MessageList-search.test.ts`.

- [x] **Step 7: Add localized bulk-action labels**

  Add `transcript_expand_all` and `transcript_collapse_all` to every locale
  using the source copy `Expand all` and `Collapse all`. Run:

  ```bash
  npm run i18n:compile
  ```

  Expected: Paraglide compilation completes with no missing-key error.

- [x] **Step 8: Run search, list, navigation, and header tests**

  ```bash
  ./node_modules/.bin/vp test run \
    src/lib/components/content/MessageList-search.test.ts \
    src/lib/components/content/MessageList.test.ts \
    src/App.test.ts \
    src/lib/components/layout/AppHeader.test.ts
  ./node_modules/.bin/vp check \
    src/lib/components/content/MessageList.svelte \
    src/lib/components/layout/AppHeader.svelte \
    src/App.svelte \
    src/lib/components/content/MessageList-search.test.ts
  ```

  Expected: tests and focused checks pass for auto-reveal, filter
  restoration/persistence, ordinal navigation, pagination migration,
  streaming, focused-mode controls, and bulk actions.

- [x] **Step 9: Commit the interaction task**

  ```bash
  git add \
    frontend/src/lib/components/content/MessageList.svelte \
    frontend/src/lib/components/layout/AppHeader.svelte \
    frontend/src/App.svelte \
    frontend/src/lib/components/content/MessageList-search.test.ts \
    frontend/src/lib/components/content/MessageList.test.ts \
    frontend/src/App.test.ts \
    frontend/src/lib/components/layout/AppHeader.test.ts \
    frontend/messages/*.json \
    docs/superpowers/plans/2026-09-25-assistant-turn-collapse.md
  git commit -m "feat(frontend): reveal and navigate assistant turn events"
  ```

### Task 7: Add Settings, Prompt Controls, Localization, and User Documentation

**Files:**

- Modify: `frontend/src/lib/components/settings/AppearanceSettings.svelte`
- Test: `frontend/src/lib/components/settings/AppearanceSettings.test.ts`
- Modify: `frontend/src/lib/components/content/MessageContent.svelte`
- Test: `frontend/src/lib/components/content/MessageContent.test.ts`
- Modify: `frontend/messages/{az,en,es,fr,ja,ko,zh-CN,zh-TW}.json`
- Modify: `docs/usage.md`

**Interfaces:**

- `ui.autoCollapseAssistantTurns` is the persisted default preference.
- User prompt expansion is keyed by source `DbMessage.id` in
  `TurnCollapseStore`; it is not affected by Expand/Collapse All.
- New Paraglide keys in this task: `appearance_auto_collapse_assistant_turns`,
  `message_content_show_full_prompt`, and `message_content_show_less`.
  Event-count and bulk-action keys are added in their rendering/control tasks.

- [ ] **Step 1: Write failing setting and prompt tests**

  Add an `AppearanceSettings.test.ts` assertion for the accessible checkbox
  name, its default-on value, toggling to off, and matching `localStorage`
  value.
  With search active, manually hide a temporarily revealed block type through
  AppearanceSettings and assert the content hides and the saved filter remains
  hidden after search closes. Add `MessageContent.test.ts` cases with exactly
  600 and 601 Unicode code points: 600 stays full; 601 shows exactly the first
  500 code points until `Show full prompt` is clicked. Include 600/601
  emoji-only prompts so counting and slicing never split surrogate pairs.
  Verify attachments render independently.

- [ ] **Step 2: Run focused settings and prompt tests and verify RED**

  ```bash
  ./node_modules/.bin/vp test run \
    src/lib/components/settings/AppearanceSettings.test.ts \
    src/lib/components/content/MessageContent.test.ts
  ```

  Expected: FAIL because the preference control and prompt disclosure are not
  present.

- [ ] **Step 3: Add localized preference and prompt labels**

  Add `appearance_auto_collapse_assistant_turns`,
  `message_content_show_full_prompt`, and `message_content_show_less` to every
  locale with identical key sets. Use source copy `Collapse assistant turns by
  default`, `Show full prompt`, and `Show less`. Run:

  ```bash
  npm run i18n:compile
  ```

  Expected: Paraglide compilation completes with no missing-key error.

- [ ] **Step 4: Implement the Appearance preference and prompt disclosure**

  Add the auto-collapse checkbox to the existing Appearance settings rows and
  bind it to `ui.setAutoCollapseAssistantTurns`. Do not send a server settings
  update. When an Appearance block-filter checkbox changes during active search,
  call `inSessionSearch.noteManualBlockFilterChange(type, nextVisible)` before
  saving the persistent `ui.visibleBlocks` choice.

  In `MessageContent`, compute
  `const promptPoints = Array.from(message.content)`. When
  `promptPoints.length > 600`, show `promptPoints.slice(0, 500).join("")` and a
  `Show full prompt` button; once expanded, show `Show less`. Store expansion
  by source message ID in `TurnCollapseStore`; keep attachments outside the
  disclosure and preserve the current image filter.

- [ ] **Step 5: Run setting and prompt tests and verify GREEN**

  ```bash
  npm run i18n:compile
  ./node_modules/.bin/vp test run \
    src/lib/components/settings/AppearanceSettings.test.ts \
    src/lib/components/content/MessageContent.test.ts
  ```

  Expected: PASS for checkbox value/storage, filter override, exact 600/601
  code-point behavior, expansion/collapse copy, and independent attachments.

- [ ] **Step 6: Document the transcript behavior**

  Update `docs/usage.md` in Message Layouts / Message Display and Settings to
  explain collapsed assistant turns, event rows, the auto-collapse preference,
  Expand/Collapse All, final-output visibility, and the 600/500-character user
  prompt preview. Add the preference to the Appearance row in the Settings
  table. Preserve existing descriptions of focused mode and filters.

- [ ] **Step 7: Run component, changed-file, and docs checks**

  ```bash
  ./node_modules/.bin/vp check \
    src/lib/components/settings/AppearanceSettings.svelte \
    src/lib/components/settings/AppearanceSettings.test.ts \
    src/lib/components/content/MessageContent.svelte \
    src/lib/components/content/MessageContent.test.ts
  make docs-check
  ```

  Expected: changed-file format/lint/type checks and documentation validation
  pass.

- [ ] **Step 8: Commit settings, copy, and docs**

  ```bash
  git add \
    frontend/src/lib/components/settings/AppearanceSettings.svelte \
    frontend/src/lib/components/settings/AppearanceSettings.test.ts \
    frontend/src/lib/components/content/MessageContent.svelte \
    frontend/src/lib/components/content/MessageContent.test.ts \
    frontend/messages/*.json \
    docs/usage.md \
    docs/superpowers/plans/2026-09-25-assistant-turn-collapse.md
  git commit -m "feat(frontend): add assistant turn settings"
  ```

### Task 8: Verify the User Workflow and Finish

**Files:**

- Modify: `frontend/e2e/message-content.spec.ts`
- Modify: `frontend/e2e/session-find.spec.ts`
- Modify: `frontend/e2e/session-find-regressions.spec.ts`
- Modify: `docs/superpowers/plans/2026-09-25-assistant-turn-collapse.md`

**Interfaces:**

- E2E coverage uses the existing mixed-content fixture and the existing hidden-
  block search fixture. No new backend fixture or package dependency is needed.

- [ ] **Step 1: Update the mixed-content transcript workflow test**

  Add the test
  `assistant turn starts collapsed and final output remains visible`.
  In `message-content.spec.ts`, update `MIXED_CONTENT_DISPLAY_ROWS` from 6 to 7
  for the three prompt rows plus two turn-header/final-output pairs. Assert both
  assistant turns start collapsed with their final output visible. Expand the
  second turn before inspecting its tool rollup; expand the rollup before
  interacting with nested `ToolBlock`s. Update every thinking/tool-content test,
  including retained-image tests, to open the owning turn and tool rollup first.
  Keep the assertion that final text stays visible while thinking is collapsed.

- [ ] **Step 2: Add a filtered-search end-to-end test**

  Add the test `hidden-type search reveals matches without persisting filters`.
  In `session-find.spec.ts`, set the thinking and tool filters hidden in local
  storage, search for fixture needles, and verify hidden-type matches remain
  indexed and their rows become visible. Verify `ui.visibleBlocks` storage is
  unchanged by search, manually hide one revealed type and verify the manual
  choice wins, then clear search and confirm temporary reveals disappear while
  the searched turn remains expanded. Preserve `PROSE_MATCHES` only for focused
  mode; a normal-mode block filter must not reduce the search index.

- [ ] **Step 3: Run focused Playwright tests**

  Run from `frontend/`:

  ```bash
  npm run e2e -- \
    --grep "assistant turn starts collapsed|hidden-type search reveals matches"
  ```

  Expected: both new workflows pass against the normal test server and fixture.

- [ ] **Step 4: Run frontend and documentation verification**

  First run `./node_modules/.bin/vp check` and record its output. The current
  baseline reports 21 unrelated formatting files; do not edit them. Then run:

  ```bash
  npm run i18n:compile
  ./node_modules/.bin/vp check --no-fmt
  npm run check
  ./node_modules/.bin/vp test run
  build_dir=$(mktemp -d "${TMPDIR:-/tmp}/agentsview-turn-collapse.XXXXXX")
  ./node_modules/.bin/vp build --outDir "$build_dir" --emptyOutDir
  make docs-check
  git diff --check
  ```

  Expected: i18n compilation, repository-wide lint/type checks, Svelte checking,
  full frontend unit tests, temporary-directory production build, and docs
  validation pass. The full formatter check may still report the 21 unrelated
  files observed before implementation; do not modify them for this feature.

- [ ] **Step 5: Review and complete the plan**

  Mark completed checkboxes only after their command succeeds. Review `git diff`
  and `git status --short`; confirm only approved frontend, locale, docs, and
  test files changed. Do not stage generated Paraglide output unless the repo
  tracks it as part of the established workflow.

- [ ] **Step 6: Commit the final verification/test changes**

  ```bash
  git add \
    frontend/e2e/message-content.spec.ts \
    frontend/e2e/session-find.spec.ts \
    docs/superpowers/plans/2026-09-25-assistant-turn-collapse.md
  git commit -m "test(frontend): verify assistant turn collapse workflow"
  ```
