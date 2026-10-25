package sync_test

// Reopen-convergence tier (audit av-20). Neither target had cass's
// end-to-end re-ingest harness shape: reopen×2 + source append + exact hit
// counts + byte conservation + a negative control. This file ports that
// shape to the agentsview engine as a regression-value suite over real
// fixture trees and a real archive database:
//
//	pass 1  cold ingest of untouched fixtures; record exact totals
//	        (sessions, messages, per-message token usage, data
//	        versions, file sizes, stored hashes, checkpoint offsets);
//	reopen  the engine and its database handles are closed and a fresh
//	        engine reopens the same archive path (process-restart
//	        semantics, not just a second SyncAll on warm state);
//	pass 2  sync again WITHOUT touching the sources — the negative
//	        control: zero re-ingested sessions, and every recorded
//	        observable byte-identical to pass 1;
//	pass 3  append to exactly one source (the JSONL lanes are real
//	        WAL-style append-only logs) and sync again: the hit counts
//	        must be exact (one synced session, every sibling skipped),
//	        the delta must be exactly the appended content, and every
//	        non-appended session must stay byte-identical;
//	reopen  again after the completed append;
//	pass 4  sync once more: totals must be stable (idempotent recovery:
//	        reopening after a completed append re-derives nothing).
//
// Byte conservation is checked at every phase: each session's stored
// file_size and parser-checkpoint offset must equal the on-disk size of its
// source, so the indexed byte total always equals the source byte total.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/dbtest"
	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/sync"
	"go.kenn.io/agentsview/internal/testjsonl"
)

// reopenEnv is one lane's harness: a real fixture source tree plus a real
// archive database at a known path, so the engine can be closed and
// reopened over the same data.
type reopenEnv struct {
	agent     parser.AgentType
	sourceDir string
	dbPath    string
	db        *db.DB
	engine    *sync.Engine
}

func newReopenEnv(t *testing.T, agent parser.AgentType) *reopenEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	env := &reopenEnv{
		agent:     agent,
		sourceDir: t.TempDir(),
		dbPath:    filepath.Join(t.TempDir(), "reopen-archive.db"),
	}
	t.Cleanup(func() {
		if env.engine != nil {
			env.engine.Close()
		}
		if env.db != nil {
			_ = env.db.Close()
		}
	})
	env.open(t)
	return env
}

// open creates fresh engine and database handles over the same archive
// path. The sources are never touched by reopen.
func (e *reopenEnv) open(t *testing.T) {
	t.Helper()
	dbtest.EnsureTestDBAt(t, e.dbPath)
	database, err := db.Open(t.Context(), e.dbPath)
	require.NoError(t, err, "open archive database")
	e.db = database
	e.engine = sync.NewEngine(t.Context(), database, sync.EngineConfig{
		AgentDirs: map[parser.AgentType][]string{
			e.agent: {e.sourceDir},
		},
		Machine: "local",
	})
}

// reopen simulates a process restart: the running engine and its database
// handles are closed, then a fresh engine opens the same archive path.
func (e *reopenEnv) reopen(t *testing.T) {
	t.Helper()
	e.engine.Close()
	require.NoError(t, e.db.Close(), "close archive database before reopen")
	e.open(t)
}

// --- per-session observables ---

type reopenMessageKey struct {
	Ordinal         int
	Role            string
	Content         string
	Model           string
	OutputTokens    int
	HasOutputTokens bool
	// Claude billed-turn identity; empty for other lanes.
	ClaudeMessageID string
	ClaudeRequestID string
}

