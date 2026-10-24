package sync

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/parser"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTempFamily creates the resync temp family files next to dbPath with
// the given contents and ages them past the orphan threshold when aged is
// true. It returns the family paths and their total byte size.
func writeTempFamily(t *testing.T, dbPath string, aged bool, main, wal, shm string) ([]string, int64) {
	t.Helper()
	tempPath := dbPath + resyncTempSuffix
	paths := tempDBFamilyPaths(tempPath)
	contents := []string{main, wal, shm}
	var total int64
	for i, path := range paths {
		require.NoError(t, os.WriteFile(path, []byte(contents[i]), 0o600))
		total += int64(len(contents[i]))
		if aged {
			past := time.Now().Add(-2 * resyncTempOrphanAge)
			require.NoError(t, os.Chtimes(path, past, past))
		}
	}
	return paths, total
}

func TestSweepResyncTempRemovesAgedFamily(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	_, total := writeTempFamily(t, dbPath, true,
		"stale main", "stale wal", "stale shm")

	cleared, reclaimed := SweepResyncTemp(dbPath)

	assert.True(t, cleared, "an aged family is provably orphaned")
	assert.Equal(t, total, reclaimed, "reclaimed bytes must cover the family")
	for _, path := range tempDBFamilyPaths(dbPath + resyncTempSuffix) {
		assert.NoFileExists(t, path)
	}
}

func TestSweepResyncTempMissingFamilyIsClear(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")

	cleared, reclaimed := SweepResyncTemp(dbPath)

	assert.True(t, cleared, "no leftover means the path is clear for a build")
	assert.Zero(t, reclaimed)
}

func TestSweepResyncTempKeepsYoungFamily(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	paths, _ := writeTempFamily(t, dbPath, false, "live main", "live wal", "live shm")

	cleared, reclaimed := SweepResyncTemp(dbPath)

	assert.False(t, cleared,
		"a young family may belong to a live concurrent build")
	assert.Zero(t, reclaimed)
	for _, path := range paths {
		assert.FileExists(t, path, "young family members must never be reaped")
	}
}

func TestSweepResyncTempMixedAgesRemoveNothing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	// Aged main database but a young WAL: the family is not provably
	// orphaned, and reaping only part of it could corrupt a live build.
	_, _ = writeTempFamily(t, dbPath, false, "main", "wal", "shm")
	past := time.Now().Add(-2 * resyncTempOrphanAge)
	require.NoError(t, os.Chtimes(dbPath+resyncTempSuffix, past, past))

	cleared, reclaimed := SweepResyncTemp(dbPath)

	assert.False(t, cleared, "one non-orphan member keeps the whole family")
	assert.Zero(t, reclaimed)
	for _, path := range tempDBFamilyPaths(dbPath + resyncTempSuffix) {
		assert.FileExists(t, path)
	}
}

func TestSweepResyncTempIgnoresForeignNames(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	_, _ = writeTempFamily(t, dbPath, true, "stale", "wal", "shm")
	foreign := []string{
		dbPath, // the archive itself is never a sweep target
		dbPath + "-resync-old",
		dbPath + ".resync",
		filepath.Join(filepath.Dir(dbPath), "resync"),
	}
	for _, path := range foreign {
		require.NoError(t, os.WriteFile(path, []byte("keep me"), 0o600))
	}

	cleared, reclaimed := SweepResyncTemp(dbPath)

	require.True(t, cleared)
	assert.Equal(t, int64(len("stale")+len("wal")+len("shm")), reclaimed,
		"only the exact family names are reclaimed")
	for _, path := range foreign {
		assert.FileExists(t, path, "wrong-name files must be kept")
	}
}

