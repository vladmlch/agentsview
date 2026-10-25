package sync

import (
	"encoding/json/v2"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/sync/failurecache"
)

func TestSyncStats_RecordSkip(t *testing.T) {
	tests := []struct {
		name  string
		skips int
		want  int
	}{
		{"zero skips", 0, 0},
		{"multiple skips", 2, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s SyncStats
			for range tt.skips {
				s.RecordSkip()
			}
			assert.Equal(t, tt.want, s.Skipped)
			assert.Equal(t, 0, s.Synced)
		})
	}
}

func TestSyncStats_RecordSynced(t *testing.T) {
	tests := []struct {
		name   string
		synced []int
		want   int
	}{
		{"zero synced", []int{}, 0},
		{"multiple synced", []int{5, 3}, 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s SyncStats
			for _, v := range tt.synced {
				s.RecordSynced(v)
			}
			assert.Equal(t, 0, s.Skipped)
			assert.Equal(t, tt.want, s.Synced)
		})
	}
}

func TestAnomalyStats_RecordMalformedLines(t *testing.T) {
	tests := []struct {
		name    string
		records []struct {
			agent string
			n     int
		}
		wantByAgent map[string]int
		wantTotal   int
	}{
		{
			name:        "clean run records nothing",
			wantByAgent: nil,
			wantTotal:   0,
		},
		{
			name: "zero and negative counts are ignored",
			records: []struct {
				agent string
				n     int
			}{
				{"claude", 0},
				{"codex", -3},
			},
			wantByAgent: nil,
			wantTotal:   0,
		},
		{
			name: "per-agent breakdown plus grand total",
			records: []struct {
				agent string
				n     int
			}{
				{"claude", 2},
				{"codex", 5},
				{"claude", 3},
			},
			wantByAgent: map[string]int{"claude": 5, "codex": 5},
			wantTotal:   10,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a AnomalyStats
			for _, r := range tt.records {
				a.RecordMalformedLines(r.agent, r.n)
			}
			assert.Equal(t, tt.wantByAgent, a.MalformedLinesByAgent)
			assert.Equal(t, tt.wantTotal, a.MalformedLinesTotal)
		})
	}
}

func TestAnomalyStats_RecordUnknownSchemaSessions(t *testing.T) {
	tests := []struct {
		name    string
		records []struct {
			agent string
			n     int
		}
		wantByAgent map[string]int
		wantTotal   int
	}{
		{
			name:        "clean run records nothing",
			wantByAgent: nil,
			wantTotal:   0,
		},
		{
			name: "zero and negative counts are ignored",
			records: []struct {
				agent string
				n     int
			}{
				{"antigravity", 0},
				{"antigravity-cli", -2},
			},
			wantByAgent: nil,
			wantTotal:   0,
		},
		{
			name: "per-agent breakdown plus grand total",
			records: []struct {
				agent string
				n     int
			}{
				{"antigravity", 1},
				{"antigravity-cli", 2},
				{"antigravity", 1},
			},
			wantByAgent: map[string]int{"antigravity": 2, "antigravity-cli": 2},
			wantTotal:   4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a AnomalyStats
			for _, r := range tt.records {
				a.RecordUnknownSchemaSessions(r.agent, r.n)
			}
			assert.Equal(t, tt.wantByAgent, a.UnknownSchemaSessionsByAgent)
			assert.Equal(t, tt.wantTotal, a.UnknownSchemaSessionsTotal)
		})
	}
}

