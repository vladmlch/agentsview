package main

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/sync/failurecache"
)

// statIdentity records the identity the sync engine would record for path.
func statIdentity(t *testing.T, path string) db.SourceFailure {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err, "stating source")
	return db.SourceFailure{
		MTimeNS: info.ModTime().UnixNano(),
		Size:    info.Size(),
	}
}

func TestCollectFailureRows(t *testing.T) {
	dir := t.TempDir()
	unchanged := filepath.Join(dir, "unchanged.jsonl")
	require.NoError(t, os.WriteFile(unchanged, []byte("line\n"), 0o600))
	unchangedID := statIdentity(t, unchanged)

	changed := filepath.Join(dir, "changed.jsonl")
	require.NoError(t, os.WriteFile(changed, []byte("line\nmore\n"), 0o600))
	// Force an older mtime so the newest-first order holds even on
	// filesystems with coarse timestamp granularity.
	older := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(changed, older, older))
	changedID := statIdentity(t, changed)
	changedID.Size-- // stats moved after the failure was recorded

	database, err := db.Open(t.Context(), filepath.Join(dir, "archive.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, database.ReplaceSourceFailures(t.Context(), map[string]db.SourceFailure{
		unchanged + "?agent=claude": unchangedID,
		changed + "?agent=codex":    changedID,
		// Never agent-qualified: the engine records no agent for sources it
		// discovered before the qualifier existed.
		dir + "/gone.jsonl": {Missing: true},
	}))

	rows, err := collectFailureRows(t.Context(), database)
	require.NoError(t, err)
	require.Len(t, rows, 3)

	bySource := make(map[string]failureRow, len(rows))
	for _, row := range rows {
		bySource[row.Source] = row
	}
	assert.Equal(t, failureRow{
		CacheKey:    unchanged + "?agent=claude",
		Agent:       "claude",
		Source:      unchanged,
		Class:       "parse-failed",
		Recorded:    unchangedID,
		Disposition: failurecache.DispositionWaiting,
	}, bySource[unchanged])
	assert.Equal(t, failurecache.DispositionRetryStatsChanged, bySource[changed].Disposition)
	assert.Equal(t, "codex", bySource[changed].Agent)

	gone := bySource[dir+"/gone.jsonl"]
	assert.Equal(t, "source-missing", gone.Class)
	assert.Equal(t, "", gone.Agent)
	assert.Equal(t, failurecache.DispositionMissing, gone.Disposition)

	// Newest recorded mtime first; the zero mtime of a missing entry sorts
	// last, with the cache key breaking ties deterministically.
	assert.Equal(t, []string{unchanged, changed, dir + "/gone.jsonl"},
		[]string{rows[0].Source, rows[1].Source, rows[2].Source})
}

func TestWriteFailuresHuman(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		var out bytes.Buffer
		require.NoError(t, writeFailuresHuman(&out, nil))
		assert.Equal(t, "No source failures recorded.\n", out.String())
	})

	t.Run("renders every column", func(t *testing.T) {
		dir := t.TempDir()
		source := filepath.Join(dir, "session.jsonl")
		require.NoError(t, os.WriteFile(source, []byte("line\n"), 0o600))
		recorded := statIdentity(t, source)

		var out bytes.Buffer
		require.NoError(t, writeFailuresHuman(&out, []failureRow{{
			Agent:       "claude",
			Source:      source,
			Class:       failureClass(recorded),
			Recorded:    recorded,
			Disposition: failurecache.DispositionWaiting,
		}, {
			Source:      dir + "/gone.jsonl",
			Class:       failureClass(db.SourceFailure{Missing: true}),
			Recorded:    db.SourceFailure{Missing: true},
			Disposition: failurecache.DispositionMissing,
		}}))

		rendered := out.String()
		assert.Contains(t, rendered, "AGENT")
		assert.Contains(t, rendered, "DISPOSITION")
		assert.Contains(t, rendered, "claude")
		assert.Contains(t, rendered, "parse-failed")
		assert.Contains(t, rendered, source)
		assert.Contains(t, rendered, "5 B")
		assert.Contains(t, rendered, "waiting (unchanged since failure)")
		assert.Contains(t, rendered, "(unknown)")
		assert.Contains(t, rendered, "source-missing")
		assert.Contains(t, rendered, "missing (skip until it returns)")
		assert.Contains(t, rendered, emDash)
	})
}

