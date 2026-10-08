package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToolCallResultSummaryStorage pins both halves of the tool-result
// dedup: what the archive stores for a call, and what a loaded call looks
// like to every consumer. The loaded ResultContent must match what the
// column held back when the summary was written twice.
func TestToolCallResultSummaryStorage(t *testing.T) {
	tests := []struct {
		name          string
		call          ToolCall
		wantStored    string
		wantStoredLen int
		wantLoaded    string
		wantEvents    int
	}{
		{
			name: "single event summary is stored directly",
			call: ToolCall{
				ToolName:            "Bash",
				Category:            "Bash",
				ToolUseID:           "call_one",
				ResultContent:       "total 4\ndrwxr-xr-x",
				ResultContentLength: len("total 4\ndrwxr-xr-x"),
				ResultEvents: []ToolResultEvent{{
					ToolUseID:     "call_one",
					Source:        "function_call_output",
					Status:        "completed",
					Content:       "total 4\ndrwxr-xr-x",
					ContentLength: len("total 4\ndrwxr-xr-x"),
				}},
			},
			wantStored:    "total 4\ndrwxr-xr-x",
			wantStoredLen: len("total 4\ndrwxr-xr-x"),
			wantLoaded:    "total 4\ndrwxr-xr-x",
			wantEvents:    0,
		},
		{
			name: "timed event summary is stored with its events",
			call: ToolCall{
				ToolName: "Bash", Category: "Bash", ToolUseID: "call_timed",
				ResultContent: "output", ResultContentLength: len("output"),
				ResultEvents: []ToolResultEvent{
					{ToolUseID: "call_timed", Source: "tool_execution", Status: "started"},
					{ToolUseID: "call_timed", Source: "tool_execution", Status: "completed", Content: "output", ContentLength: len("output")},
				},
			},
			wantStored:    "output",
			wantStoredLen: len("output"),
			wantLoaded:    "output",
			wantEvents:    2,
		},
		{
			name: "multi event summary is stored",
			call: ToolCall{
				ToolName:            "Task",
				Category:            "Task",
				ToolUseID:           "call_many",
				ResultContent:       "agent-1:\nfirst\n\nagent-2:\nsecond",
				ResultContentLength: len("agent-1:\nfirst\n\nagent-2:\nsecond"),
				ResultEvents: []ToolResultEvent{
					{
						ToolUseID:     "call_many",
						AgentID:       "agent-1",
						Source:        "subagent_notification",
						Status:        "completed",
						Content:       "first",
						ContentLength: len("first"),
						EventIndex:    0,
					},
					{
						ToolUseID:     "call_many",
						AgentID:       "agent-2",
						Source:        "subagent_notification",
						Status:        "completed",
						Content:       "second",
						ContentLength: len("second"),
						EventIndex:    1,
					},
				},
			},
			wantStored:    "agent-1:\nfirst\n\nagent-2:\nsecond",
			wantStoredLen: len("agent-1:\nfirst\n\nagent-2:\nsecond"),
			wantLoaded:    "agent-1:\nfirst\n\nagent-2:\nsecond",
			wantEvents:    2,
		},
		{
			name: "single event summary that differs is stored",
			call: ToolCall{
				ToolName:            "Task",
				Category:            "Task",
				ToolUseID:           "call_diff",
				ResultContent:       "agent-1:\nonly",
				ResultContentLength: len("agent-1:\nonly"),
				ResultEvents: []ToolResultEvent{{
					ToolUseID:     "call_diff",
					AgentID:       "agent-1",
					Source:        "subagent_notification",
					Status:        "completed",
					Content:       "only",
					ContentLength: len("only"),
				}},
			},
			wantStored:    "agent-1:\nonly",
			wantStoredLen: len("agent-1:\nonly"),
			wantLoaded:    "agent-1:\nonly",
			wantEvents:    1,
		},
		{
			name: "blocked category keeps its blanked shape",
			call: ToolCall{
				ToolName:            "Read",
				Category:            "Read",
				ToolUseID:           "call_blocked",
				ResultContent:       "",
				ResultContentLength: 4096,
				ResultEvents: []ToolResultEvent{{
					ToolUseID:     "call_blocked",
					Source:        "function_call_output",
					Status:        "completed",
					Content:       "",
					ContentLength: 4096,
				}},
			},
			wantStored:    "",
			wantStoredLen: 4096,
			wantLoaded:    "",
			wantEvents:    1,
		},
		{
			name: "empty summary over a blank event stays empty",
			call: ToolCall{
				ToolName:            "Bash",
				Category:            "Bash",
				ToolUseID:           "call_blank",
				ResultContent:       "",
				ResultContentLength: 0,
				ResultEvents: []ToolResultEvent{{
					ToolUseID:     "call_blank",
					Source:        "function_call_output",
					Status:        "completed",
					Content:       "   ",
					ContentLength: 3,
				}},
			},
			wantStored:    "",
			wantStoredLen: 0,
			wantLoaded:    "",
			wantEvents:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := testDB(t)
			insertSession(t, d, "s-dedup", "proj")
			call := tt.call
			call.SessionID = "s-dedup"
			require.NoError(t, d.InsertMessages(t.Context(), []Message{{
				SessionID:  "s-dedup",
				Ordinal:    0,
				Role:       "assistant",
				Content:    "running a tool",
				HasToolUse: true,
				ToolCalls:  []ToolCall{call},
			}}))

			var stored string
			var storedLen int
			require.NoError(t, d.Reader().QueryRow(t.Context(), `
				SELECT COALESCE(result_content, ''),
				       COALESCE(result_content_length, 0)
				FROM tool_calls
				WHERE session_id = ? AND tool_use_id = ?`,
				"s-dedup", call.ToolUseID,
			).Scan(&stored, &storedLen))
			assert.Equal(t, tt.wantStored, stored, "stored result_content")
			assert.Equal(t, tt.wantStoredLen, storedLen,
				"stored result_content_length")

			msgs, err := d.GetMessages(
				t.Context(), "s-dedup", 0, 10, true,
			)
			require.NoError(t, err)
			require.Len(t, msgs, 1)
			require.Len(t, msgs[0].ToolCalls, 1)
			assert.Equal(t, tt.wantLoaded,
				msgs[0].ToolCalls[0].ResultContent,
				"loaded ToolCall.ResultContent")
			assert.Len(t, msgs[0].ToolCalls[0].ResultEvents,
				tt.wantEvents, "result events count")
			tx, err := d.getWriter().Begin(t.Context())
			require.NoError(t, err)
			defer func() { require.NoError(t, tx.Rollback()) }()
			facts, err := (signalTxQuery{tx: tx, sessionID: "s-dedup"}).TrailingToolCalls(t.Context(), 1)
			require.NoError(t, err)
			require.Len(t, facts, 1)
			assert.Equal(t, tt.wantLoaded, facts[0].ResultContent, "signal SQL result content")
		})
	}
}