func TestSweepResyncTempKeepsSymlinkAndDirectory(t *testing.T) {
	t.Run("symlink main file", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "sessions.db")
		victim := filepath.Join(dir, "victim.db")
		require.NoError(t, os.WriteFile(victim, []byte("victim"), 0o600))
		require.NoError(t, os.Symlink(victim, dbPath+resyncTempSuffix))

		cleared, reclaimed := SweepResyncTemp(dbPath)

		assert.False(t, cleared, "a symlink is never followed or removed")
		assert.Zero(t, reclaimed)
		assert.FileExists(t, victim)
		assert.FileExists(t, dbPath+resyncTempSuffix,
			"the symlink itself is kept rather than unlinked")
	})

	t.Run("directory in family", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "sessions.db")
		require.NoError(t, os.Mkdir(dbPath+resyncTempSuffix, 0o700))

		cleared, reclaimed := SweepResyncTemp(dbPath)

		assert.False(t, cleared, "a directory is never removed")
		assert.Zero(t, reclaimed)
		assert.DirExists(t, dbPath+resyncTempSuffix)
	})
}

func TestRemoveTempDBRemovesOwnFamilyRegardlessOfAge(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")
	require.NoError(t, os.WriteFile(dbPath, []byte("archive"), 0o600))
	paths, total := writeTempFamily(t, dbPath, false, "own main", "own wal", "own shm")

	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	removeTempDB(dbPath + resyncTempSuffix)

	for _, path := range paths {
		assert.NoFileExists(t, path,
			"own just-built temp is removed without an age gate")
	}
	assert.FileExists(t, dbPath, "the archive database is never removed")
	assert.Contains(t, logs.String(),
		fmt.Sprintf("reclaimed %d bytes", total),
		"removals must report the reclaimed byte total")
}

func TestRemoveTempDBSkipsSymlinkFamily(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")
	victim := filepath.Join(dir, "victim.db")
	require.NoError(t, os.WriteFile(victim, []byte("victim"), 0o600))
	tempPath := dbPath + resyncTempSuffix
	require.NoError(t, os.Symlink(victim, tempPath))
	require.NoError(t, os.WriteFile(tempPath+"-wal", []byte("wal"), 0o600))

	removeTempDB(tempPath)

	assert.FileExists(t, victim)
	assert.FileExists(t, tempPath,
		"a symlinked family member keeps the whole family in place")
	assert.FileExists(t, tempPath+"-wal")
}

func TestRemoveWALRemovesSidecarsOnly(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")
	require.NoError(t, os.WriteFile(dbPath, []byte("archive"), 0o600))
	require.NoError(t, os.WriteFile(dbPath+"-wal", []byte("wal"), 0o600))
	require.NoError(t, os.WriteFile(dbPath+"-shm", []byte("shm"), 0o600))

	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	removeWAL(dbPath)

	assert.FileExists(t, dbPath, "the database file itself is never removed")
	assert.NoFileExists(t, dbPath+"-wal")
	assert.NoFileExists(t, dbPath+"-shm")
	assert.Contains(t, logs.String(), "reclaimed 6 bytes")
}

func TestResyncBuildRefusesYoungTempFamily(t *testing.T) {
	root := t.TempDir()
	database, err := db.Open(
		t.Context(), filepath.Join(t.TempDir(), "archive.db"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	engine := NewEngine(t.Context(), database, EngineConfig{
		AgentDirs: map[parser.AgentType][]string{parser.AgentClaude: {root}},
		Machine:   "local",
	})
	t.Cleanup(engine.Close)
	tempPath := engine.ResyncTempPath()
	require.NoError(t, os.WriteFile(tempPath, []byte("in-flight build"), 0o600))

	returnedPath, stats, err := engine.ResyncBuild(t.Context(), nil)

	require.NoError(t, err,
		"a possibly-live temp family is a safety abort, not an error")
	assert.True(t, stats.Aborted)
	require.NotEmpty(t, stats.Warnings)
	assert.Contains(t, stats.Warnings[0], "not provably orphaned")
	assert.Equal(t, tempPath, returnedPath)
	assert.FileExists(t, tempPath,
		"a young temp family must never be reaped or built over")
	assert.FileExists(t, database.Path(),
		"the archive database itself is never touched")
}