// TestAnomalyAccumulator_RecordUnknownSchemaSession verifies whole-session
// accumulation per agent (no per-source dedup) and that a reset clears it.
func TestAnomalyAccumulator_RecordUnknownSchemaSession(t *testing.T) {
	var acc anomalyAccumulator
	acc.reset()

	acc.recordUnknownSchemaSession("antigravity")
	acc.recordUnknownSchemaSession("antigravity")
	acc.recordUnknownSchemaSession("antigravity-cli")

	var stats SyncStats
	acc.applyTo(&stats)
	assert.Equal(t, 3, stats.Anomalies.UnknownSchemaSessionsTotal)
	assert.Equal(t, map[string]int{"antigravity": 2, "antigravity-cli": 1},
		stats.Anomalies.UnknownSchemaSessionsByAgent)
	assert.False(t, stats.Anomalies.IsZero())

	// A fresh run starts from zero.
	acc.reset()
	var next SyncStats
	acc.applyTo(&next)
	assert.True(t, next.Anomalies.IsZero())
	assert.Zero(t, next.Anomalies.UnknownSchemaSessionsTotal)
	assert.Nil(t, next.Anomalies.UnknownSchemaSessionsByAgent)
}

func TestAnomalyStats_RecordGenMetadataWithoutUsage(t *testing.T) {
	tests := []struct {
		name    string
		records []struct {
			agent string
			n     int
		}
		wantByAgent map[string]int
		wantTotal   int
	}{
		{
			name:        "clean run records nothing",
			wantByAgent: nil,
			wantTotal:   0,
		},
		{
			name: "zero and negative counts are ignored",
			records: []struct {
				agent string
				n     int
			}{
				{"antigravity", 0},
				{"antigravity-cli", -2},
			},
			wantByAgent: nil,
			wantTotal:   0,
		},
		{
			name: "per-agent breakdown plus grand total",
			records: []struct {
				agent string
				n     int
			}{
				{"antigravity", 1},
				{"antigravity-cli", 2},
				{"antigravity", 1},
			},
			wantByAgent: map[string]int{"antigravity": 2, "antigravity-cli": 2},
			wantTotal:   4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a AnomalyStats
			for _, r := range tt.records {
				a.RecordGenMetadataWithoutUsage(r.agent, r.n)
			}
			assert.Equal(t, tt.wantByAgent, a.GenMetadataWithoutUsageByAgent)
			assert.Equal(t, tt.wantTotal, a.GenMetadataWithoutUsageTotal)
		})
	}
}

// TestAnomalyAccumulator_RecordGenMetadataWithoutUsageSession verifies
// whole-session accumulation per agent (no per-source dedup) and that a
// reset clears it.
func TestAnomalyAccumulator_RecordGenMetadataWithoutUsageSession(t *testing.T) {
	var acc anomalyAccumulator
	acc.reset()

	acc.recordGenMetadataWithoutUsageSession("antigravity")
	acc.recordGenMetadataWithoutUsageSession("antigravity")
	acc.recordGenMetadataWithoutUsageSession("antigravity-cli")

	var stats SyncStats
	acc.applyTo(&stats)
	assert.Equal(t, 3, stats.Anomalies.GenMetadataWithoutUsageTotal)
	assert.Equal(t, map[string]int{"antigravity": 2, "antigravity-cli": 1},
		stats.Anomalies.GenMetadataWithoutUsageByAgent)
	assert.False(t, stats.Anomalies.IsZero())

	// A fresh run starts from zero.
	acc.reset()
	var next SyncStats
	acc.applyTo(&next)
	assert.True(t, next.Anomalies.IsZero())
	assert.Zero(t, next.Anomalies.GenMetadataWithoutUsageTotal)
	assert.Nil(t, next.Anomalies.GenMetadataWithoutUsageByAgent)
}

func TestAnomalyAccumulator_Aggregate(t *testing.T) {
	var acc anomalyAccumulator
	acc.reset()

	// Two sessions of one agent, one of another, plus sanitize fixes
	// from messages and usage events across the run.
	acc.recordMalformedLines("claude", "a.jsonl", 4, nil)
	acc.recordMalformedLines("codex", "b.jsonl", 1, nil)
	acc.recordMalformedLines("claude", "c.jsonl", 2, nil)
	acc.recordMalformedLines("gemini", "d.jsonl", 0, nil) // ignored

	acc.recordSanitize(validationStats{ControlCharsStripped: 3, RoleCoerced: 1})
	acc.recordSanitize(validationStats{ModelClamped: 2, TokensClamped: 5})
	acc.recordSanitize(validationStats{}) // no-op

	var stats SyncStats
	acc.applyTo(&stats)

	assert.Equal(t, 7, stats.Anomalies.MalformedLinesTotal)
	assert.Equal(t, map[string]int{"claude": 6, "codex": 1},
		stats.Anomalies.MalformedLinesByAgent)
	assert.Equal(t, SanitizeStats{
		ControlCharsStripped: 3,
		ModelClamped:         2,
		TokensClamped:        5,
		RoleCoerced:          1,
	}, stats.Anomalies.Sanitize)
	assert.Equal(t, 11, stats.Anomalies.Sanitize.Total())
	assert.False(t, stats.Anomalies.IsZero())
}