// TestSearchSessionFindsDedupedResultContent covers the in-session find bar,
// which reads the column in SQL instead of loading tool calls.
func TestSearchSessionFindsDedupedResultContent(t *testing.T) {
	d := testDB(t)
	insertSession(t, d, "s-find", "proj")
	require.NoError(t, d.InsertMessages(t.Context(), []Message{{
		SessionID:  "s-find",
		Ordinal:    0,
		Role:       "assistant",
		Content:    "running a tool",
		HasToolUse: true,
		ToolCalls: []ToolCall{{
			SessionID:           "s-find",
			ToolName:            "Bash",
			Category:            "Bash",
			ToolUseID:           "call_find",
			ResultContent:       "needle in the output",
			ResultContentLength: len("needle in the output"),
			ResultEvents: []ToolResultEvent{
				{ToolUseID: "call_find", Source: "tool_execution", Status: "started"},
				{
					ToolUseID:     "call_find",
					Source:        "tool_execution",
					Status:        "completed",
					Content:       "needle in the output",
					ContentLength: len("needle in the output"),
				},
			},
		}},
	}}))

	ordinals, err := d.SearchSession(t.Context(), "s-find", "needle")
	require.NoError(t, err)
	assert.Equal(t, []int{0}, ordinals)
}

func TestRestoreToolCallResultContent(t *testing.T) {
	tests := []struct {
		name string
		call ToolCall
		want string
	}{
		{
			name: "stored summary wins",
			call: ToolCall{
				ResultContent:       "summary",
				ResultContentLength: 7,
				ResultEvents:        []ToolResultEvent{{Content: "event"}},
			},
			want: "summary",
		},
		{
			name: "single event refills a cleared summary",
			call: ToolCall{
				ResultContentLength: 5,
				ResultEvents:        []ToolResultEvent{{Content: "event"}},
			},
			want: "event",
		},
		{
			name: "timed event refills a cleared summary",
			call: ToolCall{
				ResultContentLength: 5,
				ResultEvents:        []ToolResultEvent{{Status: "started"}, {Status: "completed", Content: "event"}},
			},
			want: "event",
		},
		{
			name: "zero length is a genuinely empty summary",
			call: ToolCall{
				ResultEvents: []ToolResultEvent{{Content: "  "}},
			},
			want: "",
		},
		{
			name: "multiple events never refill",
			call: ToolCall{
				ResultContentLength: 5,
				ResultEvents: []ToolResultEvent{
					{Content: "a"}, {Content: "b"},
				},
			},
			want: "",
		},
		{
			name: "no events never refill",
			call: ToolCall{ResultContentLength: 5},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			call := tt.call
			RestoreToolCallResultContent(&call)
			assert.Equal(t, tt.want, call.ResultContent)
		})
	}
}

