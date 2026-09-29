# Fix Junie cancellation event handling and subagent link collision

Type: task
Status: resolved
Blocked by: 

## Question

How should AgentsView update `internal/parser/junie.go` to:
1. Handle top-level `CancelAgentEvent` and `AgentTaskFailedEvent` in `junieParseFile` so cancelled tasks are marked accurately rather than misclassified as `awaiting_user` or `clean`?
2. Link parent subagent tool calls to child sessions via `stepId` prefix rather than agent name matching, preventing link collisions when multiple subagents share the same name?
3. Preserve question text in `UserAsyncResponseEvent` and ignore self-referential child `CustomAgentBlockUpdatedEvent`?

## Answer

1. **Cancellation and Failure Handling**: `junie.go` now handles top-level `CancelAgentEvent`, `AgentTaskFailedEvent`, and `AgentFailureEvent` across both root and subagent event streams. Subagent streams track failure notes in `stream.notes`, and `junieFinishSubagent` classifies terminated sessions with `TerminationTruncated`.
2. **Subagent Link Collision Resolution**: Implemented a 3-tier subagent matching algorithm: (1) exact `stepID` match, (2) delimited prefix match (separated by `-` or `_` to prevent `step-1` colliding with `step-10`), and (3) fallback agent name match. Child `CustomAgentBlockUpdatedEvent` entries on the child stream are recognized and no longer synthesized as unlinked task tool calls.
3. **Async Prompt Preservation**: `UserAsyncResponseEvent` now extracts both `question` and `answer`, formatting them into the user prompt turn so question context is not discarded, and populates `FirstMessage` when empty.
