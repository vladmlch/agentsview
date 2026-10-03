package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/money"
)

func parseJunieProviderSession(
	t *testing.T, path, machine string,
) (*ParsedSession, []ParsedMessage) {
	t.Helper()
	root := filepath.Dir(filepath.Dir(path))
	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	require.True(t, samePath(path, sources[0].DisplayPath))
	source := sources[0]
	fingerprint, err := provider.Fingerprint(t.Context(), source)
	require.NoError(t, err)
	outcome, err := provider.Parse(t.Context(), ParseRequest{
		Source: source, Fingerprint: fingerprint, Machine: machine,
	})
	require.NoError(t, err)
	require.NotEmpty(t, outcome.Results)
	result := outcome.Results[0].Result
	return &result.Session, result.Messages
}

func TestParseJunieSession(t *testing.T) {
	root := t.TempDir()
	sessionID := "session-260101-120000-abcd"
	sessionDir := filepath.Join(root, sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.jsonl"), []byte(
		`{"sessionId":"other","createdAt":1}`+"\n"+
			`{"sessionId":"`+sessionID+`","createdAt":1704067100000,"updatedAt":1704067109000,"projectDir":"/work/old","taskName":"Old title","status":"in_progress"}`+"\n"+
			`{"sessionId":"`+sessionID+`","createdAt":1704067200000,"updatedAt":1704067204500,"projectDir":"/work/demo","taskName":"Build the feature","status":"completed"}`+"\n",
	), 0o600))

	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"req-1","prompt":"hidden context","presentablePrompt":"Implement it","askMode":false,"thinkMore":false,"timestampMs":1704067201000}` + "\n" +
			`{"kind":"UserPromptEvent","requestId":"req-dropped","prompt":"Drop me","askMode":false,"thinkMore":false,"timestampMs":1704067202000}` + "\n" +
			`{"kind":"UserMessagesDroppedFromHistory","userMessageIds":["req-dropped"],"timestampMs":1704067202500}` + "\n" +
			`{"kind":"UserPromptEvent","requestId":"req-restored","prompt":"Restore me","timestampMs":1704067202600}` + "\n" +
			`{"kind":"UserMessagesDroppedFromHistory","userMessageIds":["req-restored"],"timestampMs":1704067202700}` + "\n" +
			`{"kind":"UserMessagesCommittedToHistory","userMessageIds":["req-restored"],"timestampMs":1704067202800}` + "\n" +
			`{"kind":"UserPromptEvent","requestId":"req-failed","prompt":"Fail me","timestampMs":1704067202850}` + "\n" +
			`{"kind":"UserMessagesFailedInHistory","userMessageIds":["req-failed"],"timestampMs":1704067202900}` + "\n" +
			`{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"MarkdownBlockUpdatedEvent","stepId":"step-1","text":"thinking aloud"}},"timestampMs":1704067203000}` + "\n" +
			`{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"ResultBlockUpdatedEvent","stepId":"step-1","cancelled":false,"result":"draft","changes":[]}},"timestampMs":1704067204000}` + "\n" +
			`{"kind":"SessionA2uxEvent","event":{"state":"COMPLETED","agentEvent":{"kind":"ResultBlockUpdatedEvent","stepId":"step-1","cancelled":false,"result":"<!-- ANSWER -->Done","changes":[]}},"timestampMs":1704067205000}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"LlmResponseMetadataEvent","agent":"JUNIE","modelUsage":[{"model":"claude-sonnet-4-6","cost":0.00125,"inputTokens":100,"cacheInputTokens":20,"cacheCreateTokens":30,"outputTokens":40,"time":1}]}},"timestampMs":1704067205100}` + "\n" +
			// Summarization cost snapshots can occur during compaction; they must not terminate the message stream.
			`{"kind":"SessionCostTrajectorySnapshotEvent","snapshot":{"attributedGroups":[{"costPurpose":"SUMMARIZATION","callPurpose":"SUMMARIZATION"}]},"timestampMs":1704067205200}` + "\n" +
			`{"kind":"SystemMessageEvent","text":"Notice","details":"Details","level":"ERROR","symbol":"!","timestampMs":1704067205500}` + "\n" +
			`{"kind":"AgentTaskFailedEvent","timestampMs":1704067205600}` + "\n" +
			`{"kind":"UserAsyncResponseEvent","entries":[{"question":"Continue?","answer":"Yes"}],"timestampMs":1704067206000}` + "\n" +
			`{"kind":"SessionA2uxEvent","event":{"state":"COMPLETED","agentEvent":{"kind":"ResultBlockUpdatedEvent","stepId":"step-2","cancelled":false,"result":"Finished","changes":[]}},"timestampMs":1704067207000}` + "\n" +
			"{\n" + `{}` + "\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))

	sess, messages := parseJunieProviderSession(t, path, "local")

	assert.Equal(t, "junie:"+sessionID, sess.ID)
	assert.Equal(t, sessionID, sess.SourceSessionID)
	assert.Equal(t, AgentJunie, sess.Agent)
	assert.Equal(t, "local", sess.Machine)
	assert.Equal(t, "demo", sess.Project)
	assert.Equal(t, "/work/demo", sess.Cwd)
	assert.Equal(t, "Implement it", sess.FirstMessage)
	assert.Equal(t, "Build the feature", sess.SessionName)
	assert.True(t, sess.SessionNamePresent)
	assert.Equal(t, time.UnixMilli(1704067200000), sess.StartedAt)
	assert.Equal(t, time.UnixMilli(1704067207000), sess.EndedAt)
	assert.Equal(t, 6, sess.MessageCount)
	assert.Equal(t, 3, sess.UserMessageCount)
	assert.Equal(t, 1, sess.MalformedLines)
	assert.True(t, sess.HasTotalOutputTokens)
	assert.Equal(t, 40, sess.TotalOutputTokens)
	assert.True(t, sess.HasPeakContextTokens)
	assert.Equal(t, 150, sess.PeakContextTokens)
	require.Len(t, sess.UsageEvents, 1)
	usage := sess.UsageEvents[0]
	assert.Equal(t, "junie:"+sessionID, usage.SessionID)
	assert.Equal(t, "llm-response", usage.Source)
	assert.Equal(t, "claude-sonnet-4-6", usage.Model)
	assert.Equal(t, 100, usage.InputTokens)
	assert.Equal(t, 40, usage.OutputTokens)
	assert.Equal(t, 20, usage.CacheReadInputTokens)
	assert.Equal(t, 30, usage.CacheCreationInputTokens)
	require.NotNil(t, usage.Cost)
	assert.Equal(t, money.MustParseDollars("0.00125"), *usage.Cost)
	assert.Equal(t, "exact", usage.CostStatus)
	assert.Equal(t, "junie-model-usage", usage.CostSource)
	assert.Equal(t, "2024-01-01T00:00:05.1Z", usage.OccurredAt)
	assert.Equal(t, "junie:"+sessionID+":llm-response:12:0", usage.DedupKey)

	require.Len(t, messages, 6)
	assertMessage(t, messages[0], RoleUser, "Implement it")
	assertMessage(t, messages[1], RoleUser, "Restore me")
	assertMessage(t, messages[2], RoleAssistant, "Done\n\nFinished")
	assertMessage(t, messages[3], RoleSystem, "Notice\n\nDetails")
	assertMessage(t, messages[4], RoleSystem, "Task failed")
	assert.True(t, messages[3].IsSystem)
	assert.True(t, messages[4].IsSystem)
	assert.Equal(t, "AgentTaskFailedEvent", messages[4].SourceSubtype)
	assertMessage(t, messages[5], RoleUser, "Continue?\nYes")
	for i, message := range messages {
		assert.Equal(t, i, message.Ordinal)
	}
	assert.Equal(t, time.UnixMilli(1704067205000), messages[2].Timestamp)
}

func TestParseJunieSessionRejectsInvalidReportedCost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(
		`{"kind":"SessionA2uxEvent","event":{"agentEvent":{"kind":"LlmResponseMetadataEvent","modelUsage":[{"model":"test","cost":-1}]}}}`+"\n",
	), 0o600))

	_, _, err := parseJunieSessionWithSummary(
		t.Context(), path, "session-one", junieSessionSummary{}, false, openJunieRoot,
	)
	require.ErrorContains(t, err, "invalid model usage cost")
}

func TestJunieSourceSetDiscoversOnlyEventStreams(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		filepath.Join(root, "session-one", "events.jsonl"),
		filepath.Join(root, "session-two", "events.jsonl"),
		filepath.Join(root, "session-one", "state.json"),
		filepath.Join(root, "session-two", "transcript.md"),
		filepath.Join(root, "index.jsonl"),
		filepath.Join(root, "nested", "session-three", "events.jsonl"),
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
	}
	indexPath := filepath.Join(root, "index.jsonl")
	require.NoError(t, os.WriteFile(indexPath, []byte(
		`{"sessionId":"session-one","projectDir":"/old-one"}`+"\n"+
			`{"sessionId":"session-two","projectDir":"/old-two"}`+"\n",
	), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{
		Roots:   []string{root},
		Machine: "local",
	})
	require.True(t, ok)
	assert.Equal(t, CapabilitySupported,
		provider.Capabilities().Content.AggregateUsageEvents)
	assert.Equal(t, CapabilitySupported,
		provider.Capabilities().Source.ForceReplaceOnParse)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 2)
	assert.Equal(t, AgentJunie, sources[0].Provider)
	assert.Equal(t, "session-one", sources[0].ProjectHint)
	assert.Equal(t, filepath.Join(root, "session-one", "events.jsonl"), sources[0].DisplayPath)
	assert.Equal(t, "session-two", sources[1].ProjectHint)

	fingerprint, err := provider.Fingerprint(t.Context(), sources[0])
	require.NoError(t, err)
	unchangedFingerprint, err := provider.Fingerprint(t.Context(), sources[1])
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(indexPath, []byte(
		`{"sessionId":"session-one","projectDir":"/new-one"}`+"\n"+
			`{"sessionId":"session-two","projectDir":"/old-two"}`+"\n",
	), 0o600))

	changed, err := provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{
		Path:      indexPath,
		WatchRoot: root,
	})
	require.NoError(t, err)
	require.Len(t, changed, 1)
	assert.Equal(t, sources[0].Key, changed[0].Key)

	updatedFingerprint, err := provider.Fingerprint(t.Context(), sources[0])
	require.NoError(t, err)
	assert.NotEqual(t, fingerprint.Hash, updatedFingerprint.Hash)
	stillUnchangedFingerprint, err := provider.Fingerprint(t.Context(), sources[1])
	require.NoError(t, err)
	assert.Equal(t, unchangedFingerprint, stillUnchangedFingerprint)

	require.NoError(t, os.Remove(indexPath))
	changed, err = provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{
		Path:      indexPath,
		WatchRoot: root,
	})
	require.NoError(t, err)
	assert.Empty(t, changed, "a missing index must not create destructive removal work")
}

func TestJunieDiscoveryContinuesAfterRootRemoval(t *testing.T) {
	parent := t.TempDir()
	roots := []string{filepath.Join(parent, "a"), filepath.Join(parent, "b")}
	for i, root := range roots {
		path := filepath.Join(root, fmt.Sprintf("session-%d", i), "events.jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
	}
	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: roots})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 2)

	require.NoError(t, os.RemoveAll(roots[0]))
	path := filepath.Join(roots[1], "session-new", "events.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
	for range 2 {
		sources, err = provider.Discover(t.Context())
		require.NoError(t, err)
		require.Len(t, sources, 2)
		assert.Equal(t, "session-1", sources[0].ProjectHint)
		assert.Equal(t, "session-new", sources[1].ProjectHint)
	}
}

func TestParseJunieSessionKeepsCompleteEventsBeforeTruncatedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-one", "events.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(
		`{"kind":"UserPromptEvent","requestId":"request","prompt":"Keep me"}`+"\n"+
			`{"kind":"SessionA2uxEvent","event":`,
	), 0o600))
	session, messages := parseJunieProviderSession(t, path, "local")
	require.Len(t, messages, 1)
	assert.Equal(t, "Keep me", messages[0].Content)
	assert.Equal(t, 1, session.MalformedLines)
}

func TestJunieFingerprintKeepsExistingSummaryEncoding(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session-one", "events.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.jsonl"), []byte(
		`{"sessionId":"session-one","projectDir":"/a<b&\u2028","taskName":"Demo"}`+"\n",
	), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	fingerprint, err := provider.Fingerprint(t.Context(), sources[0])
	require.NoError(t, err)
	assert.Equal(t, "a2af8ce583c1bbf7889a325be37031e072dc2186b045f2f9eb913b2f1a4c8d6c", fingerprint.Hash)
}

func TestJunieSourceSetReusesIndexSnapshotWhileParsing(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-one")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "events.jsonl"), []byte(
		`{"kind":"UserPromptEvent","prompt":"Hello","timestampMs":1704067201000}`+"\n",
	), 0o600))
	indexPath := filepath.Join(root, "index.jsonl")
	require.NoError(t, os.WriteFile(indexPath, []byte(
		`{"sessionId":"session-one","taskName":"Cached title"}`+"\n",
	), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	fingerprint, err := provider.Fingerprint(t.Context(), sources[0])
	require.NoError(t, err)
	require.NoError(t, os.Remove(indexPath))

	outcome, err := provider.Parse(t.Context(), ParseRequest{Source: sources[0], Fingerprint: fingerprint})
	require.NoError(t, err)
	require.Len(t, outcome.Results, 1)
	assert.Equal(t, "Cached title", outcome.Results[0].Result.Session.SessionName)
	assert.Equal(t, fingerprint.Size, outcome.Results[0].Result.Session.File.Size)
	assert.Equal(t, fingerprint.MTimeNS, outcome.Results[0].Result.Session.File.Mtime)
	assert.Equal(t, fingerprint.Hash, outcome.Results[0].Result.Session.File.Hash)
}

func TestJunieIndexChangeWorkIsBoundedByChangedSessions(t *testing.T) {
	for _, sessionCount := range []int{2, 128} {
		t.Run(fmt.Sprintf("sessions-%d", sessionCount), func(t *testing.T) {
			root := t.TempDir()
			changedID := fmt.Sprintf("session-%03d", sessionCount/2)
			var before, after strings.Builder
			for i := range sessionCount {
				sessionID := fmt.Sprintf("session-%03d", i)
				path := filepath.Join(root, sessionID, "events.jsonl")
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
				fmt.Fprintf(&before, `{"sessionId":"%s","taskName":"old"}`+"\n", sessionID)
				title := "old"
				if sessionID == changedID {
					title = "new"
				}
				fmt.Fprintf(&after, `{"sessionId":"%s","taskName":"%s"}`+"\n", sessionID, title)
			}

			indexPath := filepath.Join(root, "index.jsonl")
			require.NoError(t, os.WriteFile(indexPath, []byte(before.String()), 0o600))
			def, ok := AgentByType(AgentJunie)
			require.True(t, ok)
			factory := newJunieProviderFactory(def)
			cfg := ProviderConfig{Roots: []string{root}, Machine: "local"}
			provider := factory.NewProvider(cfg)
			_, err := provider.WatchPlan(t.Context())
			require.NoError(t, err)
			discovered, err := provider.Discover(t.Context())
			require.NoError(t, err)
			var changedSource *SourceRef
			for i := range discovered {
				if discovered[i].ProjectHint == changedID {
					changedSource = &discovered[i]
					break
				}
			}
			require.NotNil(t, changedSource)
			beforeFingerprint, err := provider.Fingerprint(t.Context(), *changedSource)
			require.NoError(t, err)
			eventInfo, err := os.Stat(changedSource.DisplayPath)
			require.NoError(t, err)
			assert.Equal(t, eventInfo.Size(), beforeFingerprint.Size,
				"metadata must affect the hash without inflating the transcript size")
			statusOnly := strings.ReplaceAll(before.String(), "}\n", `,"status":"RUNNING"}`+"\n")
			require.NoError(t, os.WriteFile(indexPath, []byte(statusOnly), 0o600))
			changed, err := provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{
				Path:      indexPath,
				WatchRoot: root,
			})
			require.NoError(t, err)
			assert.Empty(t, changed, "unused index fields must not invalidate a session")
			require.NoError(t, os.WriteFile(indexPath, []byte(after.String()), 0o600))
			provider = factory.NewProvider(cfg)
			_, err = provider.WatchPlan(t.Context())
			require.NoError(t, err)

			for range 2 {
				relevance, err := ResolveChangedPathRelevance(t.Context(), provider, ChangedPathRequest{Path: indexPath})
				require.NoError(t, err)
				assert.Equal(t, ChangedPathDataBearing, relevance)
			}

			changed, err = provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{
				Path:      indexPath,
				WatchRoot: root,
			})
			require.NoError(t, err)
			require.Len(t, changed, 1)
			assert.Equal(t, changedID, changed[0].ProjectHint)
			afterFingerprint, err := provider.Fingerprint(t.Context(), changed[0])
			require.NoError(t, err)
			assert.Equal(t, beforeFingerprint.Size, afterFingerprint.Size)
			assert.NotEqual(t, beforeFingerprint.Hash, afterFingerprint.Hash)

			retry, err := provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{
				Path:      indexPath,
				WatchRoot: root,
			})
			require.NoError(t, err)
			assert.Empty(t, retry, "unchanged index rows must not schedule more work")
		})
	}
}

func TestParseJunieTitleOnlySessionWithoutIndex(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session-fallback", "events.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(
		`{"kind":"SessionTitleSetEvent","name":"Fallback title","timestampMs":1704067200000}`+"\n",
	), 0o600))

	sess, messages := parseJunieProviderSession(t, path, "local")
	assert.Equal(t, "junie", sess.Project)
	assert.Empty(t, sess.Cwd)
	assert.Equal(t, "Fallback title", sess.SessionName)
	assert.Equal(t, "Fallback title", sess.FirstMessage)
	assert.Equal(t, time.UnixMilli(1704067200000), sess.StartedAt)
	assert.Equal(t, time.UnixMilli(1704067200000), sess.EndedAt)
	assert.Empty(t, messages)
}

func TestParseJunieSessionRejectsSymlinkedIndex(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "outside-index.jsonl")
	require.NoError(t, os.WriteFile(target, []byte(
		`{"sessionId":"session-safe","projectDir":"/private/secret","taskName":"Secret"}`+"\n",
	), 0o600))
	if err := os.Symlink(target, filepath.Join(root, "index.jsonl")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	path := filepath.Join(root, "session-safe", "events.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(
		`{"kind":"UserResponseEvent","prompt":"Safe","timestampMs":1704067201000}`+"\n",
	), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	_, err := provider.Discover(t.Context())
	require.ErrorContains(t, err, "is not a regular file")
}

func TestJunieIndexChangeIgnoresSymlinkedSessionDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "events.jsonl"), []byte(
		`{"kind":"UserPromptEvent","requestId":"outside","prompt":"Outside"}`+"\n",
	), 0o600))
	if err := os.Symlink(outside, filepath.Join(root, "session-outside")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	indexPath := filepath.Join(root, "index.jsonl")
	require.NoError(t, os.WriteFile(indexPath, nil, 0o600))
	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	_, err := provider.Discover(t.Context())
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(indexPath, []byte(
		`{"sessionId":"session-outside","taskName":"Outside"}`+"\n",
	), 0o600))
	changed, err := provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{
		Path:      indexPath,
		WatchRoot: root,
	})
	require.NoError(t, err)
	assert.Empty(t, changed)
}

func TestJunieSessionDirectorySwapCannotEscapeRoot(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-safe")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "events.jsonl"), []byte(
		`{"kind":"UserPromptEvent","requestId":"safe","prompt":"Safe"}`+"\n",
	), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	fingerprint, err := provider.Fingerprint(t.Context(), sources[0])
	require.NoError(t, err)

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "events.jsonl"), []byte(
		`{"kind":"UserPromptEvent","requestId":"outside","prompt":"Outside"}`+"\n",
	), 0o600))
	require.NoError(t, os.RemoveAll(sessionDir))
	if err := os.Symlink(outside, sessionDir); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err = provider.Fingerprint(t.Context(), sources[0])
	require.ErrorContains(t, err, "session directory is not a directory")
	_, err = provider.Parse(t.Context(), ParseRequest{
		Source: sources[0], Fingerprint: fingerprint, Machine: "local",
	})
	require.ErrorContains(t, err, "session directory is not a directory")
}

func TestJunieConfiguredRootSwapCannotEscapeRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "configured")
	sessionDir := filepath.Join(root, "session-safe")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "events.jsonl"), []byte(
		`{"kind":"UserPromptEvent","requestId":"safe","prompt":"Safe"}`+"\n",
	), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	fingerprint, err := provider.Fingerprint(t.Context(), sources[0])
	require.NoError(t, err)

	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "session-safe"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "session-safe", "events.jsonl"), []byte(
		`{"kind":"UserPromptEvent","requestId":"outside","prompt":"Outside"}`+"\n",
	), 0o600))
	require.NoError(t, os.Rename(root, filepath.Join(parent, "moved")))
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err = provider.Fingerprint(t.Context(), sources[0])
	require.ErrorContains(t, err, "junie root is not a directory")
	_, err = provider.Parse(t.Context(), ParseRequest{
		Source: sources[0], Fingerprint: fingerprint, Machine: "local",
	})
	require.ErrorContains(t, err, "junie root is not a directory")

	freshProvider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	_, err = freshProvider.Discover(t.Context())
	require.ErrorContains(t, err, "junie root is not a directory")
}

func TestJunieDiscoveryFindsRecreatedConfiguredRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "configured")
	writeSession := func(prompt string) {
		t.Helper()
		path := filepath.Join(root, "session-safe", "events.jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(
			`{"kind":"UserPromptEvent","requestId":"request","prompt":%q}`+"\n", prompt,
		)), 0o600))
	}
	writeSession("Before")
	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	_, err := provider.Discover(t.Context())
	require.NoError(t, err)

	require.NoError(t, os.Rename(root, filepath.Join(parent, "moved")))
	_, err = provider.Discover(t.Context())
	require.NoError(t, err)
	writeSession("After")
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	fingerprint, err := provider.Fingerprint(t.Context(), sources[0])
	require.NoError(t, err)
	outcome, err := provider.Parse(t.Context(), ParseRequest{
		Source: sources[0], Fingerprint: fingerprint, Machine: "local",
	})
	require.NoError(t, err)
	require.Len(t, outcome.Results, 1)
	assert.Equal(t, "After", outcome.Results[0].Result.Session.FirstMessage)
}

func TestJunieRecreatedRootDiscardsPreviousIndexMetadata(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "configured")
	sessionDir := filepath.Join(root, "session-safe")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(sessionDir, "events.jsonl"),
		[]byte(`{"kind":"UserPromptEvent","requestId":"request","prompt":"Before"}`+"\n"),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "index.jsonl"),
		[]byte(`{"sessionId":"session-safe","taskName":"Stale title"}`+"\n"),
		0o600,
	))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	_, err := provider.Discover(t.Context())
	require.NoError(t, err)

	// Replace the configured root at the same path with an index-free store.
	require.NoError(t, os.Rename(root, filepath.Join(parent, "moved")))
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(sessionDir, "events.jsonl"),
		[]byte(`{"kind":"UserPromptEvent","requestId":"request","prompt":"After"}`+"\n"),
		0o600,
	))

	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	fingerprint, err := provider.Fingerprint(t.Context(), sources[0])
	require.NoError(t, err)
	outcome, err := provider.Parse(t.Context(), ParseRequest{
		Source: sources[0], Fingerprint: fingerprint, Machine: "local",
	})
	require.NoError(t, err)
	require.Len(t, outcome.Results, 1)
	assert.Empty(t, outcome.Results[0].Result.Session.SessionName)
	assert.Equal(t, "After", outcome.Results[0].Result.Session.FirstMessage)
}

func TestJunieFindSourceRefreshesIndexSummary(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session-one", "events.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(
		`{"kind":"UserPromptEvent","requestId":"request","prompt":"Prompt"}`+"\n",
	), 0o600))
	indexPath := filepath.Join(root, "index.jsonl")
	require.NoError(t, os.WriteFile(indexPath, []byte(
		`{"sessionId":"session-one","taskName":"Before"}`+"\n",
	), 0o600))
	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	_, err := provider.Discover(t.Context())
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(indexPath, []byte(
		`{"sessionId":"session-one","taskName":"After"}`+"\n",
	), 0o600))
	source, found, err := provider.FindSource(t.Context(), FindSourceRequest{StoredFilePath: path})
	require.NoError(t, err)
	require.True(t, found)
	fingerprint, err := provider.Fingerprint(t.Context(), source)
	require.NoError(t, err)
	outcome, err := provider.Parse(t.Context(), ParseRequest{
		Source: source, Fingerprint: fingerprint, Machine: "local",
	})
	require.NoError(t, err)
	require.Len(t, outcome.Results, 1)
	assert.Equal(t, "After", outcome.Results[0].Result.Session.SessionName)
}

func TestJunieIncompleteIndexDoesNotReplaceCachedSnapshot(t *testing.T) {
	root := t.TempDir()
	for _, sessionID := range []string{"session-a", "session-b"} {
		path := filepath.Join(root, sessionID, "events.jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
	}
	indexPath := filepath.Join(root, "index.jsonl")
	before := `{"sessionId":"session-a","taskName":"Before"}` + "\n" +
		`{"sessionId":"session-b","taskName":"Stable"}` + "\n"
	require.NoError(t, os.WriteFile(indexPath, []byte(before), 0o600))
	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	_, err := provider.Discover(t.Context())
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(indexPath, []byte(
		`{"sessionId":"session-a","taskName":"After"}`+"\n"+"{\n",
	), 0o600))
	_, err = provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{Path: indexPath})
	require.ErrorContains(t, err, "invalid JSON")
	require.NoError(t, os.Remove(indexPath))
	changed, err := provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{Path: indexPath})
	require.NoError(t, err)
	assert.Empty(t, changed, "a missing shared index must not erase the last complete snapshot")

	after := `{"sessionId":"session-a","taskName":"After"}` + "\n" +
		`{"sessionId":"session-b","taskName":"Stable"}` + "\n"
	require.NoError(t, os.WriteFile(indexPath, []byte(after), 0o600))
	changed, err = provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{Path: indexPath})
	require.NoError(t, err)
	require.Len(t, changed, 1)
	assert.Equal(t, "session-a", changed[0].ProjectHint)
}

func TestJunieUnavailableIndexSessionDoesNotStayPending(t *testing.T) {
	root := t.TempDir()
	indexPath := filepath.Join(root, "index.jsonl")
	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	_, err := provider.Discover(t.Context())
	require.NoError(t, err)

	// The index now lists a session whose transcript does not exist, so nothing
	// can produce a source for it.
	require.NoError(t, os.WriteFile(indexPath, []byte(
		`{"sessionId":"session-gone","taskName":"Gone"}`+"\n",
	), 0o600))

	req := ChangedPathRequest{Path: indexPath, WatchRoot: root}
	relevance, err := ResolveChangedPathRelevance(t.Context(), provider, req)
	require.NoError(t, err)
	require.Equal(t, ChangedPathDataBearing, relevance)

	sources, err := provider.SourcesForChangedPath(t.Context(), req)
	require.NoError(t, err)
	require.Empty(t, sources)

	// The unavailable session must not stay pending, so an unchanged index is
	// classified as non-data instead of driving archive-wide fallback forever.
	relevance, err = ResolveChangedPathRelevance(t.Context(), provider, req)
	require.NoError(t, err)
	assert.Equal(t, ChangedPathNonData, relevance)
}

func TestJunieIndexStatErrorPreservesBaseline(t *testing.T) {
	for _, blockRoot := range []bool{false, true} {
		name := "event stream"
		if blockRoot {
			name = "session directory"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			sessionDir := filepath.Join(root, "session-one")
			require.NoError(t, os.Mkdir(sessionDir, 0o755))
			eventsPath := filepath.Join(sessionDir, "events.jsonl")
			require.NoError(t, os.WriteFile(eventsPath, []byte("{}\n"), 0o600))
			indexPath := filepath.Join(root, "index.jsonl")
			require.NoError(t, os.WriteFile(indexPath, []byte(`{"sessionId":"session-one","taskName":"before"}`+"\n"), 0o600))
			provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
			require.True(t, ok)
			_, err := provider.Discover(t.Context())
			require.NoError(t, err)

			require.NoError(t, os.WriteFile(indexPath, []byte(`{"sessionId":"session-one","taskName":"after"}`+"\n"), 0o600))
			req := ChangedPathRequest{Path: indexPath, WatchRoot: root}
			blockedDir, checkedPath := sessionDir, eventsPath
			if blockRoot {
				// Classify the index while it is still accessible.
				relevance, err := ResolveChangedPathRelevance(t.Context(), provider, req)
				require.NoError(t, err)
				require.Equal(t, ChangedPathDataBearing, relevance)
				blockedDir, checkedPath = root, sessionDir
			}
			require.NoError(t, os.Chmod(blockedDir, 0o000))
			t.Cleanup(func() { require.NoError(t, os.Chmod(blockedDir, 0o755)) })
			if _, err := os.Lstat(checkedPath); err == nil {
				t.Skip("filesystem does not enforce directory permissions")
			} else {
				require.ErrorIs(t, err, os.ErrPermission)
			}
			_, err = provider.SourcesForChangedPath(t.Context(), req)
			require.ErrorIs(t, err, os.ErrPermission)

			require.NoError(t, os.Chmod(blockedDir, 0o755))
			sources, err := provider.SourcesForChangedPath(t.Context(), req)
			require.NoError(t, err)
			require.Len(t, sources, 1, "the unchanged index must retry after access recovers")
			assert.Equal(t, "session-one", sources[0].ProjectHint)
		})
	}
}

func TestOpenJuniePinnedFileRejectsReplacement(t *testing.T) {
	rootPath := t.TempDir()
	path := filepath.Join(rootPath, "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("before"), 0o600))
	root, err := os.OpenRoot(rootPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	expected, err := root.Lstat("events.jsonl")
	require.NoError(t, err)
	require.NoError(t, os.Rename(path, filepath.Join(rootPath, "old-events.jsonl")))
	require.NoError(t, os.WriteFile(path, []byte("after"), 0o600))

	_, err = openJuniePinnedFile(root, "events.jsonl", expected)
	require.ErrorContains(t, err, "changed while opening")
}

func TestJunieRejectsOversizedRecords(t *testing.T) {
	for _, target := range []string{"events.jsonl", "index.jsonl"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "session-one", "events.jsonl")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, []byte(`{"kind":"UserPromptEvent","prompt":"Keep me"}`+"\n"), 0o600))
			indexPath := filepath.Join(root, "index.jsonl")
			index := `{"sessionId":"session-one","taskName":"Original title"}` + "\n"
			require.NoError(t, os.WriteFile(indexPath, []byte(index), 0o600))
			provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
			require.True(t, ok)
			sources, err := provider.Discover(t.Context())
			require.NoError(t, err)
			require.Len(t, sources, 1)
			before, err := provider.Fingerprint(t.Context(), sources[0])
			require.NoError(t, err)

			oversizedPath := path
			if target == "index.jsonl" {
				oversizedPath = indexPath
			}
			file, err := os.OpenFile(oversizedPath, os.O_WRONLY|os.O_APPEND, 0)
			require.NoError(t, err)
			_, err = file.WriteString(`{"sessionId":"session-one","kind":"UserResponseEvent","prompt":"` + strings.Repeat("x", maxLineSize) + `"}` + "\n")
			require.NoError(t, err)
			require.NoError(t, file.Close())

			if target == "events.jsonl" {
				outcome, err := provider.Parse(t.Context(), ParseRequest{Source: sources[0]})
				require.ErrorContains(t, err, "record exceeds")
				assert.Empty(t, outcome.Results)
			} else {
				_, err := provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{Path: indexPath})
				require.ErrorContains(t, err, "record exceeds")
				after, err := provider.Fingerprint(t.Context(), sources[0])
				require.NoError(t, err)
				assert.Equal(t, before.Hash, after.Hash, "rejected index must retain the active metadata")
				require.NoError(t, os.WriteFile(indexPath, []byte(index), 0o600))
				relevance, err := ResolveChangedPathRelevance(t.Context(), provider, ChangedPathRequest{Path: indexPath})
				require.NoError(t, err)
				assert.Equal(t, ChangedPathNonData, relevance, "rejected index must retain the watcher baseline")
			}
		})
	}
}

func TestJunieStartupStubDirectoryIsExcluded(t *testing.T) {
	root := t.TempDir()
	sessionID := "session-startup-stub"
	sessionDir := filepath.Join(root, sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(
		`{"kind":"SystemMessageEvent","text":"A new version of Junie is available.","timestampMs":1704067201000}`+"\n"+
			`{"kind":"SystemMessageEvent","text":"Model switched to Grok","timestampMs":1704067202000}`+"\n",
	), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)

	fingerprint, err := provider.Fingerprint(t.Context(), sources[0])
	require.NoError(t, err)
	outcome, err := provider.Parse(t.Context(), ParseRequest{
		Source: sources[0], Fingerprint: fingerprint, Machine: "local",
	})
	require.NoError(t, err)
	assert.Empty(t, outcome.Results)
	assert.Equal(t, []string{"junie:" + sessionID}, outcome.ExcludedSessionIDs)
	assert.True(t, outcome.ForceReplace)
	assert.True(t, outcome.ResultSetComplete)
}

func TestJunieAgentTaskNameUpdatedEventSetsSessionName(t *testing.T) {
	root := t.TempDir()
	sessionID := "session-task-name"
	sessionDir := filepath.Join(root, sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(
		`{"kind":"UserPromptEvent","requestId":"req-1","prompt":"Do work","timestampMs":1704067201000}`+"\n"+
			`{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"AgentTaskNameUpdatedEvent","name":"Title from agent event"}},"taskId":"task-1","timestampMs":1704067202000}`+"\n",
	), 0o600))

	sess, messages := parseJunieProviderSession(t, path, "local")
	assert.Equal(t, "Title from agent event", sess.SessionName)
	assert.True(t, sess.SessionNamePresent)
	require.Len(t, messages, 1)
}

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
	assert.Equal(t, sessionID+"--agent-1", sub.SourceSessionID)
	assert.Equal(t, "junie", sub.Project)
	assert.Equal(t, "Audit the auth module for leaks", sub.FirstMessage)
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
		[]byte(`{"event":{"agentEvent":{"blob":"{\"currentDirectory\":\"/work/stateproj\",\"lastAgentState\":{\"issue\":{\"name\":\"State title\"}}}"}}}`), 0o600))

	sess, _ := parseJunieProviderSession(t, path, "local")
	assert.Equal(t, "/work/stateproj", sess.Cwd)
	assert.Equal(t, "stateproj", sess.Project)
	assert.Equal(t, "State title", sess.SessionName)
}

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
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-260101-120000-abcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"req-abc-123","prompt":"Hi","timestampMs":1704067200000}` + "\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))

	_, messages := parseJunieProviderSession(t, path, "local")
	require.Len(t, messages, 1)
	assert.Equal(t, "req-abc-123", messages[0].SourceUUID)
}