func TestSubagentLinkResultEventInheritsChild(t *testing.T) {
	for _, tt := range []struct {
		name, storedChild, linkedChild, eventChild, want string
	}{
		{"new link", "", "imported:agent-child", "", "imported:agent-child"},
		{"existing link", "imported:agent-child", "", "", "imported:agent-child"},
		{"explicit event link", "imported:agent-child", "", "imported:agent-other", "imported:agent-other"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := testDB(t)
			insertSession(t, d, "parent", "project")
			require.NoError(t, d.InsertMessages(t.Context(), []Message{{
				SessionID: "parent", Ordinal: 0, Role: "assistant",
				ToolCalls: []ToolCall{{
					ToolUseID: "call-1", ToolName: "Agent", Category: "Task",
					SubagentSessionID: tt.storedChild,
				}},
			}}))
			_, err := d.WriteSessionIncremental(t.Context(), "parent", nil, IncrementalSessionUpdate{
				MsgCount: 1, NextOrdinal: 1,
				SubagentLinks: []ToolCallSubagentLink{{
					ToolUseID: "call-1", SubagentSessionID: tt.linkedChild, HasResult: true,
					ResultEvents: []ToolResultEvent{{
						Source: "tool_result", Content: "done", SubagentSessionID: tt.eventChild,
					}},
				}},
			})
			require.NoError(t, err)
			messages, err := d.GetAllMessages(t.Context(), "parent")
			require.NoError(t, err)
			require.Len(t, messages, 1)
			require.Len(t, messages[0].ToolCalls, 1)
			call := messages[0].ToolCalls[0]
			assert.Equal(t, "imported:agent-child", call.SubagentSessionID)
			require.Len(t, call.ResultEvents, 1)
			assert.Equal(t, tt.want, call.ResultEvents[0].SubagentSessionID)
		})
	}
}