type reopenUsageKey struct {
	Source                   string
	Model                    string
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

// reopenSessionSnapshot is the exact per-session state whose stability the
// tier pins: projection counters, per-message identity (including
// per-message token usage), per-usage-event identity, sync bookkeeping
// (file size/mtime, next ordinal, stored content hash, data version), and
// the parser checkpoint's committed prefix (offset, ordinal, hash).
type reopenSessionSnapshot struct {
	MessageCount int
	UserMsgCount int
	OutputTokens int
	DataVersion  int
	FileSize     int64
	FileMtime    int64
	NextOrdinal  int
	FileHash     string
	Messages     []reopenMessageKey
	Usage        []reopenUsageKey
	// SessionUsage is the canonical session-usage projection (including
	// its per-message breakdown) at snapshot time.
	SessionUsage *db.SessionUsage

	HasCheckpoint         bool
	CheckpointOffset      int64
	CheckpointNextOrdinal int
	CheckpointHash        string
}

func snapshotReopenSessions(
	t *testing.T, env *reopenEnv, sessionIDs []string,
) map[string]reopenSessionSnapshot {
	t.Helper()
	snaps := make(map[string]reopenSessionSnapshot, len(sessionIDs))
	for _, id := range sessionIDs {
		sess, err := env.db.GetSessionFull(t.Context(), id)
		require.NoError(t, err, "GetSessionFull(%q)", id)
		require.NotNil(t, sess, "session %q not found in archive", id)

		msgs := fetchMessages(t, env.db, id)
		messageKeys := make([]reopenMessageKey, 0, len(msgs))
		for _, m := range msgs {
			messageKeys = append(messageKeys, reopenMessageKey{
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
		events, err := env.db.GetUsageEvents(t.Context(), id)
		require.NoError(t, err, "GetUsageEvents(%q)", id)
		usageKeys := make([]reopenUsageKey, 0, len(events))
		for _, ev := range events {
			usageKeys = append(usageKeys, reopenUsageKey{
				Source:                   ev.Source,
				Model:                    ev.Model,
				InputTokens:              ev.InputTokens,
				OutputTokens:             ev.OutputTokens,
				CacheCreationInputTokens: ev.CacheCreationInputTokens,
				CacheReadInputTokens:     ev.CacheReadInputTokens,
			})
		}

		snap := reopenSessionSnapshot{
			MessageCount: sess.MessageCount,
			UserMsgCount: sess.UserMessageCount,
			OutputTokens: sess.TotalOutputTokens,
			DataVersion:  sess.DataVersion,
			FileSize:     reopenDerefInt64(sess.FileSize),
			FileMtime:    reopenDerefInt64(sess.FileMtime),
			NextOrdinal:  sess.NextOrdinal,
			FileHash:     reopenDerefString(sess.FileHash),
			Messages:     messageKeys,
			Usage:        usageKeys,
			SessionUsage: reopenSessionUsage(t, env, id),
		}
		cp, hasCheckpoint, err := env.db.GetParserCheckpoint(t.Context(), id)
		require.NoError(t, err, "GetParserCheckpoint(%q)", id)
		if hasCheckpoint {
			snap.HasCheckpoint = true
			snap.CheckpointOffset = cp.Offset
			snap.CheckpointNextOrdinal = cp.NextOrdinal
			snap.CheckpointHash = cp.Hash
		}
		snaps[id] = snap
	}
	return snaps
}

// reopenSessionUsage reads the canonical session-usage projection for one
// session. Codex and claude are PerMessageTokenUsage providers: neither
// emits aggregate usage_events rows, so token totals are observable here
// (and on the message rows), not through GetUsageEvents.
func reopenSessionUsage(
	t *testing.T, env *reopenEnv, sessionID string,
) *db.SessionUsage {
	t.Helper()
	usage, err := env.db.GetSessionUsage(t.Context(), sessionID, true)
	require.NoError(t, err, "GetSessionUsage(%q)", sessionID)
	require.NotNil(t, usage, "session usage for %q", sessionID)
	return usage
}

// requireReopenByteConservation pins the conservation invariant for the
// one-file-per-session JSONL lanes: a session's stored file_size and (when
// present) its parser-checkpoint offset must equal the on-disk size of its
// source, so the indexed byte total equals the source byte total at every
// phase of the tier.
func requireReopenByteConservation(
	t *testing.T,
	sourcePaths map[string]string,
	snaps map[string]reopenSessionSnapshot,
	lane string,
) {
	t.Helper()
	var totalStored, totalSource int64
	for id, path := range sourcePaths {
		snap, ok := snaps[id]
		require.True(t, ok, "%s lane: snapshot for %q missing", lane, id)
		info, err := os.Stat(path)
		require.NoError(t, err, "stat source %s", path)
		assert.Equal(t, info.Size(), snap.FileSize,
			"%s lane: session %q stored file_size must equal its source size",
			lane, id)
		if snap.HasCheckpoint {
			assert.Equal(t, info.Size(), snap.CheckpointOffset,
				"%s lane: session %q checkpoint offset must equal its source size",
				lane, id)
		}
		totalStored += snap.FileSize
		totalSource += info.Size()
	}
	assert.Equal(t, totalSource, totalStored,
		"%s lane: total indexed source bytes must be conserved", lane)
}

// reopenTierPlan is one lane's script for the shared tier driver.
type reopenTierPlan struct {
	lane string
	// sessionIDs lists every session the fixtures must produce, and
	// sourcePaths maps each of them to its one source file.
	sessionIDs  []string
	sourcePaths map[string]string
	// appendedID names the single session whose source receives
	// appendBytes in phase 3.
	appendedID  string
	appendBytes string
	// pass1..pass4 are the exact SyncStats each phase must produce.
	pass1, pass2, pass3, pass4 sync.SyncStats
}

// runReopenConvergenceTier drives the four-phase reopen×2 + append tier
// and returns the pass-1 baseline and pass-3 post-append snapshots so the
// lane test can pin the exact per-session delta.
func runReopenConvergenceTier(
	t *testing.T, env *reopenEnv, plan reopenTierPlan,
) (baseline, appended map[string]reopenSessionSnapshot) {
	t.Helper()

	// Phase 1: cold ingest of the untouched fixtures. Record exact totals.
	runSyncAndAssert(t, env.engine, plan.pass1)
	baseline = snapshotReopenSessions(t, env, plan.sessionIDs)
	require.Len(t, baseline, len(plan.sessionIDs),
		"%s lane: every fixture session must be archived", plan.lane)
	requireReopenByteConservation(
		t, plan.sourcePaths, baseline, plan.lane)

	// Phase 2: reopen, then sync WITHOUT touching the sources. Negative
	// control: unchanged sources must produce zero re-ingested sessions
	// and must not re-derive any recorded observable.
	env.reopen(t)
	runSyncAndAssert(t, env.engine, plan.pass2)
	unchanged := snapshotReopenSessions(t, env, plan.sessionIDs)
	require.Equal(t, baseline, unchanged,
		"%s lane: pass 2 over unchanged sources must not re-derive any session",
		plan.lane)

	// Phase 3: append to exactly one source, then sync. The hit counts
	// are exact (one synced session, siblings skipped), the checkpoint
	// offset must advance by exactly the appended bytes, and no sibling
	// session may be re-derived.
	before := reopenFileSize(t, plan.sourcePaths[plan.appendedID])
	f, err := os.OpenFile(plan.sourcePaths[plan.appendedID],
		os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err, "open source for append")
	_, err = f.WriteString(plan.appendBytes)
	require.NoError(t, err, "append to source")
	require.NoError(t, f.Close(), "close source after append")
	require.Equal(t, before+int64(len(plan.appendBytes)),
		reopenFileSize(t, plan.sourcePaths[plan.appendedID]),
		"%s lane: appended byte count must equal the source growth",
		plan.lane)

	runSyncAndAssert(t, env.engine, plan.pass3)
	appended = snapshotReopenSessions(t, env, plan.sessionIDs)
	for _, id := range plan.sessionIDs {
		if id == plan.appendedID {
			continue
		}
		require.Equal(t, baseline[id], appended[id],
			"%s lane: sibling session %q must not be re-derived by %q's append",
			plan.lane, id, plan.appendedID)
	}
	grown := appended[plan.appendedID]
	require.Equal(t, baseline[plan.appendedID].DataVersion, grown.DataVersion,
		"%s lane: an append must not change the session's data version",
		plan.lane)
	requireReopenByteConservation(
		t, plan.sourcePaths, appended, plan.lane)

	// Phase 4: reopen again after the completed append. Idempotent
	// recovery: the same totals must come back with nothing re-ingested.
	env.reopen(t)
	runSyncAndAssert(t, env.engine, plan.pass4)
	stable := snapshotReopenSessions(t, env, plan.sessionIDs)
	require.Equal(t, appended, stable,
		"%s lane: reopening after a completed append must re-derive nothing",
		plan.lane)
	requireReopenByteConservation(t, plan.sourcePaths, stable, plan.lane)

	return baseline, appended
}

func reopenFileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err, "stat %s", path)
	return info.Size()
}

func reopenDerefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func reopenDerefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// --- codex JSONL lane ---

const (
	reopenCodexUUIDOne = "019f4c2e-7ac1-7cc1-9f21-000000000001"
	reopenCodexUUIDTwo = "019f4c2e-7ac1-7cc1-9f21-000000000002"
)

// reopenCodexTranscript is one cold codex rollout: session meta, turn
// context, one user/assistant turn, and one token_count event.
func reopenCodexTranscript(uuid string) string {
	return testjsonl.JoinJSONL(
		testjsonl.CodexSessionMetaJSON(
			uuid, "/workspace/reopen-lane", "codex_cli_rs",
			"2024-01-01T10:00:00Z",
		),
		testjsonl.CodexTurnContextJSON("gpt-5.4", "2024-01-01T10:00:01Z"),
		testjsonl.CodexMsgJSON("user", uuid+" first prompt", "2024-01-01T10:00:02Z"),
		testjsonl.CodexMsgJSON("assistant", uuid+" first answer", "2024-01-01T10:00:03Z"),
		testjsonl.CodexTokenCountJSON("2024-01-01T10:00:04Z", 120, 40, 80),
	)
}

// reopenCodexAppend is the WAL-style tail appended to the first rollout in
// phase 3: one more user/assistant turn plus its token_count event.
func reopenCodexAppend() string {
	return testjsonl.JoinJSONL(
		testjsonl.CodexMsgJSON("user", "second prompt", "2024-01-01T10:00:05Z"),
		testjsonl.CodexMsgJSON("assistant", "second answer", "2024-01-01T10:00:06Z"),
		testjsonl.CodexTokenCountJSON("2024-01-01T10:00:07Z", 30, 10, 5),
	)
}

func writeReopenCodexFixture(
	t *testing.T, root, uuid, content string,
) string {
	t.Helper()
	day := filepath.Join(root, "2024", "01", "01")
	require.NoError(t, os.MkdirAll(day, 0o755))
	path := filepath.Join(
		day, "rollout-2024-01-01T10-00-00-"+uuid+".jsonl",
	)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644),
		"write codex rollout %s", uuid)
	return path
}