// TestAnomalyAccumulator_DedupesMalformedLinesPerSourceFile verifies a source
// file that forks into several sessions counts its malformed lines once, while
// distinct files and empty-path (DB-backed) sessions still accumulate.
func TestAnomalyAccumulator_DedupesMalformedLinesPerSourceFile(t *testing.T) {
	var acc anomalyAccumulator
	acc.reset()

	// Three forked sessions of one Claude file all carry the same count.
	acc.recordMalformedLines("claude", "fork.jsonl", 2, nil)
	acc.recordMalformedLines("claude", "fork.jsonl", 2, nil)
	acc.recordMalformedLines("claude", "fork.jsonl", 2, nil)
	// A different source file counts independently.
	acc.recordMalformedLines("claude", "other.jsonl", 5, nil)
	// Empty path (DB-backed agent) is not deduped.
	acc.recordMalformedLines("warp", "", 1, nil)
	acc.recordMalformedLines("warp", "", 1, nil)

	var stats SyncStats
	acc.applyTo(&stats)
	assert.Equal(t, 2+5, stats.Anomalies.MalformedLinesByAgent["claude"],
		"forked file counted once; distinct file added")
	assert.Equal(t, 2, stats.Anomalies.MalformedLinesByAgent["warp"],
		"empty-path sessions are not deduped")
	assert.Equal(t, 9, stats.Anomalies.MalformedLinesTotal)
}

func TestAnomalyAccumulator_ResetClears(t *testing.T) {
	var acc anomalyAccumulator
	acc.recordMalformedLines("claude", "x.jsonl", 9, nil)
	acc.recordSanitize(validationStats{TimestampsBlanked: 1})
	acc.reset()

	var stats SyncStats
	acc.applyTo(&stats)
	assert.True(t, stats.Anomalies.IsZero())
	assert.Zero(t, stats.Anomalies.MalformedLinesTotal)
	assert.Nil(t, stats.Anomalies.MalformedLinesByAgent)
	assert.Nil(t, stats.Anomalies.MalformedLineSamples)
}