// TestSubagentLinkKeepsDedupedSummary pins the incremental link path: a
// linked result that repeats the call's single content-bearing event must
// not re-inflate result_content, while a summary the event does not carry
// is stored as before.
func TestSubagentLinkKeepsDedupedSummary(t *testing.T) {
	d := testDB(t)
	insertSession(t, d, "s-link", "proj")
	require.NoError(t, d.InsertMessages(t.Context(), []Message{{
		SessionID:  "s-link",
		Ordinal:    0,
		Role:       "assistant",
		Content:    "spawning an agent",
		HasToolUse: true,
		ToolCalls: []ToolCall{{
			SessionID:           "s-link",
			ToolName:            "Agent",
			Category:            "Task",
			ToolUseID:           "call_link",
			ResultContent:       "agent finished",
			ResultContentLength: len("agent finished"),
			ResultEvents: []ToolResultEvent{
				{Source: "tool_execution", Status: "started"},
				{Source: "subagent_notification", Status: "completed", Content: "agent finished", ContentLength: len("agent finished")},
			},
		}},
	}}))

	stored := func() (string, int) {
		var content string
		var length int
		require.NoError(t, d.Reader().QueryRow(t.Context(), `
			SELECT COALESCE(result_content, ''),
			       COALESCE(result_content_length, 0)
			FROM tool_calls WHERE session_id = ? AND tool_use_id = ?`,
			"s-link", "call_link",
		).Scan(&content, &length))
		return content, length
	}

	content, length := stored()
	require.Equal(t, "agent finished", content, "insert must store the single-event summary directly")
	require.Equal(t, len("agent finished"), length)

	link := func(result string) {
		_, err := d.WriteSessionIncremental(t.Context(), "s-link", nil,
			IncrementalSessionUpdate{
				MsgCount:    1,
				NextOrdinal: 1,
				SubagentLinks: []ToolCallSubagentLink{{
					ToolUseID:         "call_link",
					SubagentSessionID: "agent-child",
					ResultContent:     result,
					ResultContentLen:  len(result),
					HasResult:         true,
				}},
			})
		require.NoError(t, err)
	}

	link("agent finished")
	content, length = stored()
	assert.Equal(t, "agent finished", content, "link repeating the event stays stored directly")
	assert.Equal(t, len("agent finished"), length)

	msgs, err := d.GetMessages(t.Context(), "s-link", 0, 10, true)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Len(t, msgs[0].ToolCalls, 1)
	assert.Equal(t, "agent finished", msgs[0].ToolCalls[0].ResultContent)
	assert.Equal(t, "agent-child", msgs[0].ToolCalls[0].SubagentSessionID)
	require.Len(t, msgs[0].ToolCalls[0].ResultEvents, 2,
		"the started sibling carries execution timing the collapse would lose")
	assert.Equal(t, "started", msgs[0].ToolCalls[0].ResultEvents[0].Status)
	assert.Equal(t, "completed", msgs[0].ToolCalls[0].ResultEvents[1].Status)

	link("agent finished with a longer final report")
	content, length = stored()
	assert.Equal(t, "agent finished with a longer final report", content,
		"a summary the event does not carry is stored")
	assert.Equal(t, len("agent finished with a longer final report"), length)
}

