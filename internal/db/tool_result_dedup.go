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
// content matches the tool call's result summary and the event's remaining
// metadata adds nothing the collapsed representation implies: a completed or
// absent status, no agent or subagent identity, and no sibling events.
// Genuine multi-event calls (events > 1 or event content differs from
// summary) persist rows in tool_result_events.
//
// The collapse consciously drops the event's timestamp, source, and
// event_index — the summary carries none of them — but never drops a
// meaningful status: an errored, cancelled, or denied result keeps its
// event row so failure detection, the activity report's terminal-event
// index, tool execution timing, and tool_content.is_error keep working.
//
// For legacy pre-v123 data where result_content was cleared and a single event
// was stored in tool_result_events, RestoreToolCallResultContent and
// ToolCallResultContentSQL retain backward compatibility.

// SoleToolResultContent ignores empty timing events when finding one payload.
func SoleToolResultContent(events []ToolResultEvent) (ToolResultEvent, bool) {
	var sole ToolResultEvent
	found := false
	for _, event := range events {
		if event.Content == "" {
			continue
		}
		if found {
			return ToolResultEvent{}, false
		}
		sole, found = event, true
	}
	return sole, found
}

// collapsibleToolResultStatus reports whether an event status carries no
// information beyond what the collapsed representation implies. A call with
// no event rows reads as an implicitly completed result, so only an empty or
// "completed" status is equivalent to the collapse. Every other status —
// terminal failures (errored, cancelled, denied), in-flight markers
// (started, running), and any status introduced later — must keep its event
// row: signals read the last stored event's status, the activity report and
// execution timing read stored rows, and tool_content.is_error is computed
// from them. Every collapse decision routes through
// ResultContentDuplicatesSingleEvent or this predicate directly, so the
// staged publishing path's checks stay aligned with the write path.
func collapsibleToolResultStatus(status string) bool {
	return status == "" || status == "completed"
}

// ResultContentDuplicatesSingleEvent reports whether a summary repeats the
// content of the call's only result event verbatim without dropping
// metadata a consumer reads: status, subagent, multi-agent, or distinct raw
// provider identity. Event timestamp, source, and event_index are
// consciously dropped by the collapse — the summary carries none of them.
func ResultContentDuplicatesSingleEvent(
	summary string, events []ToolResultEvent,
) bool {
	if summary == "" || len(events) != 1 ||
		events[0].Content != summary ||
		!collapsibleToolResultStatus(events[0].Status) ||
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
// the call's result summary and whose status is empty or "completed" are
// stored directly on the tool call, leaving 0 rows in tool_result_events.
// Multi-event calls, single events whose content diverges from the summary,
// and single events carrying a meaningful status (errored, cancelled,
// denied, started, ...) are persisted so downstream consumers keep seeing
// the metadata.
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
	if tc.ResultContent != "" || tc.ResultContentLength == 0 {
		return
	}
	if sole, ok := SoleToolResultContent(tc.ResultEvents); ok {
		tc.ResultContent = sole.Content
	}
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
				  AND COALESCE(tre_rc.content, '') <> ''
				LIMIT 2
			) sole_rc
		), '')
	END`, callAlias, ordinalExpr)
}