// TestAnomalyAccumulator_MalformedLineSamples pins the payload-free line
// records that ride along with the malformed-line counters: one sample per
// class per source file, deduplicated by the same per-path rule as the
// counts, and capped per run so a large corpus cannot bloat the report.
func TestAnomalyAccumulator_MalformedLineSamples(t *testing.T) {
	t.Run("records classes with line samples", func(t *testing.T) {
		var acc anomalyAccumulator
		acc.reset()
		acc.recordMalformedLines("antigravity", "transcript.jsonl", 3,
			[]parser.ParsedMalformedLineClass{
				{Class: parser.MalformedLineInvalidJSON, Count: 2, Lines: []int{2, 7}},
				{Class: parser.MalformedLineNoStepFields, Count: 1, Lines: []int{5}},
			})

		var stats SyncStats
		acc.applyTo(&stats)
		assert.Equal(t, 3, stats.Anomalies.MalformedLinesTotal)
		assert.Equal(t, []MalformedLineSample{
			{
				Agent: "antigravity", Path: "transcript.jsonl",
				Class: parser.MalformedLineInvalidJSON,
				Count: 2, Lines: []int{2, 7},
			},
			{
				Agent: "antigravity", Path: "transcript.jsonl",
				Class: parser.MalformedLineNoStepFields,
				Count: 1, Lines: []int{5},
			},
		}, stats.Anomalies.MalformedLineSamples)
	})

	t.Run("record's own source path wins over the session's", func(t *testing.T) {
		var acc anomalyAccumulator
		acc.reset()
		// A brain transcript folded into its conversation's database
		// session: the sample must name the transcript its line numbers
		// refer to, not the session's primary database file.
		acc.recordMalformedLines("antigravity", "conv.db", 1,
			[]parser.ParsedMalformedLineClass{{
				Class:      parser.MalformedLineInvalidJSON,
				Count:      1,
				Lines:      []int{2},
				SourcePath: "brain/id/.system_generated/logs/transcript.jsonl",
			}})

		var stats SyncStats
		acc.applyTo(&stats)
		require.Len(t, stats.Anomalies.MalformedLineSamples, 1)
		assert.Equal(t,
			"brain/id/.system_generated/logs/transcript.jsonl",
			stats.Anomalies.MalformedLineSamples[0].Path,
			"the record names its own file")
	})

	t.Run("per-path dedup drops samples with the count", func(t *testing.T) {
		var acc anomalyAccumulator
		acc.reset()
		classes := []parser.ParsedMalformedLineClass{
			{Class: parser.MalformedLineInvalidJSON, Count: 1, Lines: []int{2}},
		}
		acc.recordMalformedLines("antigravity", "fork.jsonl", 1, classes)
		acc.recordMalformedLines("antigravity", "fork.jsonl", 1, classes)

		var stats SyncStats
		acc.applyTo(&stats)
		assert.Equal(t, 1, stats.Anomalies.MalformedLinesTotal)
		assert.Len(t, stats.Anomalies.MalformedLineSamples, 1,
			"a forked file's samples are recorded once")
	})

	t.Run("clean run records nothing", func(t *testing.T) {
		var acc anomalyAccumulator
		acc.reset()
		acc.recordMalformedLines("antigravity", "ok.jsonl", 0, nil)

		var stats SyncStats
		acc.applyTo(&stats)
		assert.True(t, stats.Anomalies.IsZero())
		assert.Empty(t, stats.Anomalies.MalformedLineSamples)
	})

	t.Run("run cap bounds the sample list", func(t *testing.T) {
		var acc anomalyAccumulator
		acc.reset()
		classes := []parser.ParsedMalformedLineClass{
			{Class: parser.MalformedLineInvalidJSON, Count: 1, Lines: []int{1}},
		}
		for i := range maxMalformedLineSamplesPerRun + 5 {
			acc.recordMalformedLines("antigravity",
				fmt.Sprintf("t%d.jsonl", i), 1, classes)
		}

		var stats SyncStats
		acc.applyTo(&stats)
		assert.Equal(t, maxMalformedLineSamplesPerRun+5,
			stats.Anomalies.MalformedLinesTotal, "totals stay complete")
		assert.Len(t, stats.Anomalies.MalformedLineSamples,
			maxMalformedLineSamplesPerRun, "samples are capped")
	})
}

// TestAnomalyStatsMergeFoldsMalformedLineSamples pins that worker stats
// fold their samples into the parent, still respecting the run cap.
func TestAnomalyStatsMergeFoldsMalformedLineSamples(t *testing.T) {
	var a, b AnomalyStats
	a.RecordMalformedLines("antigravity", 1)
	b.RecordMalformedLines("antigravity", 2)
	b.MalformedLineSamples = []MalformedLineSample{{
		Agent: "antigravity", Path: "t.jsonl",
		Class: parser.MalformedLineInvalidJSON, Count: 2, Lines: []int{3},
	}}

	a.merge(b)
	assert.Equal(t, 3, a.MalformedLinesTotal)
	require.Len(t, a.MalformedLineSamples, 1)
	assert.Equal(t, MalformedLineSample{
		Agent: "antigravity", Path: "t.jsonl",
		Class: parser.MalformedLineInvalidJSON, Count: 2, Lines: []int{3},
	}, a.MalformedLineSamples[0])
}