// TestOmittedResultContentLengthRoundTrips pins the public write API: the
// stored length of a non-empty summary or event is always the text's
// length, whatever the caller supplied, and a supplied length survives
// only for withheld text. The length is what tells the loader to refill a
// summary the dedup dropped, so a caller cannot break the round trip.
func TestOmittedResultContentLengthRoundTrips(t *testing.T) {
	const summary = "output without a declared length"
	call := func(id string) ToolCall {
		return ToolCall{
			ToolName:      "Bash",
			Category:      "Bash",
			ToolUseID:     id,
			ResultContent: summary,
			ResultEvents: []ToolResultEvent{{
				Source:  "function_call_output",
				Status:  "completed",
				Content: summary,
			}},
		}
	}
	// loaded reads the single call back and requires exactly wantEvents
	// result events, so the event-length check below can never pass by
	// looping over nothing.
	loaded := func(
		t *testing.T, d *DB, sessionID string, wantEvents int,
	) ToolCall {
		t.Helper()

		msgs, err := d.GetMessages(t.Context(), sessionID, 0, 10, true)
		require.NoError(t, err)
		require.Len(t, msgs, 1)
		require.Len(t, msgs[0].ToolCalls, 1)
		tc := msgs[0].ToolCalls[0]
		require.Len(t, tc.ResultEvents, wantEvents)
		for _, ev := range tc.ResultEvents {
			assert.Equal(t, len(ev.Content), ev.ContentLength,
				"event length is inferred when omitted")
		}
		return tc
	}

	t.Run("InsertMessages", func(t *testing.T) {
		d := testDB(t)
		insertSession(t, d, "s-nolen", "proj")
		require.NoError(t, d.InsertMessages(t.Context(), []Message{{
			SessionID: "s-nolen", Ordinal: 0, Role: "assistant",
			Content: "running", HasToolUse: true,
			ToolCalls: []ToolCall{call("call_nolen")},
		}}))
		got := loaded(t, d, "s-nolen", 0)
		assert.Equal(t, summary, got.ResultContent)
		assert.Equal(t, len(summary), got.ResultContentLength)
	})

	t.Run("WriteSessionBatch", func(t *testing.T) {
		d := testDB(t)
		_, err := d.WriteSessionBatch([]SessionBatchWrite{{
			Session: Session{
				ID: "s-batch-nolen", Project: "proj",
				Machine: defaultMachine, Agent: defaultAgent,
				MessageCount: 1,
			},
			Messages: []Message{{
				SessionID: "s-batch-nolen", Ordinal: 0, Role: "assistant",
				Content: "running", HasToolUse: true,
				ToolCalls: []ToolCall{call("call_batch_nolen")},
			}},
			ReplaceMessages: true,
		}})
		require.NoError(t, err)
		got := loaded(t, d, "s-batch-nolen", 0)
		assert.Equal(t, summary, got.ResultContent)
		assert.Equal(t, len(summary), got.ResultContentLength)
	})

	t.Run("wrong length is corrected", func(t *testing.T) {
		d := testDB(t)
		insertSession(t, d, "s-wrong", "proj")
		wrong := call("call_wrong")
		wrong.ResultContentLength = len(summary) - 1
		wrong.ResultEvents[0].ContentLength = len(summary) + 7
		require.NoError(t, d.InsertMessages(t.Context(), []Message{{
			SessionID: "s-wrong", Ordinal: 0, Role: "assistant",
			Content: "running", HasToolUse: true,
			ToolCalls: []ToolCall{wrong},
		}}))
		got := loaded(t, d, "s-wrong", 0)
		assert.Equal(t, summary, got.ResultContent)
		assert.Equal(t, len(summary), got.ResultContentLength)
	})

	t.Run("withheld text keeps the supplied length", func(t *testing.T) {
		d := testDB(t)
		insertSession(t, d, "s-withheld", "proj")
		require.NoError(t, d.InsertMessages(t.Context(), []Message{{
			SessionID: "s-withheld", Ordinal: 0, Role: "assistant",
			Content: "running", HasToolUse: true,
			ToolCalls: []ToolCall{{
				ToolName: "Read", Category: "Read", ToolUseID: "call_withheld",
				ResultContentLength: 4096,
			}},
		}}))
		got := loaded(t, d, "s-withheld", 0)
		assert.Empty(t, got.ResultContent)
		assert.Equal(t, 4096, got.ResultContentLength)
	})

	t.Run("WriteSessionIncremental link", func(t *testing.T) {
		d := testDB(t)
		insertSession(t, d, "s-link-nolen", "proj")
		require.NoError(t, d.InsertMessages(t.Context(), []Message{{
			SessionID: "s-link-nolen", Ordinal: 0, Role: "assistant",
			Content: "spawning", HasToolUse: true,
			ToolCalls: []ToolCall{{
				ToolName: "Agent", Category: "Task", ToolUseID: "call_link_nolen",
			}},
		}}))
		_, err := d.WriteSessionIncremental(t.Context(), "s-link-nolen", nil,
			IncrementalSessionUpdate{
				MsgCount: 1, NextOrdinal: 1,
				SubagentLinks: []ToolCallSubagentLink{{
					ToolUseID:         "call_link_nolen",
					SubagentSessionID: "agent-child",
					ResultContent:     summary,
					HasResult:         true,
				}},
			})
		require.NoError(t, err)
		got := loaded(t, d, "s-link-nolen", 0)
		assert.Equal(t, summary, got.ResultContent)
		assert.Equal(t, len(summary), got.ResultContentLength)
	})
}

