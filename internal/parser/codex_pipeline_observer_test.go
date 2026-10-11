package parser

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/pipevents"
	"go.kenn.io/agentsview/internal/testjsonl"
)

// collectingPipelineObserver records every observed event in order. It
// implements pipevents.Observer for the parser observer tests.
type collectingPipelineObserver struct {
	mu     sync.Mutex
	events []pipevents.Event
}

func (c *collectingPipelineObserver) ObserveEvent(e pipevents.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *collectingPipelineObserver) all() []pipevents.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]pipevents.Event(nil), c.events...)
}

func (c *collectingPipelineObserver) ofKind(kind string) []pipevents.Event {
	var out []pipevents.Event
	for _, e := range c.all() {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func requirePipelineObserverProvider(
	t *testing.T,
	provider Provider,
) PipelineObserverProvider {
	t.Helper()
	observed, ok := provider.(PipelineObserverProvider)
	require.True(t, ok, "provider %T must implement PipelineObserverProvider", provider)
	return observed
}

func TestCodexPipelineObserverFullParseEmitsStructuredEvents(t *testing.T) {
	root := t.TempDir()
	const uuid = "019eb791-cf7d-75c1-8439-9ed74c1229f0"
	lines := []string{
		testjsonl.CodexSessionMetaJSON(
			uuid, "/workspace/project-a", "codex_cli_rs", tsEarly,
		),
		testjsonl.CodexTurnContextJSON("gpt-5.4", tsEarlyS1),
		testjsonl.CodexMsgJSON("user", "first request", tsEarlyS1),
		testjsonl.CodexMsgJSON("assistant", "first response", tsEarlyS5),
		testjsonl.CodexTokenCountJSON(tsLate, 1_000, 500, 0),
	}
	path := writeCodexProviderSessionContent(
		t, root, uuid, strings.Join(lines, "\n")+"\n",
	)
	provider, ok := NewProvider(AgentCodex, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)

	collector := &collectingPipelineObserver{}
	requirePipelineObserverProvider(t, provider).
		SetPipelineObserverSource(func() pipevents.Observer { return collector })
	source := requireCodexProviderSource(t, provider, uuid)

	outcome, err := provider.Parse(t.Context(), ParseRequest{Source: source})
	require.NoError(t, err)
	require.Len(t, outcome.Results, 1)
	require.Len(t, outcome.Results[0].Result.Messages, 2)

	events := collector.all()
	require.NotEmpty(t, events)
	assert.Equal(t, pipevents.KindParseStarted, events[0].Kind,
		"parse_started must open the event stream")
	started := events[0]
	assert.Equal(t, "full", started.Payload["mode"])
	assert.Equal(t, int64(0), started.Payload["offset"])
	assert.Equal(t, path, started.Path)
	assert.Equal(t, pipevents.StageParse, started.Stage)

	assert.Equal(t, pipevents.KindParseDone, events[len(events)-1].Kind,
		"parse_done must close the event stream")
	done := events[len(events)-1]
	assert.Equal(t, 2, done.Payload["messages"])
	assert.Equal(t, int64(len(strings.Join(lines, "\n")))+1,
		done.Payload["consumed_bytes"])
	assert.Equal(t, true, done.Payload["safe"])
	assert.Equal(t, true, done.Payload["checkpoint"])

	classified := collector.ofKind(pipevents.KindLineClassified)
	require.Len(t, classified, len(lines),
		"every physical JSONL line must be classified exactly once")
	wantTypes := []string{
		"session_meta", "turn_context", "response_item",
		"response_item", "event_msg",
	}
	lineOffset := int64(0)
	for i, ev := range classified {
		assert.Equal(t, i+1, ev.Payload["line_no"])
		assert.Equal(t, wantTypes[i], ev.Payload["type"])
		assert.Equal(t, lines[i], ev.Payload["raw"])
		lineOffset += int64(len(lines[i])) + 1
		assert.Equal(t, lineOffset, ev.Payload["offset"])
	}

	emitted := collector.ofKind(pipevents.KindMessageEmitted)
	require.Len(t, emitted, 2)
	previous := -1
	for i, ev := range emitted {
		ordinal, ok := ev.Payload["ordinal"].(int)
		require.True(t, ok, "ordinal must be an int, got %T", ev.Payload["ordinal"])
		require.Greater(t, ordinal, previous,
			"message ordinals must be strictly increasing")
		previous = ordinal
		assert.NotEmpty(t, ev.Payload["kind"])
		assert.NotEmpty(t, ev.Payload["role"])
		assert.NotEmpty(t, ev.Payload["digest"])
		assert.Equal(t, outcome.Results[0].Result.Messages[i].Ordinal, ordinal)
	}
	assert.Equal(t, "user", emitted[0].Payload["role"])
	assert.Equal(t, "assistant", emitted[1].Payload["role"])

	updated := collector.ofKind(pipevents.KindCursorUpdated)
	require.Len(t, updated, 1)
	cursor := updated[0]
	assert.Equal(t, lineOffset, cursor.Payload["offset"])
	assert.Equal(t, 0, cursor.Payload["pending_calls"])
	assert.Equal(t, true, cursor.Payload["first_user_seen"])
	assert.Equal(t, false, cursor.Payload["fork_gate_active"])
	assert.Equal(t, 2, cursor.Payload["next_ordinal"])

	assert.Empty(t, collector.ofKind(pipevents.KindIncrementalFallback))
}

func TestCodexPipelineObserverIncrementalParseEmitsTailEvents(t *testing.T) {
	root := t.TempDir()
	const uuid = "019eb791-cf7d-75c1-8439-9ed74c1229f1"
	prefix := testjsonl.JoinJSONL(
		testjsonl.CodexSessionMetaJSON(
			uuid, "/workspace/project-a", "codex_cli_rs", tsEarly,
		),
		testjsonl.CodexTurnContextJSON("gpt-5.4", tsEarlyS1),
		testjsonl.CodexMsgJSON("user", "first request", tsEarlyS1),
		testjsonl.CodexMsgJSON("assistant", "first response", tsEarlyS5),
	)
	path := writeCodexProviderSessionContent(t, root, uuid, prefix)
	provider, ok := NewProvider(AgentCodex, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	collector := &collectingPipelineObserver{}
	requirePipelineObserverProvider(t, provider).
		SetPipelineObserverSource(func() pipevents.Observer { return collector })
	source := requireCodexProviderSource(t, provider, uuid)
	prefixFingerprint, err := provider.Fingerprint(t.Context(), source)
	require.NoError(t, err)

	first, err := provider.Parse(t.Context(), ParseRequest{
		Source: source, Fingerprint: prefixFingerprint,
	})
	require.NoError(t, err)
	require.Len(t, first.Results, 1)
	checkpoint := first.Results[0].Result.Checkpoint
	require.NotEmpty(t, checkpoint)

	tail := testjsonl.JoinJSONL(
		testjsonl.CodexMsgJSON("user", "second request", tsLate),
		testjsonl.CodexMsgJSON("assistant", "second response", tsLateS5),
	)
	appendCodexProviderContent(t, path, tail)
	tailFingerprint, err := provider.Fingerprint(t.Context(), source)
	require.NoError(t, err)
	collector.mu.Lock()
	collector.events = nil
	collector.mu.Unlock()

	outcome, status, err := provider.ParseIncremental(
		t.Context(),
		IncrementalRequest{
			Source:       source,
			Fingerprint:  tailFingerprint,
			SessionID:    "codex:" + uuid,
			Offset:       int64(len(prefix)),
			StartOrdinal: 2,
			Seed:         checkpoint,
		},
	)
	require.NoError(t, err)
	require.Equal(t, IncrementalApplied, status)
	require.Len(t, outcome.Messages, 2)

	events := collector.all()
	require.NotEmpty(t, events)
	assert.Equal(t, pipevents.KindParseStarted, events[0].Kind)
	started := events[0]
	assert.Equal(t, "incremental", started.Payload["mode"])
	assert.Equal(t, int64(len(prefix)), started.Payload["offset"])
	assert.Equal(t, tailFingerprint.Size, started.Payload["size"])
	assert.Equal(t, true, started.Payload["seeded"])
	assert.Equal(t, pipevents.KindParseDone, events[len(events)-1].Kind)
	assert.Equal(t, 2, events[len(events)-1].Payload["messages"])

	classified := collector.ofKind(pipevents.KindLineClassified)
	require.Len(t, classified, 2,
		"only the appended tail lines are classified on the incremental path")
	lineOffset := int64(len(prefix))
	for i, ev := range classified {
		assert.Equal(t, i+1, ev.Payload["line_no"])
		assert.Equal(t, "response_item", ev.Payload["type"])
		lineOffset += int64(len(strings.Split(tail, "\n")[i])) + 1
		assert.Equal(t, lineOffset, ev.Payload["offset"])
	}

	emitted := collector.ofKind(pipevents.KindMessageEmitted)
	require.Len(t, emitted, 2)
	assert.Equal(t, 2, emitted[0].Payload["ordinal"])
	assert.Equal(t, 3, emitted[1].Payload["ordinal"])

	updated := collector.ofKind(pipevents.KindCursorUpdated)
	require.Len(t, updated, 1)
	assert.Equal(t, 4, updated[0].Payload["next_ordinal"])
	assert.Empty(t, collector.ofKind(pipevents.KindIncrementalFallback))
}

func TestCodexPipelineObserverIncrementalFallbackReportsReason(t *testing.T) {
	root := t.TempDir()
	const uuid = "019eb791-cf7d-75c1-8439-9ed74c1229f2"
	prefix := testjsonl.JoinJSONL(
		testjsonl.CodexSessionMetaJSON(
			uuid, "/workspace/project-a", "codex_cli_rs", tsEarly,
		),
		testjsonl.CodexMsgJSON("user", "first request", tsEarlyS1),
		testjsonl.CodexMsgJSON("assistant", "first response", tsEarlyS5),
	)
	path := writeCodexProviderSessionContent(t, root, uuid, prefix)
	provider, ok := NewProvider(AgentCodex, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	collector := &collectingPipelineObserver{}
	requirePipelineObserverProvider(t, provider).
		SetPipelineObserverSource(func() pipevents.Observer { return collector })
	source := requireCodexProviderSource(t, provider, uuid)
	prefixFingerprint, err := provider.Fingerprint(t.Context(), source)
	require.NoError(t, err)
	_, err = provider.Parse(t.Context(), ParseRequest{
		Source: source, Fingerprint: prefixFingerprint,
	})
	require.NoError(t, err)

	// A session_meta after the persisted offset forces an authoritative
	// rebuild: the incremental parse must decline with an observable reason.
	tail := testjsonl.JoinJSONL(
		testjsonl.CodexSessionMetaJSON(
			uuid, "/workspace/project-b", "codex_cli_rs", tsLate,
		),
	)
	appendCodexProviderContent(t, path, tail)
	tailFingerprint, err := provider.Fingerprint(t.Context(), source)
	require.NoError(t, err)
	collector.mu.Lock()
	collector.events = nil
	collector.mu.Unlock()

	_, status, err := provider.ParseIncremental(
		t.Context(),
		IncrementalRequest{
			Source:       source,
			Fingerprint:  tailFingerprint,
			SessionID:    "codex:" + uuid,
			Offset:       int64(len(prefix)),
			StartOrdinal: 2,
		},
	)
	require.NoError(t, err)
	require.Equal(t, IncrementalNeedsFullParse, status)

	fallbacks := collector.ofKind(pipevents.KindIncrementalFallback)
	require.NotEmpty(t, fallbacks,
		"a declined incremental parse must report incremental_fallback")
	found := false
	for _, ev := range fallbacks {
		reason, _ := ev.Payload["reason"].(string)
		if reason == "session_meta_after_offset" {
			assert.Equal(t, 1, ev.Payload["line_no"])
			found = true
		}
	}
	assert.True(t, found,
		fmt.Sprintf("expected session_meta_after_offset fallback, got %v", fallbacks))
}

func TestCodexPipelineObserverDisabledSourceAllocatesNoEvents(t *testing.T) {
	root := t.TempDir()
	const uuid = "019eb791-cf7d-75c1-8439-9ed74c1229f3"
	writeCodexProviderSessionContent(t, root, uuid, testjsonl.JoinJSONL(
		testjsonl.CodexSessionMetaJSON(
			uuid, "/workspace/project-a", "codex_cli_rs", tsEarly,
		),
		testjsonl.CodexMsgJSON("user", "first request", tsEarlyS1),
	))
	provider, ok := NewProvider(AgentCodex, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	source := requireCodexProviderSource(t, provider, uuid)

	// No source installed: the hot path must stay silent and uninstrumented.
	_, err := provider.Parse(t.Context(), ParseRequest{Source: source})
	require.NoError(t, err)

	// An installed source whose resolver returns nil behaves the same way.
	collector := &collectingPipelineObserver{}
	requirePipelineObserverProvider(t, provider).
		SetPipelineObserverSource(func() pipevents.Observer { return nil })
	_, err = provider.Parse(t.Context(), ParseRequest{Source: source})
	require.NoError(t, err)
	assert.Empty(t, collector.all())

	// Toggling the observer on mid-life lights up the same provider.
	requirePipelineObserverProvider(t, provider).
		SetPipelineObserverSource(func() pipevents.Observer { return collector })
	_, err = provider.Parse(t.Context(), ParseRequest{Source: source})
	require.NoError(t, err)
	assert.NotEmpty(t, collector.ofKind(pipevents.KindLineClassified))
}