// TestAnomalyStats_RecordLayoutChange pins the per-root drift counter: the
// total stays complete past the sample cap, and clean runs report nothing.
func TestAnomalyStats_RecordLayoutChange(t *testing.T) {
	t.Run("records each change with its sample", func(t *testing.T) {
		var a AnomalyStats
		a.RecordLayoutChange(parser.CodexLayoutChange{
			Agent: "codex", Root: "/r1", From: "dated", To: "flat",
		})
		a.RecordLayoutChange(parser.CodexLayoutChange{
			Agent: "codex", Root: "/r2", From: "flat+dated", To: "dated",
		})
		assert.Equal(t, 2, a.LayoutChangesTotal)
		assert.Equal(t, []parser.CodexLayoutChange{
			{Agent: "codex", Root: "/r1", From: "dated", To: "flat"},
			{Agent: "codex", Root: "/r2", From: "flat+dated", To: "dated"},
		}, a.LayoutChangeSamples)
		assert.False(t, a.IsZero())
	})

	t.Run("run cap bounds the sample list", func(t *testing.T) {
		var a AnomalyStats
		for i := range maxLayoutChangeSamplesPerRun + 5 {
			a.RecordLayoutChange(parser.CodexLayoutChange{
				Agent: "codex",
				Root:  fmt.Sprintf("/root-%d", i),
				From:  "dated",
				To:    "flat",
			})
		}
		assert.Equal(t, maxLayoutChangeSamplesPerRun+5,
			a.LayoutChangesTotal, "totals stay complete")
		assert.Len(t, a.LayoutChangeSamples,
			maxLayoutChangeSamplesPerRun, "samples are capped")
	})

	t.Run("merge folds totals and capped samples", func(t *testing.T) {
		var a, b AnomalyStats
		a.RecordLayoutChange(parser.CodexLayoutChange{
			Agent: "codex", Root: "/r1", From: "dated", To: "flat",
		})
		b.RecordLayoutChange(parser.CodexLayoutChange{
			Agent: "traex", Root: "/r2", From: "flat", To: "dated",
		})
		a.merge(b)
		assert.Equal(t, 2, a.LayoutChangesTotal)
		require.Len(t, a.LayoutChangeSamples, 2)
		assert.Equal(t, parser.AgentTraeX, a.LayoutChangeSamples[1].Agent)
	})
}

// TestAnomalyAccumulator_RecordCodexLayoutChange verifies the engine-injected
// sink: drift recorded at discovery time lands in the run's stats and a reset
// clears it.
func TestAnomalyAccumulator_RecordCodexLayoutChange(t *testing.T) {
	var acc anomalyAccumulator
	acc.reset()

	acc.RecordCodexLayoutChange(parser.CodexLayoutChange{
		Agent: "codex", Root: "/sessions", From: "dated", To: "flat",
	})

	var stats SyncStats
	acc.applyTo(&stats)
	assert.Equal(t, 1, stats.Anomalies.LayoutChangesTotal)
	require.Len(t, stats.Anomalies.LayoutChangeSamples, 1)
	assert.Equal(t, "/sessions", stats.Anomalies.LayoutChangeSamples[0].Root)
	assert.False(t, stats.Anomalies.IsZero())

	// A fresh run starts from zero.
	acc.reset()
	var next SyncStats
	acc.applyTo(&next)
	assert.True(t, next.Anomalies.IsZero())
	assert.Zero(t, next.Anomalies.LayoutChangesTotal)
	assert.Nil(t, next.Anomalies.LayoutChangeSamples)
}

func TestAnomalyStats_IsZero(t *testing.T) {
	tests := []struct {
		name string
		a    AnomalyStats
		want bool
	}{
		{"empty", AnomalyStats{}, true},
		{
			name: "malformed only",
			a:    AnomalyStats{MalformedLinesTotal: 1},
			want: false,
		},
		{
			name: "unknown schema only",
			a:    AnomalyStats{UnknownSchemaSessionsTotal: 1},
			want: false,
		},
		{
			name: "gen_metadata without usage only",
			a:    AnomalyStats{GenMetadataWithoutUsageTotal: 1},
			want: false,
		},
		{
			name: "sanitize only",
			a:    AnomalyStats{Sanitize: SanitizeStats{ModelClamped: 1}},
			want: false,
		},
		{
			name: "unsupported source layout only",
			a:    AnomalyStats{UnsupportedSourceLayoutsTotal: 1},
			want: false,
		},
		{
			name: "layout change only",
			a:    AnomalyStats{LayoutChangesTotal: 1},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.a.IsZero())
		})
	}
}