// TestResultContentDuplicatesSingleEventStatusGate pins the status side of
// the collapse contract. A collapsed call reads as an implicitly completed
// result, so only an empty or "completed" status is equivalent to the
// summary. Every other status carries information the summary cannot
// express — the failure verdict signals read, the terminal marker activity
// reports and execution timing read — and must keep its event row.
func TestResultContentDuplicatesSingleEventStatusGate(t *testing.T) {
	const summary = "output"
	for _, tc := range []struct {
		name   string
		status string
		want   bool
	}{
		{"empty status collapses", "", true},
		{"completed collapses", "completed", true},
		{"errored keeps its event", "errored", false},
		{"cancelled keeps its event", "cancelled", false},
		{"denied keeps its event", "denied", false},
		{"error keeps its event", "error", false},
		{"failed keeps its event", "failed", false},
		{"failure keeps its event", "failure", false},
		{"started keeps its event", "started", false},
		{"running keeps its event", "running", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := []ToolResultEvent{{
				Source:        "function_call_output",
				Status:        tc.status,
				Content:       summary,
				ContentLength: len(summary),
				Timestamp:     "2024-01-01T10:00:00Z",
			}}
			assert.Equal(t, tc.want,
				ResultContentDuplicatesSingleEvent(summary, events),
				"duplicate verdict")
			assert.Equal(t, !tc.want,
				ShouldPersistToolResultEvents(summary, events),
				"persist verdict")
		})
	}
}

// TestToolCallResultStorageKeepsFailureMetadata covers the storage half of
// the status gate end to end: a single errored event whose content equals
// the summary must keep its row in tool_result_events, with the status,
// source, and timestamp a downstream consumer reads.
func TestToolCallResultStorageKeepsFailureMetadata(t *testing.T) {
	d := testDB(t)
	insertSession(t, d, "s-err", "proj")
	require.NoError(t, d.InsertMessages(t.Context(), []Message{{
		SessionID: "s-err", Ordinal: 0, Role: "assistant",
		Content: "running a tool", HasToolUse: true,
		ToolCalls: []ToolCall{{
			SessionID:           "s-err",
			ToolName:            "Bash",
			Category:            "Bash",
			ToolUseID:           "call_err",
			ResultContent:       "boom",
			ResultContentLength: len("boom"),
			ResultEvents: []ToolResultEvent{{
				Source:        "function_call_output",
				Status:        "errored",
				Content:       "boom",
				ContentLength: len("boom"),
				Timestamp:     "2024-01-01T10:00:00Z",
			}},
		}},
	}}))

	msgs, err := d.GetMessages(t.Context(), "s-err", 0, 10, true)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Len(t, msgs[0].ToolCalls, 1)
	call := msgs[0].ToolCalls[0]
	assert.Equal(t, "boom", call.ResultContent)
	require.Len(t, call.ResultEvents, 1,
		"an errored event must not collapse into result_content")
	assert.Equal(t, "errored", call.ResultEvents[0].Status)
	assert.Equal(t, "function_call_output", call.ResultEvents[0].Source)
	assert.Equal(t, "boom", call.ResultEvents[0].Content)
	assert.Equal(t, "2024-01-01T10:00:00Z", call.ResultEvents[0].Timestamp)
}

// captureSignalQuery is a SignalMaintainer stub that records which result
// events the write transaction reports as inserted for the call under test
// and what the in-transaction stored-events view holds, then declines so
// the write falls back to signal invalidation.
type captureSignalQuery struct {
	inserted []ToolResultEvent
	stored   []ToolResultEvent
}

func (c *captureSignalQuery) MaintainTx(
	ctx context.Context, q SignalQuery,
) (*SignalDelta, error) {
	c.inserted = append(c.inserted, q.InsertedResultEvents(
		ToolCallPosition{MessageOrdinal: 0, CallIndex: 0},
	)...)
	stored, err := q.CallResultEvents(ctx, 0, 0)
	if err != nil {
		return nil, err
	}
	c.stored = stored
	return nil, nil
}

