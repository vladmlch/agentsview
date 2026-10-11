package sync_test

import (
	"os"
	"path/filepath"
	gosync "sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/dbtest"
	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/pipevents"
	"go.kenn.io/agentsview/internal/sync"
	"go.kenn.io/agentsview/internal/testjsonl"
)

// collectingObs records every pipeline event the engine emits.
type collectingObs struct {
	mu  gosync.Mutex
	evs []pipevents.Event
}

func (c *collectingObs) ObserveEvent(e pipevents.Event) {
	c.mu.Lock()
	c.evs = append(c.evs, e)
	c.mu.Unlock()
}

func (c *collectingObs) events() []pipevents.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]pipevents.Event(nil), c.evs...)
}

func (c *collectingObs) kinds() []string {
	evs := c.events()
	kinds := make([]string, 0, len(evs))
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

// assertOrderedSubsequence requires each wanted kind to appear in kinds at or
// after the position of the previous match, in order.
func assertOrderedSubsequence(t *testing.T, kinds, wanted []string) {
	t.Helper()
	pos := 0
	for _, want := range wanted {
		found := false
		for pos < len(kinds) {
			if kinds[pos] == want {
				found = true
				pos++
				break
			}
			pos++
		}
		require.True(t, found,
			"kind %q missing after position %d in sequence %v", want, pos, kinds)
	}
}

func findEvent(evs []pipevents.Event, stage pipevents.Stage, kind string) *pipevents.Event {
	for i := range evs {
		if evs[i].Stage == stage && evs[i].Kind == kind {
			return &evs[i]
		}
	}
	return nil
}

func TestEngineEmitsPipelineEvents(t *testing.T) {
	obs := &collectingObs{}
	codexDir := t.TempDir()

	uuid := "b2c3d4e5-2345-6789-abcd-ef0123456789"
	content := testjsonl.NewSessionBuilder().
		AddCodexMeta("2024-01-15T10:00:00.000Z", uuid, "/home/user/code/api", "user").
		AddCodexMessage("2024-01-15T10:00:01.000Z", "user", "Add tests").
		AddCodexMessage("2024-01-15T10:00:05.000Z", "assistant", "Adding test coverage.").
		String()
	path := filepath.Join(
		codexDir, "2024", "01", "15",
		"rollout-20240115-"+uuid+".jsonl",
	)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	database := dbtest.OpenTestDB(t)
	engine := sync.NewEngine(t.Context(), database, sync.EngineConfig{
		AgentDirs: map[parser.AgentType][]string{
			parser.AgentCodex: {codexDir},
		},
		Machine:          "local",
		PipelineObserver: obs,
	})

	require.NoError(t, engine.SyncPathsContext(t.Context(), []string{path}))

	assertOrderedSubsequence(t, obs.kinds(), []string{
		pipevents.KindBatchDispatched,
		pipevents.KindGateVerdict,
		pipevents.KindParseStarted,
		pipevents.KindTxCommitted,
		pipevents.KindSyncPassDone,
	})

	evs := obs.events()
	var checkpoint *pipevents.Event
	for i := range evs {
		if evs[i].Kind != pipevents.KindGateVerdict {
			continue
		}
		if evs[i].Payload["gate"] == "codex_checkpoint" {
			checkpoint = &evs[i]
			break
		}
	}
	require.NotNil(t, checkpoint,
		"first ingest must emit a codex_checkpoint gate verdict")
	assert.Equal(t, "missing", checkpoint.Payload["decision"])

	// The batch dispatch describes the changed-path work that entered the pass.
	dispatch := findEvent(evs, pipevents.StageWatch, pipevents.KindBatchDispatched)
	require.NotNil(t, dispatch)
	assert.Equal(t, 1, dispatch.Payload["paths"])

	// A first ingest parses in full and commits the batch write.
	mode := findEvent(evs, pipevents.StageParse, pipevents.KindParseModeSelected)
	require.NotNil(t, mode)
	assert.Equal(t, "full", mode.Payload["mode"])
}

func TestSetPipelineObserverTogglesEvents(t *testing.T) {
	codexDir := t.TempDir()

	uuid := "c3d4e5f6-3456-789a-bcde-f01234567890"
	content := testjsonl.NewSessionBuilder().
		AddCodexMeta("2024-01-15T10:00:00.000Z", uuid, "/home/user/code/api", "user").
		AddCodexMessage("2024-01-15T10:00:01.000Z", "user", "hello").
		String()
	path := filepath.Join(
		codexDir, "2024", "01", "15",
		"rollout-20240115-"+uuid+".jsonl",
	)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	database := dbtest.OpenTestDB(t)
	engine := sync.NewEngine(t.Context(), database, sync.EngineConfig{
		AgentDirs: map[parser.AgentType][]string{
			parser.AgentCodex: {codexDir},
		},
		Machine: "local",
	})

	require.NoError(t, engine.SyncPathsContext(t.Context(), []string{path}))

	obs := &collectingObs{}
	engine.SetPipelineObserver(obs)

	// Append a line so the file is processed again.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString(
		testjsonl.CodexMsgJSON("user", "more", "2024-01-15T10:01:00.000Z") + "\n",
	)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	require.NoError(t, engine.SyncPathsContext(t.Context(), []string{path}))
	assert.NotEmpty(t, obs.events(),
		"observer installed after NewEngine must receive events")

	engine.SetPipelineObserver(nil)
	obs.mu.Lock()
	obs.evs = nil
	obs.mu.Unlock()
	require.NoError(t, engine.SyncPathsContext(t.Context(), []string{path}))
	assert.Empty(t, obs.events(),
		"clearing the observer must disable emission")
}
