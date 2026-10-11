//go:build fts5

package pipeline

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/pipevents"
)

// scenarioByID returns the registered scenario with id, or nil.
func scenarioByID(id string) *Scenario {
	scenarios := Scenarios()
	for i := range scenarios {
		if scenarios[i].ID == id {
			return &scenarios[i]
		}
	}
	return nil
}

// findEvent returns the first event of kind whose payload matches
// every pair in want, or nil when no event qualifies.
func findEvent(
	evs []pipevents.Event, kind string, want map[string]any,
) *pipevents.Event {
	for i := range evs {
		if evs[i].Kind != kind {
			continue
		}
		ok := true
		for k, v := range want {
			if evs[i].Payload[k] != v {
				ok = false
				break
			}
		}
		if ok {
			return &evs[i]
		}
	}
	return nil
}

// countEvents returns how many events carry kind.
func countEvents(evs []pipevents.Event, kind string) int {
	n := 0
	for _, e := range evs {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// countMatching returns how many events of kind match every payload
// pair in want.
func countMatching(
	evs []pipevents.Event, kind string, want map[string]any,
) int {
	n := 0
	for _, e := range evs {
		if e.Kind != kind {
			continue
		}
		ok := true
		for k, v := range want {
			if e.Payload[k] != v {
				ok = false
				break
			}
		}
		if ok {
			n++
		}
	}
	return n
}

// scenarioStepActions lists the action payload of every scenario_step
// event in order — the scripted cause trail inside a run.
func scenarioStepActions(evs []pipevents.Event) []string {
	var out []string
	for _, e := range evs {
		if e.Kind != pipevents.KindScenarioStep {
			continue
		}
		action, _ := e.Payload["action"].(string)
		out = append(out, action)
	}
	return out
}

// runScenario opens a fresh sandbox, runs sc end to end, and returns
// the sandbox, hub, and run id. The caller owns closing the sandbox.
func runScenario(
	t *testing.T, sc *Scenario,
) (*Sandbox, *Hub, string) {
	t.Helper()
	ctx := t.Context()
	dataDir := t.TempDir()
	hub := NewHub("")
	sb, err := OpenSandbox(ctx, dataDir, hub)
	require.NoError(t, err)
	t.Cleanup(sb.Close)

	runID, err := sb.Run(ctx, sc)
	require.NoError(t, err, "scenario %s run", sc.ID)
	require.NotEmpty(t, runID)
	return sb, hub, runID
}

// runEventsEventually polls the run recording until cond accepts the
// event set, returning the final snapshot. Fails the test after the
// FSEvents-scale budget expires.
func runEventsEventually(
	t *testing.T, hub *Hub, runID string,
	cond func([]pipevents.Event) bool, msgAndArgs ...any,
) []pipevents.Event {
	t.Helper()
	var evs []pipevents.Event
	require.Eventually(t, func() bool {
		evs = hub.RunEvents(runID)
		return cond(evs)
	}, 10*time.Second, 100*time.Millisecond, msgAndArgs...)
	return evs
}

// TestScenarioRegistry checks the built-in registry's static shape:
// unique non-empty ids, presented titles and summaries, and at least
// one action step ahead of the trailing idle wait that keeps every
// run's ingest effects inside its recording window.
func TestScenarioRegistry(t *testing.T) {
	scenarios := Scenarios()
	require.NotEmpty(t, scenarios, "Scenarios() registry is empty")

	want := []string{
		"happy-path", "live-append", "reingest-unchanged",
		"malformed-line", "delete-file", "truncate-rewrite",
		"title-change", "cold-archive",
	}
	seen := make(map[string]struct{}, len(scenarios))
	for _, sc := range scenarios {
		require.NotEmpty(t, sc.ID, "scenario with empty ID")
		_, dup := seen[sc.ID]
		require.False(t, dup, "duplicate scenario ID %q", sc.ID)
		seen[sc.ID] = struct{}{}
		assert.NotEmpty(t, sc.Title, "scenario %s has no title", sc.ID)
		assert.NotEmpty(t, sc.Summary, "scenario %s has no summary", sc.ID)
		require.GreaterOrEqual(t, len(sc.Steps), 2,
			"scenario %s needs an action plus the trailing idle wait",
			sc.ID)
		for i, step := range sc.Steps {
			require.NotNil(t, step, "scenario %s step %d is nil", sc.ID, i)
		}
	}
	for _, id := range want {
		_, ok := seen[id]
		assert.True(t, ok, "registry is missing built-in %q", id)
	}
}

// TestMalformedScenario drives the malformed-line fixture through the
// real ingest path and asserts the parse/anomaly signal lands: the
// garbage line is classified "malformed" while the session still
// commits.
func TestMalformedScenario(t *testing.T) {
	sc := scenarioByID("malformed-line")
	require.NotNil(t, sc, "malformed-line scenario not registered")
	_, hub, runID := runScenario(t, sc)

	evs := runEventsEventually(t, hub, runID, func(evs []pipevents.Event) bool {
		return findEvent(evs, pipevents.KindLineClassified,
			map[string]any{"type": "malformed"}) != nil
	}, "run %s never classified a malformed line", runID)
	assert.NotNil(t,
		findEvent(evs, pipevents.KindTxCommitted, nil),
		"malformed-line session should still commit")
}

// TestBuiltinScenarios runs every registered scenario against its own
// sandbox and asserts the gate or event each one teaches. The table
// keeps expectations local to the scenario so a regression names the
// behavior it broke.
func TestBuiltinScenarios(t *testing.T) {
	for _, sc := range Scenarios() {
		t.Run(sc.ID, func(t *testing.T) {
			sb, hub, runID := runScenario(t, &sc)
			evs := runEventsEventually(t, hub, runID,
				func(evs []pipevents.Event) bool {
					return scenarioTerminal(t, sc.ID, sb, hub, evs)
				}, "run %s never reached its terminal signal", runID)
			assertScenario(t, sc.ID, sb, evs)

			// The runner closes every scripted run with the idle wait
			// so post-write delivery lands inside the recording.
			actions := scenarioStepActions(evs)
			require.NotEmpty(t, actions)
			assert.Equal(t, "wait-idle", actions[len(actions)-1],
				"scenario %s last recorded step is not wait-idle",
				sc.ID)
		})
	}
}

// scenarioTerminal reports whether the run has produced the signal
// that proves the scenario's teaching point — the predicate the
// Eventually poll waits on. DB-facing checks poll the sandbox archive
// so a commit that lags its tx event still counts.
func scenarioTerminal(
	t *testing.T, id string, sb *Sandbox, hub *Hub,
	evs []pipevents.Event,
) bool {
	t.Helper()
	switch id {
	case "happy-path":
		return findEvent(evs, pipevents.KindTxCommitted, nil) != nil
	case "live-append":
		return findEvent(evs, pipevents.KindParseStarted,
			map[string]any{"mode": "incremental"}) != nil
	case "reingest-unchanged":
		// The touched file cannot be proven unchanged by stat, so
		// the run commits a second time after re-verifying content.
		return countEvents(evs, pipevents.KindTxCommitted) >= 2
	case "malformed-line":
		return findEvent(evs, pipevents.KindLineClassified,
			map[string]any{"type": "malformed"}) != nil
	case "delete-file":
		page, err := sb.db.ListSessions(t.Context(),
			db.SessionFilter{Agent: "codex"})
		return err == nil && page.Total == 0
	case "truncate-rewrite":
		return findEvent(evs, pipevents.KindGateVerdict,
			map[string]any{"gate": "codex_checkpoint",
				"decision": "invalid"}) != nil
	case "title-change":
		sess, err := sb.db.GetSessionFull(t.Context(),
			"codex:"+uuidHappy)
		return err == nil && sess != nil && sess.SessionName != nil &&
			*sess.SessionName == indexTitle
	case "cold-archive":
		page, err := sb.db.ListSessions(t.Context(),
			db.SessionFilter{Agent: "codex"})
		return err == nil && page.Total == 3
	default:
		return true
	}
}

// assertScenario checks the run recording once the terminal signal
// landed: each built-in teaches one gate or event, and these are the
// observable traces of it.
func assertScenario(
	t *testing.T, id string, sb *Sandbox, evs []pipevents.Event,
) {
	t.Helper()
	checkpoint := func(decision string) *pipevents.Event {
		return findEvent(evs, pipevents.KindGateVerdict,
			map[string]any{"gate": "codex_checkpoint",
				"decision": decision})
	}
	switch id {
	case "happy-path":
		assert.NotNil(t, findEvent(evs, pipevents.KindSessionWritten, nil),
			"happy-path should write a session row")
		assert.NotNil(t, checkpoint("missing"),
			"first ingest has no stored checkpoint")
	case "live-append":
		assert.NotNil(t, checkpoint("append"),
			"append growth should prove clean via codex_checkpoint=append")
		assert.NotNil(t, findEvent(evs, pipevents.KindParseStarted,
			map[string]any{"mode": "incremental"}),
			"appended tail should parse incrementally")
		assert.GreaterOrEqual(t,
			countEvents(evs, pipevents.KindTxCommitted), 2,
			"initial write and append each commit")
	case "reingest-unchanged":
		// A bare mtime touch cannot be distinguished from a
		// same-size rewrite by stat, so the checkpoint gate
		// declines again on the second pass and the file
		// re-parses fully.
		assert.Equal(t, 2, countMatching(evs,
			pipevents.KindGateVerdict,
			map[string]any{"gate": "codex_checkpoint",
				"decision": "missing"}),
			"touch breaks the checkpoint proof before it can verify")
		assert.NotNil(t, findEvent(evs,
			pipevents.KindParseModeSelected,
			map[string]any{"mode": "full",
				"reason": "no new transcript bytes"}),
			"no transcript growth means a verifying full parse")
		assert.Equal(t, 2, countEvents(evs, pipevents.KindTxCommitted),
			"identical bytes still pay a re-verify commit")
	case "malformed-line":
		assert.NotNil(t, findEvent(evs, pipevents.KindLineClassified,
			map[string]any{"type": "malformed"}))
		assert.NotNil(t, findEvent(evs, pipevents.KindTxCommitted, nil))
	case "delete-file":
		assert.NotNil(t, findEvent(evs, pipevents.KindPathClassified,
			map[string]any{"classification": "missing"}),
			"the removed rollout should classify as missing")
		page, err := sb.db.ListSessions(t.Context(),
			db.SessionFilter{Agent: "codex"})
		require.NoError(t, err)
		assert.Equal(t, 0, page.Total,
			"deleted rollout should tombstone its session row")
	case "truncate-rewrite":
		assert.NotNil(t, checkpoint("invalid"),
			"truncation must invalidate the stored checkpoint")
		assert.NotNil(t, findEvent(evs, pipevents.KindParseStarted,
			map[string]any{"mode": "full"}),
			"invalid checkpoint forces a full re-parse")
	case "title-change":
		assert.NotNil(t, findEvent(evs, pipevents.KindPathClassified,
			map[string]any{"classification": "index-sidecar"}),
			"session_index.jsonl event should classify as index sidecar")
		assert.NotNil(t, findEvent(evs, pipevents.KindParseModeSelected,
			map[string]any{"mode": "full"}),
			"title rename requires a metadata refresh full parse")
		sess, err := sb.db.GetSessionFull(t.Context(), "codex:"+uuidHappy)
		require.NoError(t, err)
		require.NotNil(t, sess)
		require.NotNil(t, sess.SessionName,
			"stored session should carry the index title")
		assert.Equal(t, indexTitle, *sess.SessionName,
			"stored session name should follow the index title")
	case "cold-archive":
		assert.GreaterOrEqual(t,
			countEvents(evs, pipevents.KindSessionWritten), 3,
			"three dropped rollouts each write a session")
	}
}
