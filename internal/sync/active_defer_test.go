package sync

import (
	"os"
	"path/filepath"
	gosync "sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/pipevents"
)

// activeDeferObserver records pipeline events so tests can assert the
// active_defer gate verdict the admission seam emits.
type activeDeferObserver struct {
	mu  gosync.Mutex
	evs []pipevents.Event
}

func (o *activeDeferObserver) ObserveEvent(e pipevents.Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.evs = append(o.evs, e)
}

// activeDeferVerdicts returns the recorded gate verdicts for the named gate.
func (o *activeDeferObserver) activeDeferVerdicts(gate string) []pipevents.Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	var matched []pipevents.Event
	for _, e := range o.evs {
		if e.Stage == pipevents.StageGates &&
			e.Kind == pipevents.KindGateVerdict &&
			e.Payload["gate"] == gate {
			matched = append(matched, e)
		}
	}
	return matched
}

// newActiveDeferFixtureEngine wires a cowork process fixture with a
// pipeline observer, mirroring newProcessFixtureEngine plus the observer
// the gate-verdict assertions need.
func newActiveDeferFixtureEngine(
	t *testing.T,
	root string,
	provider *processFixtureProvider,
) (*Engine, *activeDeferObserver) {
	t.Helper()
	obs := &activeDeferObserver{}
	engine := NewEngine(t.Context(), openTestDB(t), EngineConfig{
		AgentDirs: map[parser.AgentType][]string{
			parser.AgentCowork: {root},
		},
		Machine:           "devbox",
		PipelineObserver:  obs,
		ProviderFactories: []parser.ProviderFactory{processFixtureFactory{provider: provider}},
		ProviderMigrationModes: map[parser.AgentType]parser.ProviderMigrationMode{
			parser.AgentCowork: parser.ProviderMigrationProviderAuthoritative,
		},
	})
	t.Cleanup(engine.Close)
	return engine, obs
}