func TestJunieSubagentMultilinePromptTruncationAndFallback(t *testing.T) {
	root := t.TempDir()
	sessionID := "session-260101-120000-abcd"
	sessionDir := filepath.Join(root, sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	longRequest := strings.Repeat("Long line with words. ", 20) + "\\nSecond line\\nThird line"
	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Parent prompt","timestampMs":1704067200000}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"t1","event":{"agentEvent":{"kind":"MarkdownBlockUpdatedEvent","agent":{"kind":"BuiltInAgent","id":"agent-long","name":"long-worker"},"stepId":"long-req-request","text":"` + longRequest + `"}},"timestampMs":1704067200100}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"t1","event":{"agentEvent":{"kind":"ResultBlockUpdatedEvent","agent":{"kind":"BuiltInAgent","id":"agent-long","name":"long-worker"},"stepId":"long-res","result":"Done."}},"timestampMs":1704067200200}` + "\n" +
			// Second subagent with no request prompt — tests fallback to SessionName
			`{"kind":"SessionA2uxEvent","taskId":"t1","event":{"agentEvent":{"kind":"ResultBlockUpdatedEvent","agent":{"kind":"BuiltInAgent","id":"agent-noreq","name":"namer-worker"},"stepId":"noreq-res","result":"Done."}},"timestampMs":1704067200300}` + "\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))

	outcome := parseJunieProviderOutcome(t, path, "local")
	require.Len(t, outcome.Results, 3)

	longSub := outcome.Results[1].Result.Session
	assert.Equal(t, sessionID+"--agent-long", longSub.SourceSessionID)
	assert.NotContains(t, longSub.FirstMessage, "\n", "FirstMessage must be newline-flattened")
	assert.LessOrEqual(t, len(longSub.FirstMessage), 303, "FirstMessage must be truncated to 300 runes plus ellipsis")
	assert.True(t, strings.HasSuffix(longSub.FirstMessage, "..."))
	assert.True(t, strings.HasPrefix(longSub.FirstMessage, "Long line with words."))

	noreqSub := outcome.Results[2].Result.Session
	assert.Equal(t, sessionID+"--agent-noreq", noreqSub.SourceSessionID)
	assert.Equal(t, "namer-worker", noreqSub.FirstMessage, "FirstMessage must fall back to SessionName when no user prompt")
}

func TestJunieAskUserQuestionTitleFallback(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-260101-120000-abcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	// AskAsyncRequestUpdatedEvent with empty title, but question provided in request
	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Do work","timestampMs":1704067200000}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"t1","event":{"agentEvent":{"kind":"AskAsyncRequestUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"ask-1","request":{"question":"Which DB engine?","options":[{"title":"sqlite"}]}}},"timestampMs":1704067200100}` + "\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))

	_, messages := parseJunieProviderSession(t, path, "local")
	require.Len(t, messages, 2)
	assert.True(t, messages[1].HasToolUse)
	require.Len(t, messages[1].ToolCalls, 1)
	assert.Equal(t, "AskUserQuestion", messages[1].ToolCalls[0].ToolName)
	assert.Contains(t, messages[1].ToolCalls[0].InputJSON, "Which DB engine?")
}

func TestJunieResultBlockUpdatedEventAnswerWhitespaceTrim(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-260101-120000-abcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Explain","timestampMs":1704067200000}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"t1","event":{"agentEvent":{"kind":"ResultBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"res-1","result":"<!-- ANSWER -->\nClean answer."}},"timestampMs":1704067200100}` + "\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))

	_, messages := parseJunieProviderSession(t, path, "local")
	require.Len(t, messages, 2)
	assert.Equal(t, "Clean answer.", messages[1].Content)
}

func TestJunieSubagentEndedAtFallbackToFileMtime(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-notimestamp")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	events := []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Parent"}` + "\n" +
			`{"kind":"SessionA2uxEvent","taskId":"t1","event":{"agentEvent":{"kind":"MarkdownBlockUpdatedEvent","agent":{"kind":"BuiltInAgent","id":"agent-1","name":"worker"},"stepId":"w-request","text":"Work"}}}` + "\n",
	)
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, events, 0o600))

	mtime := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(path, mtime, mtime))

	outcome := parseJunieProviderOutcome(t, path, "local")
	require.Len(t, outcome.Results, 2)
	sub := outcome.Results[1].Result.Session
	assert.Equal(t, mtime, sub.EndedAt.UTC())
}
