# Assistant Turn Collapse Design

## Goal

Make long session transcripts easier to scan by collapsing assistant turns and
intermediate events by default, while keeping user prompts and final assistant
answers accessible.

## Approach

Build assistant turns in the shared frontend display projection used by the
transcript and in-session search. Keep the API and database message model flat.
Apply grouping in normal transcript mode across all message layouts (`default`,
`compact`, `stream`, and `skim`); keep focused transcript mode unchanged. The
skim layout retains its current detail suppression within expanded turns.

A view-only grouping inside `MessageList` was rejected because search would need
a separate mapping from flat messages to rendered groups. Backend grouping was
rejected because it would change the API and duplicate display-only
responsibility in the server.

## Turn Grouping and Data Flow

- Keep each real user prompt as a separate transcript item. Classify compact
  boundaries and system-boundary subtypes before identifying prompts:
  `CompactBoundaryDivider` and messages recognized by `isSystemBoundaryMessage`
  are never prompts. Other system-only messages recognized by
  `isSystemMessage` are also not prompts. A real prompt is an unclassified
  message with `role === "user"`.
- Build ordered child event rows from assistant content. Thinking segments,
  visible message-text segments, tool calls, and skill events from one database
  message are separate rows. Keep code blocks inside their text-message row and
  preserve structured tool-call associations.
- Group assistant events after each real user prompt until the next real user
  prompt. Consecutive tool-only messages keep their existing tool-group summary
  inside the assistant turn.
- Keep mid-turn `task_notification` and `stop_hook` events as separate child
  rows; they do not split a turn. Keep `continuation`, `resume`, and
  `interrupted` cards standalone and let them end the current group. Keep
  `CompactBoundaryDivider` standalone and let it end the current group. Do not
  add a fork transcript marker; fork remains session metadata. Include sidechain
  items in their parent assistant turn. Assistant items before the first real
  user prompt form a standalone assistant group.
- Use the numeric `DbMessage.id` of the first assistant message, scoped to its
  session, as the stable turn key. If loading older pages extends a partial
  leading group, migrate its existing manual state to the new key based on the
  earliest assistant message.
- Key a child event by its source `DbMessage.id`, event type, and ordered segment
  or tool-call index. Key a tool-only rollup by the `DbMessage.id` of its first
  tool message.
- Preserve a single projection for transcript rendering and search membership.
  Apply block visibility to child items so a search match can temporarily
  reveal a hidden type without changing group membership. Search overrides must
  not persist changes to block-visibility preferences. Bulk state must also
  cover child items hidden by filters.
- Flatten each assistant turn into a header row, child rows when expanded, and a
  separate final-output row when one exists. Keep the final-output row visible
  when the turn is collapsed. Use the existing virtualizer for all rows and
  preserve ordering and ordinal navigation. In newest-first mode, reverse group
  and child-event order while keeping each group header first; place its
  final-output row immediately after the header. `j`/`k` navigation remains one
  step per source message ordinal; turn headers are not stops. If a target message
  is inside a collapsed turn, expand the turn and reveal its child event rows.
- Compute displayed ordinals and the unread boundary from rendered, visible
  rows. A collapsed header does not mark hidden child messages read. The visible
  final-output row contributes its source message ordinal to read progress.
  Omit an assistant-turn header when filters hide every child and there is no
  visible final output; a temporary search visibility override may make the
  group visible again.

## Collapse State and Controls

- Add a global auto-collapse preference to the existing `AppearanceSettings`
  page and persist it in the `ui` store using local storage. Default it to
  enabled. It controls the default state of assistant turns and their child
  event rows in normal transcript mode; disabling it defaults both levels to
  expanded. Focused mode remains unchanged and does not show the turn-level bulk
  controls.
- Keep manual expansion state in memory for the active session only. Reset it
  on page reload or session switch. Changing the global preference changes
  defaults but does not erase explicit manual choices.
- Add `Expand all` / `Collapse all` controls to the transcript control strip in
  `AppHeader`. They affect all assistant turns and their child rows in the full
  session, including unloaded pages and items hidden by filters. They also
  expand or collapse nested tool output and history drawers. They do not affect
  user prompt previews or standalone structural cards.
- Represent a bulk action as a session-scoped state for all pages. Individual
  manual changes after that action are per-group or per-item exceptions. The
  next bulk action replaces the prior bulk state and clears those exceptions.
  None of this session state persists across reloads or session switches.
- Nested tool output and history drawers remain independently collapsed when
  the auto-collapse preference is disabled. Error output drawers default open
  regardless of that preference. Explicit bulk and manual actions can still
  collapse them.

## Search and Streaming

- In-session search expands the matching assistant turn and child item. If the
  matching item type is hidden, temporarily make that type visible for the
  search without writing to the persistent block-visibility preference. Restore
  the user's prior visibility filters when search is cleared, but leave
  search-expanded groups and items open.
- A manual collapse or `Collapse all` can close a search-matched group or item
  while the search result is selected.
- A running assistant turn remains collapsed by default. The final output stays
  visible and updates during streaming. If the user manually opens the turn, it
  remains open; newly appended child rows use their normal collapse default.

## Transcript Presentation

- Render each collapsed child event as a compact one-line preview. Text and
  thinking previews are limited to about 80 Unicode code points; tool rows use
  a type-specific summary.
- Show the assistant model and message/tool-call counts in the turn header.
  Show timestamp, token usage, and context statistics when available.
- Select the final output as the last non-empty user-facing assistant text in
  the turn. Later tool calls and thinking do not replace it. If a turn has no
  such text, do not render a separate final-output row.
- Render the full final output in its separate row so it remains visible when
  the turn is collapsed. It follows the assistant visibility filter and can be
  temporarily revealed by search. Keep its child event row in the expanded turn
  as a collapsed preview; the full output and preview are both present when the
  turn is expanded.
- For user prompts longer than 600 Unicode code points, show the first 500
  Unicode code points and a `Show full prompt` control without splitting a
  surrogate pair. Attachments remain separate and follow the existing attachment
  and image visibility behavior.
- Reuse existing message, thinking, tool, and skill rendering for their promoted
  child event rows where practical. Do not add another nested collapse layer for
  an event already promoted to a row. Keep code blocks inside the message-text
  row and preserve their existing rendering.

## Accessibility and Localization

Use buttons with expanded state exposed to assistive technology for turn, row,
and bulk controls. Add all new user-facing strings through Paraglide messages;
keep every supported locale catalogue in sync.

## Test Strategy

Add focused frontend tests for:

- Group boundaries, stable turn and child-event keys, state migration when older
  pages extend a partial turn, ordered typed events from mixed assistant
  messages, initial assistant items, sidechains, mid-turn system events,
  continuation/resume/interrupted cards, and standalone compact boundaries.
- Message-prompt classification using `isSystemMessage`, including user-role
  system rows and content-prefix cases.
- Final-output selection when a turn has multiple text items, trailing tools or
  thinking, or no user-facing text.
- Default and manual collapse state, preference changes, bulk state across
  unloaded pages, hidden child items, nested tool outputs, error defaults, and
  session resets.
- Search reveal, temporary non-persistent visibility overrides, manual collapse
  during search, filter restoration, and retained expansion after clearing
  search.
- Streaming updates, user prompt truncation, independent attachments, flattened
  virtual rows, keyboard navigation by message ordinal, empty filtered groups,
  and read progress that ignores hidden child ordinals.
- Localized labels and accessible expanded-state controls.

Run the focused frontend tests and checks, then the broader frontend test,
format, lint, and type-check commands when practical.
