package sync_test

import (
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/config"
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

func (c *collectingObs) reset() {
	c.mu.Lock()
	c.evs = nil
	c.mu.Unlock()
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
	obs.reset()
	require.NoError(t, engine.SyncPathsContext(t.Context(), []string{path}))
	assert.Empty(t, obs.events(),
		"clearing the observer must disable emission")
}

// parseModeEvents returns every parse_mode_selected event in evs, so a
// test can assert the full set rather than whichever one fires first.
func parseModeEvents(evs []pipevents.Event) []pipevents.Event {
	var out []pipevents.Event
	for _, e := range evs {
		if e.Kind == pipevents.KindParseModeSelected {
			out = append(out, e)
		}
	}
	return out
}

// TestPipelineEventsIncrementalDeclinesEmitParseMode covers the two
// incremental-decline paths the round-1 review found silent: a Claude
// same-message.id split run the rescan cannot resolve into a
// trustworthy delta, and the first real prompt arriving after an empty
// stored preview. Both fall back to a full parse and must record that
// decision through parse_mode_selected — otherwise the stream shows an
// incremental parse_started/parse_done pair followed by an unexplained
// full parse.
func TestPipelineEventsIncrementalDeclinesEmitParseMode(t *testing.T) {
	t.Run("claude split run unresolved", func(t *testing.T) {
		obs := &collectingObs{}
		dir := t.TempDir()
		database := dbtest.OpenTestDB(t)
		engine := sync.NewEngine(t.Context(), database, sync.EngineConfig{
			AgentDirs: map[parser.AgentType][]string{
				parser.AgentClaude: {dir},
			},
			Machine:          "local",
			ArchiveContent:   config.ArchiveContentUsage,
			PipelineObserver: obs,
		})
		t.Cleanup(engine.Close)

		// Usage-only archives decline the split-run rescan on sight —
		// they drop the user rows the ranged write would recount — so
		// finishing a stored open run lands on the
		// verdict != ClaudeSplitNone fallback.
		shape := claudeSplitShapes["cumulative_text"]
		path := filepath.Join(dir, "proj", "split.jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(
			path,
			[]byte(strings.Join(shape.lines[:shape.splitAfter], "\n")+"\n"),
			0o644,
		))
		engine.SyncAll(t.Context(), nil)

		obs.reset()
		appendClaudeSplitLines(t, path, shape.lines[shape.splitAfter:]...)
		require.NoError(t,
			engine.SyncPathsContext(t.Context(), []string{path}))

		assertOrderedSubsequence(t, obs.kinds(), []string{
			pipevents.KindParseStarted,
			pipevents.KindParseDone,
			pipevents.KindParseModeSelected,
		})
		modes := parseModeEvents(obs.events())
		require.Len(t, modes, 1,
			"the decline must produce exactly one mode decision")
		assert.Equal(t, "full", modes[0].Payload["mode"])
		assert.Equal(t, "claude split run unresolved",
			modes[0].Payload["reason"])
	})

	t.Run("empty preview repaired on first prompt", func(t *testing.T) {
		obs := &collectingObs{}
		dir := t.TempDir()
		database := dbtest.OpenTestDB(t)
		engine := sync.NewEngine(t.Context(), database, sync.EngineConfig{
			AgentDirs: map[parser.AgentType][]string{
				parser.AgentClaude: {dir},
			},
			Machine:          "local",
			PipelineObserver: obs,
		})
		t.Cleanup(engine.Close)

		// The first sync stores only promoted IDE context, so
		// first_message stays empty; the appended chunk then carries
		// the first real prompt, which the incremental path cannot
		// repair and must hand to a full parse.
		initial := testjsonl.JoinJSONL(
			testjsonl.ClaudeUserJSON(
				"<ide_opened_file>The user opened "+
					"/workspace/app/README.md.</ide_opened_file>",
				"2024-01-01T00:00:00Z",
			),
		)
		path := filepath.Join(dir, "proj", "ide-only.jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(initial), 0o644))
		engine.SyncAll(t.Context(), nil)

		obs.reset()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		require.NoError(t, err, "open for append")
		_, err = f.WriteString(testjsonl.ClaudeUserJSON(
			"Explain this code", "2024-01-01T00:00:01Z",
		) + "\n")
		require.NoError(t, err, "append")
		require.NoError(t, f.Close())
		require.NoError(t,
			engine.SyncPathsContext(t.Context(), []string{path}))

		assertOrderedSubsequence(t, obs.kinds(), []string{
			pipevents.KindParseStarted,
			pipevents.KindParseDone,
			pipevents.KindParseModeSelected,
		})
		modes := parseModeEvents(obs.events())
		require.Len(t, modes, 1,
			"the decline must produce exactly one mode decision")
		assert.Equal(t, "full", modes[0].Payload["mode"])
		assert.Equal(t, "first real user prompt after empty preview",
			modes[0].Payload["reason"])
	})
}