// TestFailuresCommandExecution exercises the command end to end against a
// seeded archive in an isolated data directory: config load, read-only open,
// collection, and both output formats.
func TestFailuresCommandExecution(t *testing.T) {
	dir := testDataDir(t)
	source := filepath.Join(dir, "session.jsonl")
	require.NoError(t, os.WriteFile(source, []byte("line\n"), 0o600))
	recorded := statIdentity(t, source)

	database, err := db.Open(t.Context(), filepath.Join(dir, "sessions.db"))
	require.NoError(t, err)
	require.NoError(t, database.ReplaceSourceFailures(t.Context(), map[string]db.SourceFailure{
		source + "?agent=claude":                          recorded,
		filepath.Join(dir, "gone.jsonl") + "?agent=codex": {Missing: true},
	}))
	require.NoError(t, database.Close())

	var out bytes.Buffer
	jsonCmd := newFailuresCommand()
	jsonCmd.SetOut(&out)
	jsonCmd.SetArgs([]string{"--json"})
	require.NoError(t, jsonCmd.Execute())
	assert.Contains(t, out.String(), `"agent": "claude"`)
	assert.Contains(t, out.String(), `"disposition": "waiting (unchanged since failure)"`)
	assert.Contains(t, out.String(), `"disposition": "missing (skip until it returns)"`)

	out.Reset()
	humanCmd := newFailuresCommand()
	humanCmd.SetOut(&out)
	require.NoError(t, humanCmd.Execute())
	assert.Contains(t, out.String(), source)
	assert.Contains(t, out.String(), "waiting (unchanged since failure)")
	assert.Contains(t, out.String(), "missing (skip until it returns)")
}

func TestWriteFailuresJSON(t *testing.T) {
	recordedAt := time.Date(2024, 10, 1, 10, 15, 0, 0, time.UTC)
	recorded := db.SourceFailure{
		MTimeNS: recordedAt.UnixNano(), Size: 4096, Fingerprint: "sha256:abc",
	}
	var out bytes.Buffer
	require.NoError(t, writeFailuresJSON(&out, []failureRow{{
		Agent:       "claude",
		Source:      "/sessions/a.jsonl",
		Class:       failureClass(recorded),
		Recorded:    recorded,
		Disposition: failurecache.DispositionRetryStatsChanged,
	}, {
		Source:      "/sessions/gone.jsonl",
		Class:       failureClass(db.SourceFailure{Missing: true}),
		Recorded:    db.SourceFailure{Missing: true},
		Disposition: failurecache.DispositionMissing,
	}}))

	var decoded []failureJSON
	require.NoError(t, json.UnmarshalDecode(
		jsontext.NewDecoder(&out), &decoded))
	require.Len(t, decoded, 2)

	assert.Equal(t, failureJSON{
		Agent:             "claude",
		Source:            "/sessions/a.jsonl",
		Class:             "parse-failed",
		RecordedMTime:     recordedAt.Format(time.RFC3339),
		RecordedSizeBytes: 4096,
		Fingerprint:       "sha256:abc",
		Disposition:       "due for retry (stats changed)",
	}, decoded[0])
	assert.Equal(t, failureJSON{
		Source:            "/sessions/gone.jsonl",
		Class:             "source-missing",
		RecordedSizeBytes: 0,
		Disposition:       "missing (skip until it returns)",
	}, decoded[1])
	assert.NotContains(t, out.String(), `"recorded_mtime": ""`, "empty mtime omitted")
}
