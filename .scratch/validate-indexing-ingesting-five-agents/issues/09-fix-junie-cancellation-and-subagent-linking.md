# Fix Junie cancellation event handling and subagent link collision

Type: task
Status: open
Blocked by: 

## Question

How should AgentsView update `internal/parser/junie.go` to:
1. Handle top-level `CancelAgentEvent` and `AgentTaskFailedEvent` in `junieParseFile` so cancelled tasks are marked accurately rather than misclassified as `awaiting_user` or `clean`?
2. Link parent subagent tool calls to child sessions via `stepId` prefix rather than agent name matching, preventing link collisions when multiple subagents share the same name?
3. Preserve question text in `UserAsyncResponseEvent` and ignore self-referential child `CustomAgentBlockUpdatedEvent`?
