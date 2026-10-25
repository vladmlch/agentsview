package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/dbtest"
)

// seedBackupArchive creates a small current-schema archive at
// dir/sessions.db with two sessions of msgsPerSession messages each and
// closes it, so the backup command sees a quiescent file.
func seedBackupArchive(t *testing.T, dir string, msgsPerSession int) {
	t.Helper()
	database, err := db.Open(t.Context(), filepath.Join(dir, "sessions.db"))
	require.NoError(t, err)
	for _, seeded := range []struct {
		id      string
		project string
		role    string
	}{
		{"backup-sess-1", "proj-a", "user"},
		{"backup-sess-2", "proj-b", "assistant"},
	} {
		var msgs []db.Message
		for i := range msgsPerSession {
			content := strings.Repeat("x", 200) + fmt.Sprintf(" body %04d", i)
			if seeded.role == "user" {
				msgs = append(msgs, dbtest.UserMsg(seeded.id, i, content))
			} else {
				msgs = append(msgs, dbtest.AsstMsg(seeded.id, i, content))
			}
		}
		dbtest.SeedSessionWithMessages(t, database, seeded.id, seeded.project,
			msgs, dbtest.WithMessageCounts(msgsPerSession, msgsPerSession))
	}
	require.NoError(t, database.Close())
}

// openBackupCopy opens a backup file read-only for row-count checks.
func openBackupCopy(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite3", doctorReadOnlyDSN(path))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	return database
}

// sha256OfFile hashes a file the same way the backup command does.
func sha256OfFile(t *testing.T, path string) string {
	t.Helper()
	digest, _, err := hashFileSHA256(path)
	require.NoError(t, err)
	return digest
}

func TestBackupCommandCreatesVerifiedSnapshot(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	dest := filepath.Join(dir, "backups", "archive.db")

	var out bytes.Buffer
	cmd := newBackupCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{dest})
	require.NoError(t, cmd.Execute())

	// The copy is a valid, standalone SQLite database with the source's
	// rows, and carries no sidecars.
	require.FileExists(t, dest)
	database := openBackupCopy(t, dest)
	var sessions, messages int
	require.NoError(t, database.QueryRow("SELECT COUNT(*) FROM sessions").
		Scan(&sessions))
	require.NoError(t, database.QueryRow("SELECT COUNT(*) FROM messages").
		Scan(&messages))
	assert.Equal(t, 2, sessions)
	assert.Equal(t, 2, messages)
	var quickCheck string
	require.NoError(t, database.QueryRow("PRAGMA quick_check").Scan(&quickCheck))
	assert.Equal(t, "ok", quickCheck)
	assert.NoFileExists(t, dest+"-wal")
	assert.NoFileExists(t, dest+"-shm")
	assert.NoFileExists(t, dest+backupTmpSuffix)

	// The receipt records a verified, reproducible snapshot.
	var receipt backupReceipt
	encoded, err := os.ReadFile(dest + backupReceiptSuffix)
	require.NoError(t, err)
	require.NoError(t, json.UnmarshalDecode(
		jsontext.NewDecoder(bytes.NewReader(encoded)), &receipt))

	cfg, err := config.LoadReadOnly()
	require.NoError(t, err)
	createdAt, err := time.Parse(time.RFC3339, receipt.CreatedAt)
	require.NoError(t, err, "created_at must parse as RFC 3339")
	assert.WithinDuration(t, time.Now(), createdAt, time.Minute)
	assert.Equal(t, cfg.DBPath, receipt.SourceDB)
	assert.Equal(t, sha256OfFile(t, dest), receipt.SourceSha256)
	assert.Equal(t, receipt.SourceSha256, receipt.BackupSha256,
		"installed file must hash to the recorded snapshot digest")
	assert.Equal(t, "ok", receipt.Integrity)
	assert.Equal(t, db.CurrentDataVersion(), receipt.DataVersion)
	assert.Equal(t, version, receipt.ToolVersion)

	human := out.String()
	assert.Contains(t, human, "Backup complete.")
	assert.Contains(t, human, dest)
	assert.Contains(t, human, receipt.BackupSha256)
	assert.Contains(t, human, "ok (PRAGMA quick_check)")
}

// The --json payload is exactly the receipt object written to the sidecar
// file, so consumers can read either one.
func TestBackupCommandJSONMatchesReceipt(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	dest := filepath.Join(dir, "archive-backup.db")

	var out bytes.Buffer
	cmd := newBackupCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{dest, "--json"})
	require.NoError(t, cmd.Execute())

	encoded, err := os.ReadFile(dest + backupReceiptSuffix)
	require.NoError(t, err)
	assert.Equal(t, string(encoded), out.String())

	var receipt backupReceipt
	require.NoError(t, json.UnmarshalDecode(
		jsontext.NewDecoder(strings.NewReader(out.String())), &receipt))
	assert.Equal(t, "ok", receipt.Integrity)
	assert.Equal(t, 64, len(receipt.BackupSha256))
}

