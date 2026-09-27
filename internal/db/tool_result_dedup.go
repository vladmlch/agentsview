package db

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strings"
)

// Tool-result summaries used to be stored twice: once as
// tool_calls.result_content and once as the tool_result_events row the
// summary was derived from. On a real archive that duplication accounted for
// roughly 40% of the file, because the overwhelming majority of calls have a
// single result event and the summary is that event's content byte for byte.
//
// In the current model, single-event results are stored directly in
// tool_calls.result_content with 0 rows in tool_result_events when the event
// content matches the tool call's result summary. Genuine multi-event calls
// (events > 1 or event content differs from summary) persist rows in
// tool_result_events.
//
// For legacy pre-v123 data where result_content was cleared and a single event
// was stored in tool_result_events, RestoreToolCallResultContent and
// ToolCallResultContentSQL retain backward compatibility.

// ResultContentDuplicatesSingleEvent reports whether a summary repeats the
// content of the call's only result event verbatim without dropping subagent,
// multi-agent, or distinct raw provider identity.
func ResultContentDuplicatesSingleEvent(
	summary string, events []ToolResultEvent,
) bool {
	if summary == "" || len(events) != 1 ||
		events[0].Content != summary ||
		events[0].SubagentSessionID != "" ||
		events[0].AgentID != "" {
		return false
	}
	if len(events[0].RawContentDigest) > 0 {
		digest := sha256.Sum256([]byte(summary))
		if !bytes.Equal(events[0].RawContentDigest, digest[:]) {
			if !strings.Contains(summary, "agentsview_image") &&
				!strings.Contains(summary, "asset://") {
				return false
			}
		}
	}
	return true
}

// ShouldPersistToolResultEvents reports whether a call's result events must be
// persisted to tool_result_events. Single-event results whose content matches
// the call's result summary are stored directly on the tool call, leaving 0 rows
// in tool_result_events. Only multi-event calls or single events whose content
// diverges from the summary are persisted.
func ShouldPersistToolResultEvents(
	summary string, events []ToolResultEvent,
) bool {
	if ResultContentDuplicatesSingleEvent(summary, events) {
		return false
	}
	return len(events) > 0
}

// DedupToolCallResultSummary returns the result summary to persist for a call
// with the given result events: in the current storage model, the summary is
// always stored directly on the tool call.
func DedupToolCallResultSummary(
	summary string, events []ToolResultEvent,
) string {
	return summary
}

// RestoreToolCallResultContent refills the summary that the write path
// dropped, so every consumer of a loaded tool call sees the same
// ResultContent it saw when the summary was stored twice.
func RestoreToolCallResultContent(tc *ToolCall) {
	if tc.ResultContent != "" || tc.ResultContentLength == 0 ||
		len(tc.ResultEvents) != 1 {
		return
	}
	tc.ResultContent = tc.ResultEvents[0].Content
}

// RestoreMessageResultContent applies RestoreToolCallResultContent across a
// loaded message slice. Call it once the messages carry both their tool calls
// and their result events.
func RestoreMessageResultContent(msgs []Message) {
	for i := range msgs {
		for j := range msgs[i].ToolCalls {
			RestoreToolCallResultContent(&msgs[i].ToolCalls[j])
		}
	}
}

// ToolCallResultContentSQL builds the SQL expression that yields a tool
// call's display result content for readers that select the column directly
// instead of loading tool calls with their events. callAlias is the
// tool_calls alias and ordinalExpr resolves to the owning message's ordinal.
func ToolCallResultContentSQL(callAlias, ordinalExpr string) string {
	return fmt.Sprintf(`CASE
		WHEN COALESCE(%[1]s.result_content, '') <> ''
			THEN %[1]s.result_content
		WHEN COALESCE(%[1]s.result_content_length, 0) = 0 THEN ''
		ELSE COALESCE((
			SELECT CASE WHEN COUNT(*) = 1 THEN MIN(sole_rc.content) END
			FROM (
				SELECT tre_rc.content FROM tool_result_events tre_rc
				WHERE tre_rc.session_id = %[1]s.session_id
				  AND tre_rc.tool_call_message_ordinal = %[2]s
				  AND tre_rc.call_index = COALESCE(%[1]s.call_index, 0)
				LIMIT 2
			) sole_rc
		), '')
	END`, callAlias, ordinalExpr)
}