// writeActiveDeferSource writes one fixture source and backdates its mtime
// to the given instant, so the injected clock can reason about its age
// deterministically.
func writeActiveDeferSource(
	t *testing.T, root string, mtime time.Time,
) (string, parser.SourceFingerprint) {
	t.Helper()
	path := filepath.Join(root, "owned.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"session":"fixture"}`+"\n"), 0o644))
	require.NoError(t, os.Chtimes(path, mtime, mtime))
	info, err := os.Stat(path)
	require.NoError(t, err)
	return path, parser.SourceFingerprint{
		Key:     path,
		Size:    info.Size(),
		MTimeNS: info.ModTime().UnixNano(),
	}
}

// activeDeferOutcome builds the fixture parse outcome the provider returns
// once a source gets past admission.
func activeDeferOutcome(
	path string, fingerprint parser.SourceFingerprint,
) parser.ParseOutcome {
	return parser.ParseOutcome{
		Results: []parser.ParseResultOutcome{{
			Result: processFixtureResult(
				"cowork:defer", parser.AgentCowork, "fixture-project", path, fingerprint,
			),
			DataVersion: parser.DataVersionCurrent,
		}},
		ResultSetComplete: true,
		ForceReplace:      true,
	}
}

// appendToSource grows the source file by one line; the injected
// activeDeferSleep calls it, standing in for the writer's flush between the
// gate's two stats without any real delay.
func appendToSource(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.WriteString(`{"session":"fixture-append"}` + "\n")
	require.NoError(t, err)
}

// activeDeferOptIn enables the gate for one test through the env seam: the
// go-test carve-out keeps an unset knob off, so gate tests state the window
// explicitly. Restores the seam on cleanup.
func activeDeferOptIn(t *testing.T) {
	t.Helper()
	original := activeDeferEnvLookup
	activeDeferEnvLookup = func(string) string {
		return "5"
	}
	t.Cleanup(func() { activeDeferEnvLookup = original })
}

// TestActiveWriteDeferDefersFreshGrowingSource covers the gate's core
// verdict: a source inside the recency window that grew between the two
// admission stats is deferred — parse admission returns before the provider
// ever sees the file, the gate verdict is recorded, and the anomaly report
// counts the deferral.
func TestActiveWriteDeferDefersFreshGrowingSource(t *testing.T) {
	activeDeferOptIn(t)
	root := t.TempDir()
	now := time.Now()
	path, fingerprint := writeActiveDeferSource(t, root, now.Add(-time.Second))
	provider := newProcessFixtureProvider(
		processFixtureSource(path), fingerprint, activeDeferOutcome(path, fingerprint),
	)
	engine, obs := newActiveDeferFixtureEngine(t, root, provider)
	engine.activeDeferNow = func() time.Time { return now }
	engine.activeDeferSleep = func(time.Duration) { appendToSource(t, path) }

	res := engine.processFile(t.Context(), parser.DiscoveredFile{
		Path:  path,
		Agent: parser.AgentCowork,
	})

	require.NoError(t, res.err)
	assert.True(t, res.skip, "a fresh growing source must defer this admission")
	assert.Empty(t, res.results)
	assert.NotContains(t, provider.calls, "parse",
		"deferred admission must return before the provider parses")

	verdicts := obs.activeDeferVerdicts("active_defer")
	require.Len(t, verdicts, 1, "one active_defer gate verdict must be recorded")
	assert.Equal(t, "skip", verdicts[0].Payload["decision"])
	assert.Equal(t, fingerprint.Size, verdicts[0].Payload["size_first"])

	var stats SyncStats
	engine.anomalies.applyTo(&stats)
	assert.Equal(t, 1, stats.Anomalies.ActiveDefersTotal)
	assert.Equal(t, map[string]int{
		string(parser.AgentCowork): 1,
	}, stats.Anomalies.ActiveDefersByAgent)
}

// TestActiveWriteDeferAgedSourceParsesImmediately covers the age escape: a
// source older than the window never defers, parses as usual, and pays no
// sample delay.
func TestActiveWriteDeferAgedSourceParsesImmediately(t *testing.T) {
	activeDeferOptIn(t)
	root := t.TempDir()
	now := time.Now()
	path, fingerprint := writeActiveDeferSource(t, root, now.Add(-10*time.Second))
	provider := newProcessFixtureProvider(
		processFixtureSource(path), fingerprint, activeDeferOutcome(path, fingerprint),
	)
	engine, obs := newActiveDeferFixtureEngine(t, root, provider)
	engine.activeDeferNow = func() time.Time { return now }
	waited := false
	engine.activeDeferSleep = func(time.Duration) {
		waited = true
		appendToSource(t, path)
	}

	res := engine.processFile(t.Context(), parser.DiscoveredFile{
		Path:  path,
		Agent: parser.AgentCowork,
	})

	require.NoError(t, res.err)
	assert.False(t, res.skip, "an aged source must parse this admission")
	assert.Contains(t, provider.calls, "parse")
	assert.False(t, waited,
		"the age escape must return before the growth sample is taken")
	assert.Empty(t, obs.activeDeferVerdicts("active_defer"))

	var stats SyncStats
	engine.anomalies.applyTo(&stats)
	assert.Equal(t, 0, stats.Anomalies.ActiveDefersTotal)
}

// TestActiveWriteDeferFreshStableSourceParses covers the growth half: a
// source inside the window that did not grow between the two stats parses
// immediately — recency alone is not evidence of an active writer, so a
// quiet-but-young file never starves.
func TestActiveWriteDeferFreshStableSourceParses(t *testing.T) {
	activeDeferOptIn(t)
	root := t.TempDir()
	now := time.Now()
	path, fingerprint := writeActiveDeferSource(t, root, now.Add(-time.Second))
	provider := newProcessFixtureProvider(
		processFixtureSource(path), fingerprint, activeDeferOutcome(path, fingerprint),
	)
	engine, obs := newActiveDeferFixtureEngine(t, root, provider)
	engine.activeDeferNow = func() time.Time { return now }
	engine.activeDeferSleep = func(time.Duration) {}

	res := engine.processFile(t.Context(), parser.DiscoveredFile{
		Path:  path,
		Agent: parser.AgentCowork,
	})

	require.NoError(t, res.err)
	assert.False(t, res.skip, "a fresh but stable source must parse")
	assert.Contains(t, provider.calls, "parse")
	assert.Empty(t, obs.activeDeferVerdicts("active_defer"))

	var stats SyncStats
	engine.anomalies.applyTo(&stats)
	assert.Equal(t, 0, stats.Anomalies.ActiveDefersTotal)
}

// TestActiveWriteDeferEnvZeroDisables covers the knob: setting
// AGENTSVIEW_SYNC_ACTIVE_DEFER_SECONDS=0 disables the gate, so even a fresh
// growing source parses this admission.
func TestActiveWriteDeferEnvZeroDisables(t *testing.T) {
	original := activeDeferEnvLookup
	activeDeferEnvLookup = func(string) string { return "0" }
	t.Cleanup(func() { activeDeferEnvLookup = original })

	root := t.TempDir()
	now := time.Now()
	path, fingerprint := writeActiveDeferSource(t, root, now.Add(-time.Second))
	provider := newProcessFixtureProvider(
		processFixtureSource(path), fingerprint, activeDeferOutcome(path, fingerprint),
	)
	engine, obs := newActiveDeferFixtureEngine(t, root, provider)
	engine.activeDeferNow = func() time.Time { return now }
	waited := false
	engine.activeDeferSleep = func(time.Duration) {
		waited = true
		appendToSource(t, path)
	}

	res := engine.processFile(t.Context(), parser.DiscoveredFile{
		Path:  path,
		Agent: parser.AgentCowork,
	})

	require.NoError(t, res.err)
	assert.False(t, res.skip, "a disabled gate must not defer")
	assert.Contains(t, provider.calls, "parse")
	assert.False(t, waited, "a disabled gate must not sample growth")
	assert.Empty(t, obs.activeDeferVerdicts("active_defer"))

	var stats SyncStats
	engine.anomalies.applyTo(&stats)
	assert.Equal(t, 0, stats.Anomalies.ActiveDefersTotal)
}

// TestActiveDeferWindowEnvSemantics pins the knob's parsing in production:
// unset and unusable values keep the default, "0" and "false" disable, and
// a positive integer re-times the window.
func TestActiveDeferWindowEnvSemantics(t *testing.T) {
	original := activeDeferEnvLookup
	originalUnderTest := activeDeferUnderGoTest
	t.Cleanup(func() {
		activeDeferEnvLookup = original
		activeDeferUnderGoTest = originalUnderTest
	})
	// Pin the production side of the go-test carve-out: outside `go test`
	// an unset knob keeps the default window.
	activeDeferUnderGoTest = func() bool { return false }

	tests := []struct {
		name string
		env  string
		want time.Duration
	}{
		{name: "unset keeps default", env: "", want: activeDeferDefaultWindow},
		{name: "zero disables", env: "0", want: 0},
		{name: "false disables", env: "false", want: 0},
		{name: "negative keeps default", env: "-3", want: activeDeferDefaultWindow},
		{name: "garbage keeps default", env: "soon", want: activeDeferDefaultWindow},
		{name: "positive re-times", env: "7", want: 7 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			activeDeferEnvLookup = func(string) string { return tt.env }
			assert.Equal(t, tt.want, activeDeferWindow())
		})
	}
}

// TestActiveDeferWindowGoTestCarveOut pins the test side of the carve-out:
// under `go test` an unset knob is off, so the gate's inter-sample wait
// never taxes the thousands of fixture admissions that hand it freshly
// written files; gate-specific tests opt in through the env seam.
func TestActiveDeferWindowGoTestCarveOut(t *testing.T) {
	original := activeDeferEnvLookup
	originalUnderTest := activeDeferUnderGoTest
	t.Cleanup(func() {
		activeDeferEnvLookup = original
		activeDeferUnderGoTest = originalUnderTest
	})
	activeDeferUnderGoTest = func() bool { return true }
	activeDeferEnvLookup = func(string) string { return "" }
	assert.Equal(t, time.Duration(0), activeDeferWindow())
	// An explicit opt-in still re-times or enables the gate under test.
	activeDeferEnvLookup = func(string) string { return "5" }
	assert.Equal(t, 5*time.Second, activeDeferWindow())
}
