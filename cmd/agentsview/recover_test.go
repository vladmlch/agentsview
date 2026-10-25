package main

import (
	"archive/tar"
	"bytes"
	"database/sql"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/db"
)

// recoverTarEntry is one file entry for a fixture transfer tar.
type recoverTarEntry struct {
	name string
	body []byte
}

// buildRecoverTar writes a PAX transfer tar the way
// remotesync.WriteArchive does (hdr.Format = tar.FormatPAX).
func buildRecoverTar(t *testing.T, path string, entries []recoverTarEntry) {
	t.Helper()
	file, err := os.Create(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	writer := tar.NewWriter(file)
	for _, entry := range entries {
		header := &tar.Header{
			Name:   entry.name,
			Mode:   0o644,
			Size:   int64(len(entry.body)),
			Format: tar.FormatPAX,
		}
		require.NoError(t, writer.WriteHeader(header))
		_, err := writer.Write(entry.body)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
}

// createRecoverEngineSchema creates a fresh current-schema archive file.
func createRecoverEngineSchema(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	engine, err := db.OpenFreshIsolatedContext(t.Context(), path)
	require.NoError(t, err)
	require.NoError(t, engine.CloseContext(t.Context()))
}

// buildRecoverSQLiteFixture creates a fresh current-schema archive at
// dir/name with one sessions row, so its rows are importable into a
// recovery destination built from the same engine schema.
func buildRecoverSQLiteFixture(
	t *testing.T, dir, name, sessID, project string,
) string {
	t.Helper()
	path := filepath.Join(dir, name)
	createRecoverEngineSchema(t, path)

	database, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = database.Exec(
		`INSERT INTO sessions (id, project, message_count) VALUES (?, ?, 1)`,
		sessID, project,
	)
	require.NoError(t, err)
	require.NoError(t, database.Close())
	return path
}

// corruptRecoverDBPages overwrites the bytes of the 1-based page range
// [firstPage, lastPage] with 0xFF, simulating mid-file garbage.
func corruptRecoverDBPages(
	t *testing.T, path string, firstPage, lastPage int,
) {
	t.Helper()
	database := openBackupCopy(t, path)
	var pageSize, pageCount int
	require.NoError(t, database.QueryRow("PRAGMA page_size").Scan(&pageSize))
	require.NoError(t, database.QueryRow("PRAGMA page_count").Scan(&pageCount))
	require.NoError(t, database.Close())
	require.GreaterOrEqual(t, pageCount, lastPage,
		"fixture too small to corrupt pages %d..%d", firstPage, lastPage)

	file, err := os.OpenFile(path, os.O_RDWR, 0)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	garbage := bytes.Repeat([]byte{0xFF}, (lastPage-firstPage+1)*pageSize)
	_, err = file.WriteAt(garbage, int64((firstPage-1)*pageSize+64))
	require.NoError(t, err)
}

// seedRecoverWideSessions builds an archive whose sessions table spans
// hundreds of pages (one 64 KiB first_message per session, no messages),
// so corrupting the tail of the file is guaranteed to land inside the
// sessions b-tree while its earlier leaves stay readable.
func seedRecoverWideSessions(t *testing.T, path string, count int) {
	t.Helper()
	createRecoverEngineSchema(t, path)

	database, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	for i := range count {
		_, err := database.Exec(
			`INSERT INTO sessions (id, project, first_message, message_count)
			 VALUES (?, ?, ?, 1)`,
			fmt.Sprintf("wide-sess-%03d", i), "proj",
			strings.Repeat(fmt.Sprintf("%04d ", i), 16400),
		)
		require.NoError(t, err)
	}
	require.NoError(t, database.Close())
}

// runRecoverCmd executes the recover command and returns its stdout.
func runRecoverCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := newRecoverCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// readRecoverReport parses the --json output of a recover run.
func readRecoverReport(t *testing.T, output string) recoverReport {
	t.Helper()
	var report recoverReport
	require.NoError(t, json.UnmarshalDecode(
		jsontext.NewDecoder(strings.NewReader(output)), &report))
	return report
}

func TestRecoverTruncatedTarRecoversReadablePrefix(t *testing.T) {
	dir := t.TempDir()
	healthy := buildRecoverSQLiteFixture(
		t, dir, "healthy.db", "tar-sess-1", "proj-tar")
	healthyBody, err := os.ReadFile(healthy)
	require.NoError(t, err)
	sessionLog := bytes.Repeat([]byte("{\"uuid\":\"u\"}\n"), 128) // 2048 B

	archive := filepath.Join(dir, "transfer.tar")
	buildRecoverTar(t, archive, []recoverTarEntry{
		{name: "/home/wes/.claude/state.db", body: healthyBody},
		{name: "/home/wes/.claude/sessions/abc.jsonl", body: sessionLog},
	})
	info, err := os.Stat(archive)
	require.NoError(t, err)
	// Cut into the last entry's content: the 1024-byte end marker and the
	// tail of the 2048-byte body are gone.
	require.NoError(t, os.Truncate(archive, info.Size()-1024-600))

	dest := filepath.Join(dir, "recovered.db")
	output, err := runRecoverCmd(t, archive, dest, "--json")
	require.NoError(t, err)
	report := readRecoverReport(t, output)

	assert.Equal(t, "tar", report.Lane)
	assert.Equal(t, 2, report.EntriesTotal)
	assert.Equal(t, 1, report.EntriesRecovered,
		"only the fully parsed entries before the damage are recovered")
	require.Len(t, report.EntriesSkipped, 1)
	skip := report.EntriesSkipped[0]
	assert.Equal(t, "/home/wes/.claude/sessions/abc.jsonl", skip.Name)
	assert.Greater(t, skip.Offset, int64(0))
	assert.Contains(t, skip.Reason, "truncated")

	// The readable prefix landed in the destination: the healthy SQLite
	// entry was row-imported and its bytes preserved verbatim.
	database := openBackupCopy(t, dest)
	var sessions int
	require.NoError(t, database.QueryRow("SELECT COUNT(*) FROM sessions").
		Scan(&sessions))
	assert.Equal(t, 1, sessions)
	var artifacts int
	require.NoError(t, database.QueryRow(
		"SELECT COUNT(*) FROM recover_artifacts WHERE kind = 'file'").
		Scan(&artifacts))
	assert.Equal(t, 1, artifacts,
		"the truncated entry's partial bytes are never stored as the file")
	require.NoError(t, database.Close())
}

func TestRecoverTarSkipsCorruptSQLiteEntry(t *testing.T) {
	dir := t.TempDir()
	healthy := buildRecoverSQLiteFixture(
		t, dir, "healthy.db", "tar-sess-1", "proj-tar")
	healthyBody, err := os.ReadFile(healthy)
	require.NoError(t, err)

	corrupt := buildRecoverSQLiteFixture(
		t, dir, "corrupt.db", "tar-sess-2", "proj-tar")
	corruptRecoverDBPages(t, corrupt, 2, 3)
	corruptBody, err := os.ReadFile(corrupt)
	require.NoError(t, err)

	archive := filepath.Join(dir, "transfer.tar")
	buildRecoverTar(t, archive, []recoverTarEntry{
		{name: "/home/wes/.hermes/state.db", body: healthyBody},
		{name: "/home/wes/.zed/threads.db", body: corruptBody},
		{name: "/home/wes/.claude/plain.txt", body: []byte("hello")},
	})

	dest := filepath.Join(dir, "recovered.db")
	output, err := runRecoverCmd(t, archive, dest, "--json")
	require.NoError(t, err)
	report := readRecoverReport(t, output)

	assert.Equal(t, 3, report.EntriesTotal)
	assert.Equal(t, 3, report.EntriesRecovered,
		"every entry parses fully at the tar level")
	require.Len(t, report.EntriesSkipped, 1)
	assert.Equal(t, "/home/wes/.zed/threads.db",
		report.EntriesSkipped[0].Name)
	assert.Contains(t, report.EntriesSkipped[0].Reason, "sqlite entry")

	// The corrupt entry's rows were never imported; the healthy entry's
	// were, and the corrupt entry's bytes are preserved as evidence.
	database := openBackupCopy(t, dest)
	var sessions []string
	rows, err := database.Query("SELECT id FROM sessions ORDER BY id")
	require.NoError(t, err)
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		sessions = append(sessions, id)
	}
	require.NoError(t, rows.Close())
	assert.Equal(t, []string{"tar-sess-1"}, sessions)
	var quickCheck sql.NullString
	require.NoError(t, database.QueryRow(
		`SELECT sqlite_quick_check FROM recover_artifacts
		 WHERE name = '/home/wes/.zed/threads.db'`).Scan(&quickCheck))
	require.True(t, quickCheck.Valid, "corrupt entry must carry a verdict")
	assert.NotEqual(t, "ok", quickCheck.String)
	require.NoError(t, database.Close())
}

func TestRecoverKeepsFirstDuplicatePrimaryKey(t *testing.T) {
	dir := t.TempDir()
	first := buildRecoverSQLiteFixture(
		t, dir, "first.db", "dupe-sess", "project-first")
	second := buildRecoverSQLiteFixture(
		t, dir, "second.db", "dupe-sess", "project-second")
	firstBody, err := os.ReadFile(first)
	require.NoError(t, err)
	secondBody, err := os.ReadFile(second)
	require.NoError(t, err)

	archive := filepath.Join(dir, "transfer.tar")
	buildRecoverTar(t, archive, []recoverTarEntry{
		{name: "first/state.db", body: firstBody},
		{name: "second/state.db", body: secondBody},
	})

	dest := filepath.Join(dir, "recovered.db")
	output, err := runRecoverCmd(t, archive, dest, "--json")
	require.NoError(t, err)
	report := readRecoverReport(t, output)

	database := openBackupCopy(t, dest)
	var project string
	require.NoError(t, database.QueryRow(
		"SELECT project FROM sessions WHERE id = 'dupe-sess'").Scan(&project))
	assert.Equal(t, "project-first", project,
		"the first recovered occurrence of a primary key is authoritative")
	require.NoError(t, database.Close())

	// The destination keeps its own seeded identity rows (archive
	// metadata, stats) as first occurrences, so the ledger records those
	// conflicts too; the sessions conflict must be exactly one.
	var sessionSkips []recoverRowSkip
	for _, rowSkip := range report.RowSkips {
		if rowSkip.Table == "sessions" {
			sessionSkips = append(sessionSkips, rowSkip)
		}
	}
	require.Len(t, sessionSkips, 1)
	assert.Equal(t, "id=dupe-sess", sessionSkips[0].Key)
	assert.Contains(t, sessionSkips[0].Reason, "identity conflict")
}

func TestRecoverDamagedStandaloneSnapshotRecoversReadablePrefix(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "damaged-snapshot.db")
	seedRecoverWideSessions(t, source, 12)
	database := openBackupCopy(t, source)
	var pageCount int
	require.NoError(t, database.QueryRow("PRAGMA page_count").Scan(&pageCount))
	var integrity string
	require.NoError(t, database.QueryRow("PRAGMA quick_check").Scan(&integrity))
	require.Equal(t, "ok", integrity, "fixture must be healthy before damage")
	require.NoError(t, database.Close())

	corruptRecoverDBPages(t, source, pageCount-59, pageCount)

	dest := filepath.Join(dir, "recovered.db")
	output, err := runRecoverCmd(t, source, dest, "--json")
	require.NoError(t, err)
	report := readRecoverReport(t, output)

	assert.Equal(t, "sqlite-rows", report.Lane)
	require.Len(t, report.EntriesSkipped, 1)
	assert.Contains(t, report.EntriesSkipped[0].Reason, "quick_check")
	require.NotEmpty(t, report.Breakpoints,
		"the corrupt pages must cut at least one table at a breakpoint")
	breakpointTables := make(map[string]recoverBreakpoint)
	for _, bp := range report.Breakpoints {
		breakpointTables[bp.Table] = bp
	}
	sessionsBP, ok := breakpointTables["sessions"]
	require.True(t, ok,
		"the sessions table spans the corrupted pages: %v", report.Breakpoints)
	assert.Greater(t, sessionsBP.RowsSoFar, int64(0))
	assert.Less(t, sessionsBP.RowsSoFar, int64(12))
	assert.Equal(t, sessionsBP.RowsSoFar, report.RowsPerTable["sessions"])
	assert.Less(t, report.RowsPerTable["sessions"], int64(12))
	for _, rowSkip := range report.RowSkips {
		assert.NotEqual(t, "sessions", rowSkip.Table)
	}

	// The destination itself is a healthy archive holding exactly the
	// recovered rows.
	destDB := openBackupCopy(t, dest)
	require.NoError(t, destDB.QueryRow("PRAGMA quick_check").Scan(&integrity))
	assert.Equal(t, "ok", integrity)
	var recovered int
	require.NoError(t, destDB.QueryRow("SELECT COUNT(*) FROM sessions").
		Scan(&recovered))
	assert.Equal(t, report.RowsPerTable["sessions"], int64(recovered))
	require.NoError(t, destDB.Close())
}