// TestLateErroredResultMaterializesCollapsedCall pins the late-update half
// of the collapse contract: when a call's single event was collapsed into
// result_content, a later update replaying the same bytes with a meaningful
// status must materialize event rows instead of being swallowed by the
// identical-summary early return. The stored rows are what signals, the
// activity report, and execution timing read.
func TestLateErroredResultMaterializesCollapsedCall(t *testing.T) {
	d := testDB(t)
	insertSession(t, d, "s-late", "proj")
	require.NoError(t, d.InsertMessages(t.Context(), []Message{{
		SessionID: "s-late", Ordinal: 0, Role: "assistant",
		Content: "running", HasToolUse: true,
		ToolCalls: []ToolCall{{
			SessionID:           "s-late",
			ToolName:            "Bash",
			Category:            "Bash",
			ToolUseID:           "call_late",
			ResultContent:       "done",
			ResultContentLength: len("done"),
			ResultEvents: []ToolResultEvent{{
				Source:        "function_call_output",
				Status:        "completed",
				Content:       "done",
				ContentLength: len("done"),
			}},
		}},
	}}))

	msgs, err := d.GetMessages(t.Context(), "s-late", 0, 10, true)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Len(t, msgs[0].ToolCalls, 1)
	require.Empty(t, msgs[0].ToolCalls[0].ResultEvents,
		"the completed single event must collapse first")

	// The signal maintainer runs inside the write transaction; a capture
	// stub proves the materialized rows — including the implicit completed
	// event the summary is restored as — reach the findings scanner.
	capture := &captureSignalQuery{}
	_, err = d.WriteSessionIncremental(t.Context(), "s-late", nil,
		IncrementalSessionUpdate{
			MsgCount:    1,
			NextOrdinal: 1,
			ToolCallResultUpdates: []ToolCallResultUpdate{{
				ToolUseID: "call_late",
				Position:  ToolCallPosition{MessageOrdinal: 0, CallIndex: 0},
				Events: []ToolResultEvent{{
					ToolUseID:     "call_late",
					Source:        "function_call_output",
					Status:        "errored",
					Content:       "done",
					ContentLength: len("done"),
					Timestamp:     "2024-01-01T10:05:00Z",
				}},
			}},
			SignalMaintainer: capture,
		})
	require.NoError(t, err)

	require.Len(t, capture.inserted, 2,
		"both materialized events must be visible to the findings scanner")
	assert.Equal(t, "completed", capture.inserted[0].Status,
		"the synthesized summary event must be scanned for findings")
	assert.Equal(t, 0, capture.inserted[0].EventIndex)
	assert.Equal(t, "errored", capture.inserted[1].Status)
	assert.Equal(t, 1, capture.inserted[1].EventIndex)
	require.Len(t, capture.stored, 2,
		"the canonical stored-events view must see both rows")

	msgs, err = d.GetMessages(t.Context(), "s-late", 0, 10, true)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Len(t, msgs[0].ToolCalls, 1)
	call := msgs[0].ToolCalls[0]
	assert.Equal(t, "done", call.ResultContent)
	require.Len(t, call.ResultEvents, 2,
		"a late errored status must materialize event rows")
	assert.Equal(t, "completed", call.ResultEvents[0].Status,
		"the collapsed summary materializes as the implicit completed event")
	assert.Equal(t, 0, call.ResultEvents[0].EventIndex)
	assert.Equal(t, "errored", call.ResultEvents[1].Status,
		"the late status must reach storage")
	assert.Equal(t, 1, call.ResultEvents[1].EventIndex)
	assert.Equal(t, "done", call.ResultEvents[1].Content)
	assert.Equal(t, "2024-01-01T10:05:00Z", call.ResultEvents[1].Timestamp)
}