func TestAnomalyStats_RecordUnsupportedSourceLayouts(t *testing.T) {
	var a AnomalyStats
	a.RecordUnsupportedSourceLayouts("trae", 2)
	a.RecordUnsupportedSourceLayouts("trae", 1)
	a.RecordUnsupportedSourceLayouts("trae", 0)
	assert.Equal(t, 3, a.UnsupportedSourceLayoutsTotal)
	assert.Equal(t, 3, a.UnsupportedSourceLayoutsByAgent["trae"])
}

func TestAnomalyAccumulator_DedupesUnsupportedSource(t *testing.T) {
	var acc anomalyAccumulator
	acc.recordUnsupportedSourceLayout("trae", "state.vscdb")
	acc.recordUnsupportedSourceLayout("trae", "state.vscdb")

	var stats SyncStats
	acc.applyTo(&stats)
	assert.Equal(t, 1, stats.Anomalies.UnsupportedSourceLayoutsTotal)
	assert.Equal(t, 1, stats.Anomalies.UnsupportedSourceLayoutsByAgent["trae"])
}

func TestProgress_Percent(t *testing.T) {
	tests := []struct {
		name string
		p    Progress
		want float64
	}{
		{
			name: "zero total",
			p:    Progress{SessionsTotal: 0, SessionsDone: 0},
			want: 0,
		},
		{
			name: "half done",
			p:    Progress{SessionsTotal: 10, SessionsDone: 5},
			want: 50,
		},
		{
			name: "all done",
			p:    Progress{SessionsTotal: 4, SessionsDone: 4},
			want: 100,
		},
		{
			name: "one third",
			p:    Progress{SessionsTotal: 3, SessionsDone: 1},
			want: 33.333333,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.p.Percent()
			assert.InDelta(t, tt.want, got, 1e-4)
		})
	}
}

// TestSyncStatsJSONOmitsZeroAnomalies verifies the anomaly JSON fields use
// omitzero semantics: a clean run emits no "anomalies" object at all, and a
// run with only malformed-line counts omits the empty nested "sanitize"
// object. Plain omitempty cannot do this for struct-valued fields.
func TestSyncStatsJSONOmitsZeroAnomalies(t *testing.T) {
	clean, err := json.Marshal(SyncStats{Synced: 3})
	require.NoError(t, err)
	assert.NotContains(t, string(clean), "anomalies",
		"a clean run must not emit an empty anomalies object")

	malformedOnly, err := json.Marshal(SyncStats{
		Anomalies: AnomalyStats{
			MalformedLinesByAgent: map[string]int{"claude": 2},
			MalformedLinesTotal:   2,
		},
	})
	require.NoError(t, err)
	got := string(malformedOnly)
	assert.Contains(t, got, "malformed_lines_total",
		"malformed counts must still serialize")
	assert.NotContains(t, got, "sanitize",
		"malformed-only run must not emit an empty sanitize object")
}

func TestSyncStatsCwdUpdatedSurvivesWorkerJSONRoundTrip(t *testing.T) {
	stats := SyncStats{}
	stats.RecordCwdUpdated(2)
	require.True(t, stats.hasSessionChanges())
	require.True(t, stats.shouldEmitSync())

	payload, err := json.Marshal(stats)
	require.NoError(t, err)
	var restored SyncStats
	require.NoError(t, json.Unmarshal(payload, &restored))

	assert.Equal(t, 2, restored.CwdUpdated,
		"worker-process passes marshal SyncStats; cwd-only updates must survive")
	assert.True(t, restored.hasSessionChanges())
	assert.True(t, restored.shouldEmitSync())
}