func TestRecoverHealthySnapshotStraightCopy(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "snapshot.db")
	seedRecoverWideSessions(t, source, 3)

	dest := filepath.Join(dir, "recovered.db")
	output, err := runRecoverCmd(t, source, dest, "--json")
	require.NoError(t, err)
	report := readRecoverReport(t, output)

	assert.Equal(t, "sqlite-copy", report.Lane)
	assert.Equal(t, 1, report.EntriesTotal)
	assert.Equal(t, 1, report.EntriesRecovered)
	assert.Empty(t, report.EntriesSkipped)
	assert.Empty(t, report.RowsPerTable)

	// The straight copy is a standalone healthy database with the
	// source's rows and no recovery ledger added; the online-backup API
	// rewrites the file, so fidelity is checked by content, not bytes.
	database := openBackupCopy(t, dest)
	var integrity string
	require.NoError(t, database.QueryRow("PRAGMA quick_check").Scan(&integrity))
	assert.Equal(t, "ok", integrity)
	var sessions int
	require.NoError(t, database.QueryRow("SELECT COUNT(*) FROM sessions").
		Scan(&sessions))
	assert.Equal(t, 3, sessions)
	var ledgerTables int
	require.NoError(t, database.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master
		 WHERE name LIKE 'recover_%'`).Scan(&ledgerTables))
	assert.Equal(t, 0, ledgerTables,
		"a straight copy adds no recovery bookkeeping")
	require.NoError(t, database.Close())
	assert.NoFileExists(t, dest+recoverTmpSuffix)
	assert.NoFileExists(t, dest+"-wal")
	assert.NoFileExists(t, dest+"-shm")
}

func TestRecoverRefusesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "snapshot.db")
	seedRecoverWideSessions(t, source, 1)

	dest := filepath.Join(dir, "recovered.db")
	require.NoError(t, os.WriteFile(dest, []byte("precious"), 0o644))

	_, err := runRecoverCmd(t, source, dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to overwrite")

	contents, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "precious", string(contents),
		"an existing destination is never touched")
}

func TestRecoverJSONReportShape(t *testing.T) {
	dir := t.TempDir()
	healthy := buildRecoverSQLiteFixture(
		t, dir, "healthy.db", "tar-sess-1", "proj-tar")
	healthyBody, err := os.ReadFile(healthy)
	require.NoError(t, err)
	archive := filepath.Join(dir, "transfer.tar")
	buildRecoverTar(t, archive, []recoverTarEntry{
		{name: "state.db", body: healthyBody},
	})

	dest := filepath.Join(dir, "recovered.db")
	output, err := runRecoverCmd(t, archive, dest, "--json")
	require.NoError(t, err)

	var report map[string]any
	require.NoError(t, json.UnmarshalDecode(
		jsontext.NewDecoder(strings.NewReader(output)), &report))
	for _, field := range []string{
		"source", "destination", "lane", "entries_total",
		"entries_recovered", "entries_skipped", "rows_per_table",
		"identity_skips",
	} {
		require.Contains(t, report, field, "report field %s", field)
	}
	assert.Equal(t, "tar", report["lane"])
	assert.Equal(t, float64(1), report["entries_total"])
	assert.Equal(t, float64(1), report["entries_recovered"])
}

func TestRecoverLeavesSourceUntouched(t *testing.T) {
	dir := t.TempDir()
	healthy := buildRecoverSQLiteFixture(
		t, dir, "healthy.db", "tar-sess-1", "proj-tar")
	healthyBody, err := os.ReadFile(healthy)
	require.NoError(t, err)
	archive := filepath.Join(dir, "transfer.tar")
	buildRecoverTar(t, archive, []recoverTarEntry{
		{name: "state.db", body: healthyBody},
		{name: "sessions/a.jsonl", body: []byte("{}")},
	})
	before, _, err := hashFileSHA256(archive)
	require.NoError(t, err)

	dest := filepath.Join(dir, "recovered.db")
	humanOutput, err := runRecoverCmd(t, archive, dest)
	require.NoError(t, err)
	assert.Contains(t, humanOutput, "Recovery complete.")
	assert.Contains(t, humanOutput, "2 recovered of 2")

	after, _, err := hashFileSHA256(archive)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the source archive is read-only")
}

func TestRecoverRefusesUnrecognizedFormat(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "mystery.bin")
	require.NoError(t, os.WriteFile(source, []byte("not an archive"), 0o644))

	dest := filepath.Join(dir, "recovered.db")
	_, err := runRecoverCmd(t, source, dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither a remotesync transfer tar")
	assert.NoFileExists(t, dest)
	assert.NoFileExists(t, dest+recoverTmpSuffix)
}

func TestRecoverRespectsPageSizeFlag(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "damaged-snapshot.db")
	seedRecoverWideSessions(t, source, 12)
	var pageCount int
	database := openBackupCopy(t, source)
	require.NoError(t, database.QueryRow("PRAGMA page_count").Scan(&pageCount))
	require.NoError(t, database.Close())
	corruptRecoverDBPages(t, source, pageCount-59, pageCount)

	dest := filepath.Join(dir, "recovered.db")
	output, err := runRecoverCmd(t, source, dest, "--page-size", "2", "--json")
	require.NoError(t, err)
	report := readRecoverReport(t, output)
	assert.Greater(t, report.RowsPerTable["sessions"], int64(0),
		"a tiny page size still walks the readable prefix")
}

func TestRecoverRequiresTwoArgs(t *testing.T) {
	_, err := runRecoverCmd(t, "only-one-arg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 2 arg")
}
