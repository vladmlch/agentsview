package sync_test

// Multi-pass byte-stability tier for the claude JSONL lane (audit av-21).
// The reopen-convergence tier (av-20) proves the untouched and append-only
// paths converge; this tier covers the unstable-input cases that tier
// deliberately avoids — the source's stat identity moves while its content
// stays byte-identical, so every pass is a forced full re-parse of the same
// bytes:
//
//	unstable input   mtime-only touch, then a same-size in-place rewrite
//	                 with identical bytes: the derived stored content must
//	                 stay byte-identical to the cold-ingest baseline
//	                 (message rows: ordinals, roles, contents, models,
//	                 per-message token usage; session projection: counts,
//	                 file size, content hash, drop count; parser
//	                 checkpoint: offset, next ordinal, hash);
//	malformed drops  transcript with invalid-JSON lines interleaved
//	                 between valid messages, re-parsed after the same
//	                 unstable-input moves: the drop set must be
//	                 DETERMINISTIC — the same lines dropped on every pass
//	                 (pinned by byte-identical surviving message rows) and
//	                 equal drop counts on every pass.
//
// Only file_mtime is allowed to move: it is the instability trigger, not
// derived content. Everything the parse derives must be byte-identical
// across passes.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/sync"
	"go.kenn.io/agentsview/internal/testjsonl"
)

// claudeStabilitySnapshot is the per-session derived state whose
// byte-stability the tier pins. It deliberately omits FileMtime: the
// unstable-input moves change it by construction, and it is the input's
// stat identity, not derived content.
type claudeStabilitySnapshot struct {
	MessageCount   int
	UserMsgCount   int
	NextOrdinal    int
	DataVersion    int
	FileSize       int64
	FileHash       string
	MalformedLines int
	Messages       []reopenMessageKey
	SessionUsage   *db.SessionUsage

	HasCheckpoint         bool
	CheckpointOffset      int64
	CheckpointNextOrdinal int
	CheckpointHash        string
}

func snapshotClaudeStability(
	t *testing.T, env *reopenEnv, sessionID string,
) claudeStabilitySnapshot {
	t.Helper()
	sess, err := env.db.GetSessionFull(t.Context(), sessionID)
	require.NoError(t, err, "GetSessionFull(%q)", sessionID)
	require.NotNil(t, sess, "session %q not found in archive", sessionID)

	snap := claudeStabilitySnapshot{
		MessageCount:   sess.MessageCount,
		UserMsgCount:   sess.UserMessageCount,
		NextOrdinal:    sess.NextOrdinal,
		DataVersion:    sess.DataVersion,
		FileSize:       reopenDerefInt64(sess.FileSize),
		FileHash:       reopenDerefString(sess.FileHash),
		MalformedLines: sess.ParserMalformedLines,
		Messages:       nil,
		SessionUsage:   reopenSessionUsage(t, env, sessionID),
	}
	for _, m := range fetchMessages(t, env.db, sessionID) {
		snap.Messages = append(snap.Messages, reopenMessageKey{
			Ordinal:         m.Ordinal,
			Role:            m.Role,
			Content:         m.Content,
			Model:           m.Model,
			OutputTokens:    m.OutputTokens,
			HasOutputTokens: m.HasOutputTokens,
			ClaudeMessageID: m.ClaudeMessageID,
			ClaudeRequestID: m.ClaudeRequestID,
		})
	}
	cp, hasCheckpoint, err := env.db.GetParserCheckpoint(t.Context(), sessionID)
	require.NoError(t, err, "GetParserCheckpoint(%q)", sessionID)
	if hasCheckpoint {
		snap.HasCheckpoint = true
		snap.CheckpointOffset = cp.Offset
		snap.CheckpointNextOrdinal = cp.NextOrdinal
		snap.CheckpointHash = cp.Hash
	}
	return snap
}

// requireClaudeStabilitySnapshotEqual asserts the derived stored content
// is byte-identical across passes, and that the stored file size still
// matches the on-disk source size.
func requireClaudeStabilitySnapshotEqual(
	t *testing.T,
	want, got claudeStabilitySnapshot,
	sourcePath, sessionID, phase string,
) {
	t.Helper()
	assert.Equal(t, want, got,
		"%s: session %q derived stored content must be byte-identical "+
			"across passes", phase, sessionID)
	info, err := os.Stat(sourcePath)
	require.NoError(t, err, "stat source %s", sourcePath)
	assert.Equal(t, info.Size(), got.FileSize,
		"%s: session %q stored file_size must equal its source size",
		phase, sessionID)
}

// writeClaudeStabilityFixture writes one claude transcript under the
// reopenEnv source root and returns its path.
func writeClaudeStabilityFixture(
	t *testing.T, env *reopenEnv, name, content string,
) string {
	t.Helper()
	projectDir := filepath.Join(env.sourceDir, reopenClaudeProjectDir)
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	path := filepath.Join(projectDir, name+".jsonl")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644),
		"write claude transcript %s", name)
	return path
}