func TestMergeSyncStatsCarriesMetadataChanges(t *testing.T) {
	for name, merge := range map[string]func(*SyncStats, SyncStats){
		"reconciliation": mergeReconciliationSyncStats,
		"rebuild":        mergeSyncStats,
	} {
		t.Run(name, func(t *testing.T) {
			dst := SyncStats{CwdUpdated: 1, LinksUpdated: 2}
			merge(&dst, SyncStats{CwdUpdated: 3, LinksUpdated: 4})
			assert.Equal(t, 4, dst.CwdUpdated)
			assert.Equal(t, 6, dst.LinksUpdated)
			assert.True(t, dst.hasSessionChanges(),
				"metadata-only changes must still notify session consumers")
		})
	}
}

func TestAnomalyStats_QuarantineCounters(t *testing.T) {
	var a AnomalyStats
	assert.True(t, a.IsZero(), "a clean run reports no anomalies")

	a.RecordQuarantineSkips("codex", 1)
	a.RecordQuarantineSkips("codex", 2)
	a.RecordQuarantineSkips("claude", 1)
	a.RecordQuarantineSkips("claude", 0)
	a.RecordQuarantineSkips("claude", -1)
	assert.Equal(t, map[string]int{"codex": 3, "claude": 1}, a.QuarantineSkipsByAgent)
	assert.Equal(t, 4, a.QuarantineSkipsTotal)
	assert.False(t, a.IsZero())

	b := AnomalyStats{}
	b.RecordQuarantineRetries("malformed-json", 1)
	b.RecordQuarantineRetries("malformed-json", 1)
	b.RecordQuarantineRetries("transient-io", 3)
	b.RecordQuarantineRetries("shape-violation", 0)
	assert.Equal(t, map[string]int{"malformed-json": 2}, b.QuarantineRetriesByClass)
	assert.Equal(t, 2, b.QuarantineRetriesTotal)

	// merge folds both counters through the same Record paths.
	var merged AnomalyStats
	merged.merge(a)
	merged.merge(b)
	assert.Equal(t, 4, merged.QuarantineSkipsTotal)
	assert.Equal(t, map[string]int{"codex": 3, "claude": 1}, merged.QuarantineSkipsByAgent)
	assert.Equal(t, 2, merged.QuarantineRetriesTotal)
	assert.Equal(t, map[string]int{"malformed-json": 2}, merged.QuarantineRetriesByClass)
}

func TestAnomalyAccumulator_QuarantineSkipDeduplicatedPerSource(t *testing.T) {
	var acc anomalyAccumulator
	acc.recordQuarantineSkip("codex", "codex:/s.jsonl?agent=codex")
	acc.recordQuarantineSkip("codex", "codex:/s.jsonl?agent=codex")
	acc.recordQuarantineSkip("claude", "claude:/t.jsonl?agent=claude")

	var stats SyncStats
	acc.applyTo(&stats)
	anomalies := stats.Anomalies
	assert.Equal(t, map[string]int{"codex": 1, "claude": 1}, anomalies.QuarantineSkipsByAgent)
	assert.Equal(t, 2, anomalies.QuarantineSkipsTotal)

	acc.reset()
	var fresh SyncStats
	acc.applyTo(&fresh)
	assert.Equal(t, 0, fresh.Anomalies.QuarantineSkipsTotal)
}

func TestAnomalyAccumulator_QuarantineRetryCountsOnlyBudgetAbsorbed(t *testing.T) {
	var acc anomalyAccumulator
	acc.recordQuarantineRetry(failurecache.ClassMalformedJSON, 1)
	acc.recordQuarantineRetry(failurecache.ClassTransient, 3)
	acc.recordQuarantineRetry(failurecache.ClassMalformedJSON, 2)

	var stats SyncStats
	acc.applyTo(&stats)
	assert.Equal(t, 1, stats.Anomalies.QuarantineRetriesTotal,
		"only attempts beyond a class's first failure count as retries")
	assert.Equal(t, map[string]int{"malformed-json": 1}, stats.Anomalies.QuarantineRetriesByClass)
}