// TestReopenConvergenceCodexJSONL runs the full tier over the codex JSONL
// lane, the lane whose parser-checkpoint offset makes byte conservation
// exactly observable. The phase-3 delta must be exactly the appended turn:
// two messages, one usage event (30/10/5 tokens), and a checkpoint offset
// that advanced by exactly the appended byte count.
func TestReopenConvergenceCodexJSONL(t *testing.T) {
	env := newReopenEnv(t, parser.AgentCodex)

	pathOne := writeReopenCodexFixture(
		t, env.sourceDir, reopenCodexUUIDOne, reopenCodexTranscript(reopenCodexUUIDOne),
	)
	pathTwo := writeReopenCodexFixture(
		t, env.sourceDir, reopenCodexUUIDTwo, reopenCodexTranscript(reopenCodexUUIDTwo),
	)
	sessionOne := "codex:" + reopenCodexUUIDOne
	sessionTwo := "codex:" + reopenCodexUUIDTwo

	baseline, appended := runReopenConvergenceTier(t, env, reopenTierPlan{
		lane: "codex",
		sessionIDs: []string{
			sessionOne, sessionTwo,
		},
		sourcePaths: map[string]string{
			sessionOne: pathOne,
			sessionTwo: pathTwo,
		},
		appendedID:  sessionOne,
		appendBytes: reopenCodexAppend(),
		pass1:       sync.SyncStats{TotalSessions: 2, Synced: 2},
		pass2:       sync.SyncStats{TotalSessions: 2, Skipped: 2},
		pass3:       sync.SyncStats{TotalSessions: 2, Synced: 1, Skipped: 1},
		pass4:       sync.SyncStats{TotalSessions: 2, Skipped: 2},
	})

	// Cold-ingest shape: one file, one session, two messages, a live
	// checkpoint at the exact end of the file. Codex is a
	// PerMessageTokenUsage provider: the token_count event lands on the
	// assistant message and the session-usage projection, and no
	// aggregate usage_events row exists.
	for _, id := range []string{sessionOne, sessionTwo} {
		snap := baseline[id]
		require.True(t, snap.HasCheckpoint,
			"a cold codex sync must commit a parser checkpoint")
		require.Equal(t, 2, snap.MessageCount, "session %q message count", id)
		require.Len(t, snap.Messages, 2)
		require.Empty(t, snap.Usage,
			"codex usage is per-message; usage_events must stay empty")
		assistant := snap.Messages[1]
		require.Equal(t, "assistant", assistant.Role)
		require.True(t, assistant.HasOutputTokens,
			"session %q assistant message token presence", id)
		require.Equal(t, 40, assistant.OutputTokens,
			"session %q assistant message output tokens", id)
		usage := snap.SessionUsage
		assert.Equal(t, 40, usage.TotalOutputTokens,
			"session %q total output tokens", id)
		assert.Equal(t, 120, usage.PeakContextTokens,
			"session %q peak context tokens", id)
		require.Len(t, usage.Breakdown, 1, "session %q usage breakdown", id)
		assert.Equal(t, 40, usage.Breakdown[0].InputTokens,
			"session %q billed input tokens (reported input minus cached)",
			id)
		assert.Equal(t, 80, usage.Breakdown[0].CacheReadInputTokens,
			"session %q cached input tokens", id)
		assert.Equal(t, 40, usage.Breakdown[0].OutputTokens,
			"session %q breakdown output tokens", id)
	}

	// Exact phase-3 delta on the appended session: the two appended
	// messages, the one appended token_count event, and the checkpoint
	// advanced by exactly the appended bytes — nothing re-parsed.
	grown := appended[sessionOne]
	require.Equal(t, baseline[sessionOne].MessageCount+2, grown.MessageCount,
		"appended session message count")
	require.Len(t, grown.Messages, baseline[sessionOne].MessageCount+2)
	require.Equal(t,
		baseline[sessionOne].Messages, grown.Messages[:len(baseline[sessionOne].Messages)],
		"existing codex messages must be preserved in place by the append")
	require.Equal(t, "user", grown.Messages[2].Role)
	require.Equal(t, "second prompt", grown.Messages[2].Content)
	require.Equal(t, "assistant", grown.Messages[3].Role)
	require.Equal(t, "second answer", grown.Messages[3].Content)
	require.Empty(t, grown.Usage,
		"an append must not fabricate aggregate usage_events rows")
	usage := appended[sessionOne].SessionUsage
	assert.Equal(t, 50, usage.TotalOutputTokens,
		"total output tokens must grow by exactly the appended turn")
	assert.Equal(t, 120, usage.PeakContextTokens,
		"peak context tokens must stay at the cold-ingest maximum")
	require.Len(t, usage.Breakdown, 2, "usage breakdown rows")
	assert.Equal(t, 25, usage.Breakdown[1].InputTokens,
		"appended billed input tokens (30 reported minus 5 cached)")
	assert.Equal(t, 5, usage.Breakdown[1].CacheReadInputTokens,
		"appended cached input tokens")
	assert.Equal(t, 10, usage.Breakdown[1].OutputTokens,
		"appended output tokens")
	require.Equal(t,
		baseline[sessionOne].CheckpointOffset+int64(len(reopenCodexAppend())),
		grown.CheckpointOffset,
		"checkpoint offset must advance by exactly the appended bytes")
}