// touchFile moves only the mtime of path (same bytes, same size): the
// canonical unstable-input move that defeats the size+mtime skip gate
// without changing any content.
func touchFile(t *testing.T, path string, at time.Time) {
	t.Helper()
	require.NoError(t, os.Chtimes(path, at, at), "touch %s", path)
}

// rewriteSameBytes rewrites path with exactly the bytes it already held
// (same content, same size, new mtime and write): the same-size rewrite
// half of the unstable-input pair.
func rewriteSameBytes(t *testing.T, path string) {
	t.Helper()
	before, err := os.ReadFile(path)
	require.NoError(t, err, "read %s before rewrite", path)
	require.NoError(t, os.WriteFile(path, before, 0o644),
		"rewrite %s with identical bytes", path)
	after, err := os.ReadFile(path)
	require.NoError(t, err, "read %s after rewrite", path)
	require.Equal(t, before, after,
		"same-size rewrite must leave the bytes identical")
}

// claudeStabilityTranscript builds one clean billed-turn transcript.
func claudeStabilityTranscript(label string) string {
	return testjsonl.NewSessionBuilder().
		AddClaudeUser("2024-01-01T10:00:00Z", label+" first prompt",
			"/Users/alice/code/stability-lane").
		AddClaudeAssistantUsage("2024-01-01T10:00:01Z", label+" first answer",
			testjsonl.ClaudeAssistantUsage{
				MessageID:    "msg_" + label + "_1",
				RequestID:    "req_" + label + "_1",
				Model:        "claude-sonnet-4-5",
				InputTokens:  900,
				OutputTokens: 100,
			}).
		String()
}

// claudeStabilityMalformedTranscript builds a transcript with three
// invalid-JSON lines interleaved between four valid billed messages. The
// surviving content and the drop positions are exactly what the
// determinism phase pins across re-parses.
func claudeStabilityMalformedTranscript(label string) string {
	return testjsonl.NewSessionBuilder().
		AddClaudeUser("2024-01-01T10:00:00Z", label+" prompt one",
			"/Users/alice/code/stability-lane").
		AddRaw("}{ not json at all").
		AddClaudeAssistantUsage("2024-01-01T10:00:01Z", label+" answer one",
			testjsonl.ClaudeAssistantUsage{
				MessageID:    "msg_" + label + "_1",
				RequestID:    "req_" + label + "_1",
				Model:        "claude-sonnet-4-5",
				InputTokens:  900,
				OutputTokens: 100,
			}).
		AddRaw("{\"truncated\": ").
		AddClaudeUser("2024-01-01T10:00:02Z", label+" prompt two",
			"/Users/alice/code/stability-lane").
		AddRaw("]]}broken_tail").
		AddClaudeAssistantUsage("2024-01-01T10:00:03Z", label+" answer two",
			testjsonl.ClaudeAssistantUsage{
				MessageID:    "msg_" + label + "_2",
				RequestID:    "req_" + label + "_2",
				Model:        "claude-sonnet-4-5",
				InputTokens:  700,
				OutputTokens: 50,
			}).
		String()
}

