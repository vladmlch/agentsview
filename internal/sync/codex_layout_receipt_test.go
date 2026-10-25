package sync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/testjsonl"
)

// TestCodexLayoutReceiptWarnsOnLayoutChangeBetweenPasses runs two stable
// passes over a dated Codex archive and then flattens it (the flat↔dated
// rename hazard shape): the first observation and the stable pass must stay
// silent, and the drift pass must surface exactly one anomaly entry naming
// the root and the layout flip (audit av-19, validation-only receipt).
func TestCodexLayoutReceiptWarnsOnLayoutChangeBetweenPasses(t *testing.T) {
	const id = "019f0000-0000-7000-8000-00000000c0d1"
	root := t.TempDir()
	name := "rollout-2026-09-15T10-00-00-" + id + ".jsonl"
	content := testjsonl.JoinJSONL(
		testjsonl.CodexSessionMetaJSON(
			id, "/workspace/project", "user", "2026-09-15T10:00:00Z",
		),
		testjsonl.CodexMsgJSON("user", "hello", "2026-09-15T10:00:01Z"),
	)
	day := filepath.Join(root, "2026", "09", "15")
	require.NoError(t, os.MkdirAll(day, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(day, name), []byte(content), 0o600,
	))

	engine := NewEngine(t.Context(), openTestDB(t), EngineConfig{
		AgentDirs: map[parser.AgentType][]string{parser.AgentCodex: {root}},
		Machine:   "local",
	})
	t.Cleanup(engine.Close)

	stats := engine.SyncAll(t.Context(), nil)
	assert.Zero(t, stats.Anomalies.LayoutChangesTotal,
		"the first observation receipts an INFO, not a change")
	assert.Empty(t, stats.Anomalies.LayoutChangeSamples)

	// A stable second pass over the unchanged layout stays silent.
	stats = engine.SyncAll(t.Context(), nil)
	assert.Zero(t, stats.Anomalies.LayoutChangesTotal)
	assert.True(t, stats.Anomalies.IsZero())

	// Flatten the archive: the root's observed layout set flips.
	require.NoError(t, os.Rename(
		filepath.Join(day, name), filepath.Join(root, name),
	))
	require.NoError(t, os.RemoveAll(filepath.Join(root, "2026")))

	stats = engine.SyncAll(t.Context(), nil)
	require.Equal(t, 1, stats.Anomalies.LayoutChangesTotal)
	require.Len(t, stats.Anomalies.LayoutChangeSamples, 1)
	change := stats.Anomalies.LayoutChangeSamples[0]
	assert.Equal(t, parser.AgentCodex, change.Agent)
	assert.Equal(t, root, change.Root)
	assert.Equal(t, "dated", change.From)
	assert.Equal(t, "flat", change.To)
	assert.False(t, stats.Anomalies.IsZero())
}

// TestCodexLayoutReceiptStableMixedRootStaysSilent pins that a root which
// legitimately holds live dated transcripts and flat archived copies reads
// as one stable "flat+dated" observation: no drift is reported even though
// discovery sees both layouts on every pass.
func TestCodexLayoutReceiptStableMixedRootStaysSilent(t *testing.T) {
	const id = "019f0000-0000-7000-8000-00000000c0d2"
	root := t.TempDir()
	name := "rollout-2026-09-15T10-00-00-" + id + ".jsonl"
	content := testjsonl.JoinJSONL(
		testjsonl.CodexSessionMetaJSON(
			id, "/workspace/project", "user", "2026-09-15T10:00:00Z",
		),
		testjsonl.CodexMsgJSON("user", "hello", "2026-09-15T10:00:01Z"),
	)
	day := filepath.Join(root, "2026", "09", "15")
	require.NoError(t, os.MkdirAll(day, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(day, name), []byte(content), 0o600,
	))
	// A flat archived copy of a different session alongside the dated one.
	archivedID := "019f0000-0000-7000-8000-00000000c0d3"
	require.NoError(t, os.WriteFile(
		filepath.Join(root,
			"rollout-2026-01-02T08-00-00-"+archivedID+".jsonl"),
		[]byte(content), 0o600,
	))

	engine := NewEngine(t.Context(), openTestDB(t), EngineConfig{
		AgentDirs: map[parser.AgentType][]string{parser.AgentCodex: {root}},
		Machine:   "local",
	})
	t.Cleanup(engine.Close)

	for range 2 {
		stats := engine.SyncAll(t.Context(), nil)
		assert.Zero(t, stats.Anomalies.LayoutChangesTotal,
			"a stable mixed root is not layout drift")
		assert.Empty(t, stats.Anomalies.LayoutChangeSamples)
		assert.True(t, stats.Anomalies.IsZero())
	}
}