// --- claude JSONL lane ---

const reopenClaudeProjectDir = "reopen-proj"

// reopenClaudeTranscript is one cold claude transcript: one billed
// user/assistant turn.
func reopenClaudeTranscript(label string) string {
	return testjsonl.NewSessionBuilder().
		AddClaudeUser("2024-01-01T10:00:00Z", label+" first prompt",
			"/Users/alice/code/reopen-lane").
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

// reopenClaudeAppend is the WAL-style tail appended to the first claude
// transcript in phase 3: one more billed turn.
func reopenClaudeAppend(label string) string {
	return testjsonl.NewSessionBuilder().
		AddClaudeUser("2024-01-01T10:00:02Z", label+" second prompt",
			"/Users/alice/code/reopen-lane").
		AddClaudeAssistantUsage("2024-01-01T10:00:03Z", label+" second answer",
			testjsonl.ClaudeAssistantUsage{
				MessageID:    "msg_" + label + "_2",
				RequestID:    "req_" + label + "_2",
				Model:        "claude-sonnet-4-5",
				InputTokens:  700,
				OutputTokens: 50,
			}).
		String()
}

func writeReopenClaudeFixture(
	t *testing.T, root, name, content string,
) string {
	t.Helper()
	projectDir := filepath.Join(root, reopenClaudeProjectDir)
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	path := filepath.Join(projectDir, name+".jsonl")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644),
		"write claude transcript %s", name)
	return path
}

