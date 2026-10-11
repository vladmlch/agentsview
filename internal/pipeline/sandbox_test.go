//go:build fts5

package pipeline

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/pipevents"
	"go.kenn.io/agentsview/internal/testjsonl"
)

// firstStageIndex returns the index of the first event produced by
// stage, or -1 when the stage never appeared.
func firstStageIndex(evs []pipevents.Event, stage pipevents.Stage) int {
	for i, e := range evs {
		if e.Stage == stage {
			return i
		}
	}
	return -1
}

// TestSandboxRunHappyPath is the linchpin check for the whole
// playground: a scenario file write must travel the real ingest path —
// FSEvents watcher → batch → gates → parse → archive write — and land
// both in the run's event recording and in the sandbox SQLite archive.
func TestSandboxRunHappyPath(t *testing.T) {
	ctx := t.Context()
	dataDir := t.TempDir()
	hub := NewHub(filepath.Join(dataDir, "recordings"))

	sb, err := OpenSandbox(ctx, dataDir, hub)
	require.NoError(t, err)
	t.Cleanup(sb.Close)

	require.Equal(t, filepath.Join(dataDir, "pipeline-sandbox"), sb.Root)
	require.Equal(t, filepath.Join(sb.Root, "sessions"), sb.SrcDir)
	require.Equal(t, sb.Root, sb.MetaDir)

	const uuid = "019eb791-cf7d-75c1-8439-9ed74c122b05"
	fixture := testjsonl.JoinJSONL(
		testjsonl.CodexSessionMetaJSON(
			uuid, "/workspace/project-a", "codex_cli_rs",
			"2024-01-01T10:00:00Z",
		),
		testjsonl.CodexTurnContextJSON(
			"gpt-5.4", "2024-01-01T10:00:01Z",
		),
		testjsonl.CodexMsgJSON(
			"user", "run the suite", "2024-01-01T10:00:02Z",
		),
		testjsonl.CodexMsgJSON(
			"assistant", "all green", "2024-01-01T10:00:03Z",
		),
	)
	relPath := filepath.Join(
		"2024", "01", "01",
		"rollout-2024-01-01T10-00-00-"+uuid+".jsonl",
	)
	sc := &Scenario{
		ID:      "sandbox-happy-path",
		Fixture: relPath,
		Steps: []Step{
			func(ctx context.Context, rc *RunContext) error {
				return rc.WriteFile(relPath, fixture)
			},
			func(ctx context.Context, rc *RunContext) error {
				return rc.WaitIdle(0)
			},
		},
	}
	runID, err := sb.Run(ctx, sc)
	require.NoError(t, err)
	require.NotEmpty(t, runID)

	// The write-file step returns before the watcher dispatches; poll
	// the run recording until the archive write commits. The FSEvents
	// round trip gets the full seconds-scale budget, never a
	// sub-second shortcut.
	var evs []pipevents.Event
	require.Eventually(t, func() bool {
		evs = hub.RunEvents(runID)
		for _, e := range evs {
			if e.Kind == pipevents.KindTxCommitted {
				return true
			}
		}
		return false
	}, 10*time.Second, 100*time.Millisecond,
		"run %s never produced tx_committed", runID)

	// Event order must reflect the pipeline: the watch stage observes
	// the batch before gates, gates before parse, parse before write.
	watchIdx := firstStageIndex(evs, pipevents.StageWatch)
	gatesIdx := firstStageIndex(evs, pipevents.StageGates)
	parseIdx := firstStageIndex(evs, pipevents.StageParse)
	writeIdx := firstStageIndex(evs, pipevents.StageWrite)
	for name, idx := range map[string]int{
		"watch": watchIdx, "gates": gatesIdx,
		"parse": parseIdx, "write": writeIdx,
	} {
		require.NotEqual(t, -1, idx,
			"no %s-stage event in run %s", name, runID)
	}
	assert.Less(t, watchIdx, gatesIdx, "watch stage precedes gates")
	assert.Less(t, gatesIdx, parseIdx, "gates stage precedes parse")
	assert.Less(t, parseIdx, writeIdx, "parse stage precedes write")

	// The runner marked the scripted actions with scenario_step
	// events inside the same run window.
	var stepActions []string
	for _, e := range evs {
		if e.Kind == pipevents.KindScenarioStep {
			action, _ := e.Payload["action"].(string)
			stepActions = append(stepActions, action)
		}
	}
	assert.Equal(t, []string{"write-file", "wait-idle"}, stepActions)

	// The sandbox archive holds the ingested session under its codex
	// id — the engine really wrote, not just reported events.
	page, err := sb.db.ListSessions(ctx, db.SessionFilter{Agent: "codex"})
	require.NoError(t, err)
	assert.Equal(t, 1, page.Total, "sandbox archive session count")

	sess, err := sb.db.GetSessionFull(ctx, "codex:"+uuid)
	require.NoError(t, err)
	if assert.NotNil(t, sess, "session codex:%s not archived", uuid) {
		assert.Equal(t, "codex", sess.Agent)
	}
}