// TestLateCompletedResultStaysCollapsed pins the opposite half: replaying
// the same bytes with a collapsible status is still a no-op — the call must
// not grow event rows on an idempotent late update.
func TestLateCompletedResultStaysCollapsed(t *testing.T) {
	d := testDB(t)
	insertSession(t, d, "s-idem", "proj")
	require.NoError(t, d.InsertMessages(t.Context(), []Message{{
		SessionID: "s-idem", Ordinal: 0, Role: "assistant",
		Content: "running", HasToolUse: true,
		ToolCalls: []ToolCall{{
			SessionID:           "s-idem",
			ToolName:            "Bash",
			Category:            "Bash",
			ToolUseID:           "call_idem",
			ResultContent:       "done",
			ResultContentLength: len("done"),
			ResultEvents: []ToolResultEvent{{
				Source:        "function_call_output",
				Status:        "completed",
				Content:       "done",
				ContentLength: len("done"),
			}},
		}},
	}}))

	for i := 0; i < 2; i++ {
		_, err := d.WriteSessionIncremental(t.Context(), "s-idem", nil,
			IncrementalSessionUpdate{
				MsgCount:    1,
				NextOrdinal: 1,
				ToolCallResultUpdates: []ToolCallResultUpdate{{
					ToolUseID: "call_idem",
					Position: ToolCallPosition{
						MessageOrdinal: 0, CallIndex: 0,
					},
					Events: []ToolResultEvent{{
						ToolUseID:     "call_idem",
						Source:        "function_call_output",
						Status:        "completed",
						Content:       "done",
						ContentLength: len("done"),
						Timestamp:     "2024-01-01T10:05:00Z",
					}},
				}},
			})
		require.NoError(t, err)
	}

	msgs, err := d.GetMessages(t.Context(), "s-idem", 0, 10, true)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Len(t, msgs[0].ToolCalls, 1)
	call := msgs[0].ToolCalls[0]
	assert.Equal(t, "done", call.ResultContent)
	assert.Empty(t, call.ResultEvents,
		"an identical completed replay must not grow event rows")
}

// TestToolContentIsErrorCoversFailureStatuses pins parity between the
// event-status failure vocabulary and the tool_content.is_error flag the
// has_error search filter reads: an errored (and, by extension, cancelled
// or denied) event must mark its tool_content row.
func TestToolContentIsErrorCoversFailureStatuses(t *testing.T) {
	d := testDB(t)
	if !d.HasFTS(t.Context()) {
		t.Skip("tool_content triggers require fts5")
	}
	insertSession(t, d, "s-fts", "proj")
	calls := []ToolCall{}
	for _, status := range []string{
		"errored", "cancelled", "denied", "error", "failed", "failure",
	} {
		calls = append(calls, ToolCall{
			SessionID:           "s-fts",
			ToolName:            "Bash",
			Category:            "Bash",
			ToolUseID:           "call_" + status,
			ResultContent:       "failed " + status,
			ResultContentLength: len("failed " + status),
			ResultEvents: []ToolResultEvent{{
				ToolUseID:     "call_" + status,
				Source:        "function_call_output",
				Status:        status,
				Content:       "failed " + status,
				ContentLength: len("failed " + status),
			}},
		})
	}
	require.NoError(t, d.InsertMessages(t.Context(), []Message{{
		SessionID:  "s-fts",
		Ordinal:    0,
		Role:       "assistant",
		Content:    "running tools",
		HasToolUse: true,
		ToolCalls:  calls,
	}}))

	rows, err := d.Reader().Query(t.Context(), `
		SELECT COALESCE(tre.status, ''), tc.is_error
		FROM tool_content tc
		JOIN tool_result_events tre ON tre.id = tc.source_id
		WHERE tc.source_kind = 'event'
		ORDER BY tre.id`)
	require.NoError(t, err)
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var status string
		var isError int
		require.NoError(t, rows.Scan(&status, &isError))
		got[status] = isError
	}
	require.NoError(t, rows.Err())
	require.Len(t, got, 6, "every failure-status event must keep its row")
	for _, status := range []string{
		"errored", "cancelled", "denied", "error", "failed", "failure",
	} {
		assert.Equal(t, 1, got[status],
			"status %q must flag tool_content.is_error", status)
	}
}