func TestBackupCommandRefusesExistingDestination(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	dest := filepath.Join(dir, "archive-backup.db")
	writeTestFile(t, dest, []byte("existing backup"))

	cmd := newBackupCommand()
	cmd.SetArgs([]string{dest})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.Contains(t, err.Error(), "refusing to overwrite")

	existing, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "existing backup", string(existing))
	assert.NoFileExists(t, dest+backupReceiptSuffix)
}

func TestBackupCommandRefusesHeldDestinationLock(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	dest := filepath.Join(dir, "archive-backup.db")

	held := flock.New(dest + backupLockSuffix)
	locked, err := held.TryLock()
	require.NoError(t, err)
	require.True(t, locked, "test must hold the destination lock")
	t.Cleanup(func() { require.NoError(t, held.Close()) })

	cmd := newBackupCommand()
	cmd.SetArgs([]string{dest})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), backupLockSuffix)
	assert.NoFileExists(t, dest)
	assert.NoFileExists(t, dest+backupReceiptSuffix)
}

// A corrupt archive must be reported, not silently skipped: the command
// fails and leaves no destination, receipt, or partial copy behind.
func TestBackupCommandReportsCorruptSource(t *testing.T) {
	t.Run("not a database", func(t *testing.T) {
		dir := testDataDir(t)
		writeTestFile(t, filepath.Join(dir, "sessions.db"),
			[]byte("this is not a database"))
		dest := filepath.Join(dir, "archive-backup.db")

		cmd := newBackupCommand()
		cmd.SetArgs([]string{dest})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "snapshotting archive")
		assert.NoFileExists(t, dest)
		assert.NoFileExists(t, dest+backupReceiptSuffix)
		assert.NoFileExists(t, dest+backupTmpSuffix)
	})

	t.Run("damaged page fails quick_check", func(t *testing.T) {
		dir := testDataDir(t)
		seedBackupArchive(t, dir, 100)
		source := filepath.Join(dir, "sessions.db")

		// Overwrite a middle data page with garbage: the online backup
		// copies pages blindly, so verification of the copy must catch it.
		database := openBackupCopy(t, source)
		var pageSize, pageCount int
		require.NoError(t, database.QueryRow("PRAGMA page_size").Scan(&pageSize))
		require.NoError(t, database.QueryRow("PRAGMA page_count").Scan(&pageCount))
		require.GreaterOrEqual(t, pageCount, 3,
			"fixture needs several pages for a data-page corruption")
		require.NoError(t, database.Close())
		page := make([]byte, pageSize)
		for i := range page {
			page[i] = 0xFF
		}
		offset := int64(pageSize * (pageCount / 2))
		file, err := os.OpenFile(source, os.O_WRONLY, 0)
		require.NoError(t, err)
		_, err = file.WriteAt(page, offset)
		require.NoError(t, err)
		require.NoError(t, file.Close())

		dest := filepath.Join(dir, "archive-backup.db")
		cmd := newBackupCommand()
		cmd.SetArgs([]string{dest})
		err = cmd.Execute()
		require.Error(t, err)
		// Verification can fail either way PRAGMA quick_check reports:
		// problem rows, or a corrupt-database error while checking.
		assert.Contains(t, err.Error(), "verifying backup copy")
		assert.NoFileExists(t, dest)
		assert.NoFileExists(t, dest+backupReceiptSuffix)
		assert.NoFileExists(t, dest+backupTmpSuffix)
	})
}

// A truncated archive shorter than its header page count must fail the
// online backup rather than produce a short copy.
func TestBackupCommandReportsTruncatedSource(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	source := filepath.Join(dir, "sessions.db")
	info, err := os.Stat(source)
	require.NoError(t, err)
	require.NoError(t, os.Truncate(source, info.Size()/2))
	dest := filepath.Join(dir, "archive-backup.db")

	cmd := newBackupCommand()
	cmd.SetArgs([]string{dest})
	err = cmd.Execute()
	require.Error(t, err)
	assert.NoFileExists(t, dest)
	assert.NoFileExists(t, dest+backupReceiptSuffix)
}

// hashFileSHA256 must produce the plain hex SHA-256 of the file bytes.
func TestHashFileSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blob")
	content := []byte("agentsview backup digest")
	writeTestFile(t, path, content)
	sum := sha256.Sum256(content)

	digest, size, err := hashFileSHA256(path)
	require.NoError(t, err)
	assert.Equal(t, hex.EncodeToString(sum[:]), digest)
	assert.Equal(t, int64(len(content)), size)
}