// TestClaudeByteStabilityUnstableInput runs the unstable-input tier over
// the clean claude lane: cold ingest, mtime-only touch, same-size
// identical-bytes rewrite, and a reopen — with a sibling whose source is
// never touched, so each forced re-parse has exact hit counts (one synced
// session, one skipped) and the untouched sibling is never re-derived.
func TestClaudeByteStabilityUnstableInput(t *testing.T) {
	env := newReopenEnv(t, parser.AgentClaude)

	const (
		sessionOne = "stability-clean-one"
		sessionTwo = "stability-clean-two"
	)
	pathOne := writeClaudeStabilityFixture(
		t, env, sessionOne, claudeStabilityTranscript("one"))
	writeClaudeStabilityFixture(
		t, env, sessionTwo, claudeStabilityTranscript("two"))

	// Pass 1: cold ingest; the baseline every later pass must reproduce.
	runSyncAndAssert(t, env.engine,
		sync.SyncStats{TotalSessions: 2, Synced: 2})
	baseline := map[string]claudeStabilitySnapshot{
		sessionOne: snapshotClaudeStability(t, env, sessionOne),
		sessionTwo: snapshotClaudeStability(t, env, sessionTwo),
	}
	require.Len(t, baseline[sessionOne].Messages, 2,
		"cold ingest message rows")

	// Pass 2: mtime-only touch of session one (identical bytes, same
	// size). The skip gate must force exactly one re-parse, and the
	// re-derived stored content must be byte-identical to the baseline.
	touchedAt := time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)
	touchFile(t, pathOne, touchedAt)
	runSyncAndAssert(t, env.engine,
		sync.SyncStats{TotalSessions: 2, Synced: 1, Skipped: 1})
	afterTouch := map[string]claudeStabilitySnapshot{
		sessionOne: snapshotClaudeStability(t, env, sessionOne),
		sessionTwo: snapshotClaudeStability(t, env, sessionTwo),
	}
	requireClaudeStabilitySnapshotEqual(
		t, baseline[sessionOne], afterTouch[sessionOne],
		pathOne, sessionOne, "mtime touch")
	require.Equal(t, baseline[sessionTwo], afterTouch[sessionTwo],
		"the untouched sibling must not be re-derived by the touch")

	// Pass 3: same-size rewrite of session one with identical bytes.
	// Again exactly one forced re-parse, again byte-identical content.
	rewriteSameBytes(t, pathOne)
	runSyncAndAssert(t, env.engine,
		sync.SyncStats{TotalSessions: 2, Synced: 1, Skipped: 1})
	afterRewrite := map[string]claudeStabilitySnapshot{
		sessionOne: snapshotClaudeStability(t, env, sessionOne),
		sessionTwo: snapshotClaudeStability(t, env, sessionTwo),
	}
	requireClaudeStabilitySnapshotEqual(
		t, baseline[sessionOne], afterRewrite[sessionOne],
		pathOne, sessionOne, "same-size rewrite")
	require.Equal(t, baseline[sessionTwo], afterRewrite[sessionTwo],
		"the untouched sibling must not be re-derived by the rewrite")

	// Pass 4: reopen the archive (process restart) with both sources
	// untouched since pass 3: nothing re-derived.
	env.reopen(t)
	runSyncAndAssert(t, env.engine,
		sync.SyncStats{TotalSessions: 2, Skipped: 2})
	afterReopen := map[string]claudeStabilitySnapshot{
		sessionOne: snapshotClaudeStability(t, env, sessionOne),
		sessionTwo: snapshotClaudeStability(t, env, sessionTwo),
	}
	require.Equal(t, afterRewrite, afterReopen,
		"reopening after the completed rewrites must re-derive nothing")
}

// TestClaudeByteStabilityMalformedDropsDeterministic runs the same
// unstable-input moves over a transcript with invalid-JSON lines
// interleaved between valid messages: every forced re-parse must drop the
// SAME lines (pinned by byte-identical surviving message rows — a
// different drop set would shift the survivors' ordinals or contents) and
// must report the SAME drop count, on the session row and in the sync
// anomalies alike.
func TestClaudeByteStabilityMalformedDropsDeterministic(t *testing.T) {
	env := newReopenEnv(t, parser.AgentClaude)

	const sessionID = "stability-malformed"
	path := writeClaudeStabilityFixture(
		t, env, sessionID, claudeStabilityMalformedTranscript("drop"))

	malformedAnomalies := sync.SyncStats{
		TotalSessions: 1, Synced: 1,
		Anomalies: sync.AnomalyStats{
			MalformedLinesTotal:   3,
			MalformedLinesByAgent: map[string]int{"claude": 3},
		},
	}

	// Pass 1: cold ingest drops the three invalid lines.
	runSyncAndAssert(t, env.engine, malformedAnomalies)
	baseline := snapshotClaudeStability(t, env, sessionID)
	require.Len(t, baseline.Messages, 4,
		"the four valid messages must survive the drops")
	require.Equal(t, 3, baseline.MalformedLines,
		"the session row must record the cold-ingest drop count")
	require.Equal(t, "drop prompt one", baseline.Messages[0].Content)
	require.Equal(t, "drop answer one", baseline.Messages[1].Content)
	require.Equal(t, "drop prompt two", baseline.Messages[2].Content)
	require.Equal(t, "drop answer two", baseline.Messages[3].Content)

	// Pass 2: mtime-only touch forces a full re-parse of the same bytes.
	touchedAt := time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)
	touchFile(t, path, touchedAt)
	runSyncAndAssert(t, env.engine, malformedAnomalies)
	requireClaudeStabilitySnapshotEqual(
		t, baseline, snapshotClaudeStability(t, env, sessionID),
		path, sessionID, "malformed drop after mtime touch")

	// Pass 3: same-size identical-bytes rewrite forces another re-parse.
	rewriteSameBytes(t, path)
	runSyncAndAssert(t, env.engine, malformedAnomalies)
	requireClaudeStabilitySnapshotEqual(
		t, baseline, snapshotClaudeStability(t, env, sessionID),
		path, sessionID, "malformed drop after same-size rewrite")

	// Pass 4: reopen with the source untouched: nothing re-derived.
	env.reopen(t)
	runSyncAndAssert(t, env.engine,
		sync.SyncStats{TotalSessions: 1, Skipped: 1})
	require.Equal(t, baseline,
		snapshotClaudeStability(t, env, sessionID),
		"reopening after the completed re-parses must re-derive nothing")
}