// TestReopenConvergenceClaudeJSONL runs the same tier over the claude JSONL
// lane: session IDs are filename stems, and the phase-3 delta must be
// exactly the appended billed turn.
func TestReopenConvergenceClaudeJSONL(t *testing.T) {
	env := newReopenEnv(t, parser.AgentClaude)

	pathOne := writeReopenClaudeFixture(
		t, env.sourceDir, "reopen-claude-one", reopenClaudeTranscript("one"),
	)
	pathTwo := writeReopenClaudeFixture(
		t, env.sourceDir, "reopen-claude-two", reopenClaudeTranscript("two"),
	)
	sessionOne := "reopen-claude-one"
	sessionTwo := "reopen-claude-two"

	baseline, appended := runReopenConvergenceTier(t, env, reopenTierPlan{
		lane: "claude",
		sessionIDs: []string{
			sessionOne, sessionTwo,
		},
		sourcePaths: map[string]string{
			sessionOne: pathOne,
			sessionTwo: pathTwo,
		},
		appendedID:  sessionOne,
		appendBytes: reopenClaudeAppend("one"),
		pass1:       sync.SyncStats{TotalSessions: 2, Synced: 2},
		pass2:       sync.SyncStats{TotalSessions: 2, Skipped: 2},
		pass3:       sync.SyncStats{TotalSessions: 2, Synced: 1, Skipped: 1},
		pass4:       sync.SyncStats{TotalSessions: 2, Skipped: 2},
	})

	// Cold-ingest shape: two messages, one billed assistant turn, project
	// derived from the transcript cwd. Claude is a PerMessageTokenUsage
	// provider: usage lives on the assistant message rows and the
	// session-usage projection, and no aggregate usage_events row exists.
	for _, id := range []string{sessionOne, sessionTwo} {
		snap := baseline[id]
		require.Equal(t, 2, snap.MessageCount, "session %q message count", id)
		require.Len(t, snap.Messages, 2)
		require.Empty(t, snap.Usage,
			"claude usage is per-message; usage_events must stay empty")
		assistant := snap.Messages[1]
		require.Equal(t, "assistant", assistant.Role)
		require.Equal(t, "claude-sonnet-4-5", assistant.Model,
			"session %q assistant message model", id)
		require.True(t, assistant.HasOutputTokens,
			"session %q assistant message token presence", id)
		require.Equal(t, 100, assistant.OutputTokens,
			"session %q assistant message output tokens", id)
		usage := snap.SessionUsage
		assert.Equal(t, 100, usage.TotalOutputTokens,
			"session %q total output tokens", id)
		assert.Equal(t, 900, usage.PeakContextTokens,
			"session %q peak context tokens", id)
		require.Len(t, usage.Breakdown, 1, "session %q usage breakdown", id)
		assert.Equal(t, "claude-sonnet-4-5", usage.Breakdown[0].Model,
			"session %q breakdown model", id)
		assert.Equal(t, 900, usage.Breakdown[0].InputTokens,
			"session %q input tokens", id)
		assert.Equal(t, 100, usage.Breakdown[0].OutputTokens,
			"session %q breakdown output tokens", id)
	}
	assertSessionProject(t, env.db, sessionOne, "reopen_lane")

	// Exact phase-3 delta on the appended session.
	grown := appended[sessionOne]
	require.Equal(t, baseline[sessionOne].MessageCount+2, grown.MessageCount,
		"appended session message count")
	require.Len(t, grown.Messages, baseline[sessionOne].MessageCount+2)
	require.Equal(t,
		baseline[sessionOne].Messages, grown.Messages[:len(baseline[sessionOne].Messages)],
		"existing claude messages must be preserved in place by the append")
	require.Equal(t, "user", grown.Messages[2].Role)
	require.Equal(t, "one second prompt", grown.Messages[2].Content)
	require.Equal(t, "assistant", grown.Messages[3].Role)
	require.Equal(t, "one second answer", grown.Messages[3].Content)
	require.Equal(t, "msg_one_2", grown.Messages[3].ClaudeMessageID,
		"appended assistant message id")
	require.Equal(t, "req_one_2", grown.Messages[3].ClaudeRequestID,
		"appended assistant request id")
	require.Empty(t, grown.Usage,
		"an append must not fabricate aggregate usage_events rows")
	usage := appended[sessionOne].SessionUsage
	assert.Equal(t, 150, usage.TotalOutputTokens,
		"total output tokens must grow by exactly the appended turn")
	assert.Equal(t, 900, usage.PeakContextTokens,
		"peak context tokens must stay at the cold-ingest maximum")
	require.Len(t, usage.Breakdown, 2, "usage breakdown rows")
	assert.Equal(t, 700, usage.Breakdown[1].InputTokens,
		"appended input tokens")
	assert.Equal(t, 50, usage.Breakdown[1].OutputTokens,
		"appended output tokens")
}
