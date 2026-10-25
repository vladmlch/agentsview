package main

import (
	"bytes"
	"database/sql"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/dbtest"
)

// createRestoreBackup runs `agentsview backup` against the seeded archive
// and returns the destination together with its recorded receipt.
func createRestoreBackup(t *testing.T, dir string) (string, backupReceipt) {
	t.Helper()
	dest := filepath.Join(dir, "backups", "snapshot.db")
	cmd := newBackupCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{dest})
	require.NoError(t, cmd.Execute())

	encoded, err := os.ReadFile(dest + backupReceiptSuffix)
	require.NoError(t, err)
	var receipt backupReceipt
	require.NoError(t, json.UnmarshalDecode(
		jsontext.NewDecoder(bytes.NewReader(encoded)), &receipt))
	return dest, receipt
}

// runRestore runs the restore command with args and returns its output.
func runRestore(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := newRestoreCommand()
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

var restoreFingerprintPattern = regexp.MustCompile(
	`Plan fingerprint:\s+([0-9a-f]{64})`)

// rehearseRestoreFingerprint runs a rehearsal and returns the plan
// fingerprint printed by it.
func rehearseRestoreFingerprint(t *testing.T, backupPath string) string {
	t.Helper()
	out, err := runRestore(t, backupPath)
	require.NoError(t, err)
	match := restoreFingerprintPattern.FindStringSubmatch(out)
	require.NotNil(t, match, "rehearsal output must print the plan fingerprint")
	return match[1]
}

// addSessionToLive reopens the seeded archive and appends one more session,
// so the live archive diverges from the backup taken earlier.
func addSessionToLive(t *testing.T, dir string) {
	t.Helper()
	database, err := db.Open(t.Context(), filepath.Join(dir, "sessions.db"))
	require.NoError(t, err)
	dbtest.SeedSessionWithMessages(t, database, "post-backup-sess", "proj-c",
		[]db.Message{dbtest.UserMsg("post-backup-sess", 0,
			strings.Repeat("y", 200))},
		dbtest.WithMessageCounts(1, 1))
	require.NoError(t, database.Close())
}

func appendByteToFile(t *testing.T, path string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	require.NoError(t, err)
	_, err = file.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

// countRestoreAsides returns the aside copies (without their sidecars) left
// next to the live archive. An aside copy carries the replaced archive's
// -wal/-shm sidecars under the same names, so they are filtered here.
func countRestoreAsides(t *testing.T, live string) []string {
	t.Helper()
	return globMainRestoreFiles(t, live+restoreAsideInfix+"*")
}

func countRestoreUndones(t *testing.T, live string) []string {
	t.Helper()
	return globMainRestoreFiles(t, live+restoreUndoneInfix+"*")
}

func globMainRestoreFiles(t *testing.T, pattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	require.NoError(t, err)
	var mains []string
	for _, match := range matches {
		if strings.HasSuffix(match, "-wal") ||
			strings.HasSuffix(match, "-shm") ||
			strings.HasSuffix(match, "-journal") {
			continue
		}
		mains = append(mains, match)
	}
	return mains
}

// reversionRestoreBackup rewrites the backup's SQLite user_version and
// records the changed file in its receipt, simulating a snapshot taken by a
// binary with a different data version.
func reversionRestoreBackup(t *testing.T, dest string, dataVersion int) {
	t.Helper()
	database, err := sql.Open("sqlite3", dest)
	require.NoError(t, err)
	_, err = database.Exec(
		fmt.Sprintf("PRAGMA user_version = %d", dataVersion))
	require.NoError(t, err)
	require.NoError(t, database.Close())
	assert.NoFileExists(t, dest+"-wal")
	assert.NoFileExists(t, dest+"-shm")

	encoded, err := os.ReadFile(dest + backupReceiptSuffix)
	require.NoError(t, err)
	var receipt backupReceipt
	require.NoError(t, json.UnmarshalDecode(
		jsontext.NewDecoder(bytes.NewReader(encoded)), &receipt))
	receipt.BackupSha256 = sha256OfFile(t, dest)
	receipt.DataVersion = dataVersion
	require.NoError(t, writeBackupReceipt(dest, receipt))
}

func TestRestoreRehearsalPrintsPlanAndTouchesNothing(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	dest, receipt := createRestoreBackup(t, dir)
	live := filepath.Join(dir, "sessions.db")
	liveInfoBefore, err := os.Stat(live)
	require.NoError(t, err)
	liveShaBefore := sha256OfFile(t, live)
	backupShaBefore := sha256OfFile(t, dest)

	out, err := runRestore(t, dest)
	require.NoError(t, err)

	// The plan names everything the apply would do.
	assert.Contains(t, out, "Restore rehearsal")
	assert.Contains(t, out, live)
	assert.Contains(t, out, dest)
	assert.Contains(t, out, dest+backupReceiptSuffix)
	assert.Contains(t, out, receipt.BackupSha256)
	assert.Contains(t, out, "ok (PRAGMA quick_check)")
	assert.Contains(t, out, fmt.Sprintf("Backup data version: %d",
		db.CurrentDataVersion()))
	assert.Contains(t, out, live+restoreAsideInfix)
	assert.Contains(t, out, "restore --undo")
	assert.Contains(t, out, "Rehearsal only")
	assert.Contains(t, out, "--expect-fingerprint")

	fingerprint := restoreFingerprintPattern.FindStringSubmatch(out)
	require.NotNil(t, fingerprint, "the plan must print the fingerprint")
	assert.Len(t, fingerprint[1], 64)

	// Nothing on disk changed: the live archive and the backup keep their
	// bytes and timestamps, and no aside or receipt appears.
	liveInfoAfter, err := os.Stat(live)
	require.NoError(t, err)
	assert.Equal(t, liveInfoBefore.ModTime(), liveInfoAfter.ModTime())
	assert.Equal(t, liveInfoBefore.Size(), liveInfoAfter.Size())
	assert.Equal(t, liveShaBefore, sha256OfFile(t, live))
	assert.Equal(t, backupShaBefore, sha256OfFile(t, dest))
	assert.Empty(t, countRestoreAsides(t, live))
	assert.NoFileExists(t, live+restoreReceiptSuffix)
}

func TestRestoreApplyReplacesLiveArchive(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	dest, receipt := createRestoreBackup(t, dir)
	live := filepath.Join(dir, "sessions.db")
	addSessionToLive(t, dir)
	liveShaBefore := sha256OfFile(t, live)
	liveSidecarsBefore := existingSidecars(live)
	fingerprint := rehearseRestoreFingerprint(t, dest)

	out, err := runRestore(t, dest, "--apply", "--expect-fingerprint", fingerprint)
	require.NoError(t, err)

	// The backup was consumed and now sits at the live path.
	assert.NoFileExists(t, dest)
	assert.Equal(t, receipt.BackupSha256, sha256OfFile(t, live))

	// The replaced archive survives untouched as the aside copy, together
	// with the sidecars it had when it was replaced: the pair is a
	// complete snapshot of the replaced state.
	asides := countRestoreAsides(t, live)
	require.Len(t, asides, 1)
	assert.Equal(t, liveShaBefore, sha256OfFile(t, asides[0]))
	for _, sidecar := range liveSidecarsBefore {
		assert.FileExists(t, asides[0]+strings.TrimPrefix(sidecar, live))
	}

	// The new live is a healthy standalone database with the backup's rows.
	database := openBackupCopy(t, live)
	var sessions int
	require.NoError(t, database.QueryRow("SELECT COUNT(*) FROM sessions").
		Scan(&sessions))
	assert.Equal(t, 2, sessions)
	var quickCheck string
	require.NoError(t, database.QueryRow("PRAGMA quick_check").Scan(&quickCheck))
	assert.Equal(t, "ok", quickCheck)
	assert.NoFileExists(t, live+"-wal")
	assert.NoFileExists(t, live+"-shm")

	// The after-receipt anchors the undo.
	encoded, err := os.ReadFile(live + restoreReceiptSuffix)
	require.NoError(t, err)
	var applied restoreReceipt
	require.NoError(t, json.UnmarshalDecode(
		jsontext.NewDecoder(bytes.NewReader(encoded)), &applied))
	assert.Equal(t, liveShaBefore, applied.BeforeSha256)
	assert.Equal(t, receipt.BackupSha256, applied.AfterSha256)
	assert.Equal(t, receipt.BackupSha256, applied.BackupSha256)
	assert.Equal(t, asides[0], applied.AsidePath)
	assert.Equal(t, fingerprint, applied.Fingerprint)
	assert.Equal(t, live, applied.LivePath)
	assert.Equal(t, dest, applied.BackupPath)
	assert.Equal(t, db.CurrentDataVersion(), applied.DataVersion)
	assert.Equal(t, version, applied.ToolVersion)

	assert.Contains(t, out, "Restore applied.")
	assert.Contains(t, out, asides[0])
	assert.Contains(t, out, applied.BeforeSha256)
	assert.Contains(t, out, "restore --undo")
}

func TestRestoreApplyRefusesDriftSinceRehearsal(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	dest, _ := createRestoreBackup(t, dir)
	live := filepath.Join(dir, "sessions.db")
	fingerprint := rehearseRestoreFingerprint(t, dest)

	// The live archive changes after the rehearsal: the printed
	// fingerprint no longer matches, and the apply must refuse.
	appendByteToFile(t, live)
	liveShaDrifted := sha256OfFile(t, live)

	_, err := runRestore(t, dest, "--apply", "--expect-fingerprint", fingerprint)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "drifted")
	assert.Equal(t, liveShaDrifted, sha256OfFile(t, live),
		"the refused apply must not touch the live archive")
	assert.FileExists(t, dest)
	assert.Empty(t, countRestoreAsides(t, live))
	assert.NoFileExists(t, live+restoreReceiptSuffix)
}

func TestRestoreRefusesTamperedBackup(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	dest, _ := createRestoreBackup(t, dir)
	live := filepath.Join(dir, "sessions.db")
	liveShaBefore := sha256OfFile(t, live)

	// Flip a byte in the middle of the backup: the digest no longer
	// matches the receipt and the restore fails closed.
	backupData, err := os.ReadFile(dest)
	require.NoError(t, err)
	require.NotEmpty(t, backupData)
	backupData[len(backupData)/2] ^= 0xFF
	require.NoError(t, os.WriteFile(dest, backupData, 0o600))

	out, err := runRestore(t, dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match the digest recorded in")
	assert.Contains(t, err.Error(), "changed after the backup was verified")
	assert.NotContains(t, out, "Rehearsal only")
	assert.Equal(t, liveShaBefore, sha256OfFile(t, live))
	assert.Empty(t, countRestoreAsides(t, live))
	assert.NoFileExists(t, live+restoreReceiptSuffix)
}

func TestRestoreUndoSwapsBack(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	dest, backupReceipt := createRestoreBackup(t, dir)
	live := filepath.Join(dir, "sessions.db")
	addSessionToLive(t, dir)
	liveShaBefore := sha256OfFile(t, live)
	liveSidecarsBefore := existingSidecars(live)
	fingerprint := rehearseRestoreFingerprint(t, dest)
	_, err := runRestore(t, dest, "--apply", "--expect-fingerprint", fingerprint)
	require.NoError(t, err)

	encoded, err := os.ReadFile(live + restoreReceiptSuffix)
	require.NoError(t, err)
	var applied restoreReceipt
	require.NoError(t, json.UnmarshalDecode(
		jsontext.NewDecoder(bytes.NewReader(encoded)), &applied))

	out, err := runRestore(t, "--undo")
	require.NoError(t, err)

	// The aside copy is back at the live path with its original bytes and
	// its original sidecars.
	assert.Equal(t, liveShaBefore, sha256OfFile(t, live))
	assert.NoFileExists(t, applied.AsidePath)
	for _, sidecar := range liveSidecarsBefore {
		assert.FileExists(t, sidecar)
	}

	// The applied archive is kept as operator-managed evidence.
	undones := countRestoreUndones(t, live)
	require.Len(t, undones, 1)
	assert.Equal(t, backupReceipt.BackupSha256, sha256OfFile(t, undones[0]))

	database := openBackupCopy(t, live)
	var quickCheck string
	require.NoError(t, database.QueryRow("PRAGMA quick_check").Scan(&quickCheck))
	assert.Equal(t, "ok", quickCheck)

	// The receipt stays in place, so a second undo fails closed instead of
	// swapping on a stale verification chain.
	assert.FileExists(t, live+restoreReceiptSuffix)
	_, err = runRestore(t, "--undo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refuses to swap")

	assert.Contains(t, out, "Undo complete.")
	assert.Contains(t, out, undones[0])
	assert.Contains(t, out, liveShaBefore)
}

func TestRestoreUndoRefusesWhenLiveDrifted(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	dest, _ := createRestoreBackup(t, dir)
	live := filepath.Join(dir, "sessions.db")
	fingerprint := rehearseRestoreFingerprint(t, dest)
	_, err := runRestore(t, dest, "--apply", "--expect-fingerprint", fingerprint)
	require.NoError(t, err)

	// The applied archive changed after the restore: the receipt's
	// after_sha256 no longer matches and the undo refuses before swapping.
	appendByteToFile(t, live)
	liveShaDrifted := sha256OfFile(t, live)

	_, err = runRestore(t, "--undo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "after_sha256")
	assert.Equal(t, liveShaDrifted, sha256OfFile(t, live))
	asides := countRestoreAsides(t, live)
	require.Len(t, asides, 1, "the refused undo must keep the aside copy")
	assert.Empty(t, countRestoreUndones(t, live))
}

func TestRestoreRefusesSamePathAsLive(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	live := filepath.Join(dir, "sessions.db")

	for _, args := range [][]string{
		{live},
		{live, "--apply"},
	} {
		_, err := runRestore(t, args...)
		require.Error(t, err, "args %v must be refused", args)
		assert.Contains(t, err.Error(), "replace the archive with itself")
	}
	assert.FileExists(t, live)
	assert.Empty(t, countRestoreAsides(t, live))
	assert.NoFileExists(t, live+restoreReceiptSuffix)
}

// injectRestoreRenameFailure swaps the rename seam for one that fails for
// the matching step; every other rename goes through os.Rename so the
// rollback path itself still works.
func injectRestoreRenameFailure(
	t *testing.T, match func(from, to string) bool,
) {
	t.Helper()
	original := restoreRename
	restoreRename = func(from, to string) error {
		if match(from, to) {
			return errors.New("injected rename failure")
		}
		return original(from, to)
	}
	t.Cleanup(func() { restoreRename = original })
}

func TestRestoreApplyRollsBackOnRenameFailure(t *testing.T) {
	t.Run("aside rename fails before anything moved", func(t *testing.T) {
		dir := testDataDir(t)
		seedBackupArchive(t, dir, 1)
		dest, _ := createRestoreBackup(t, dir)
		archive := filepath.Join(dir, "sessions.db")
		liveShaBefore := sha256OfFile(t, archive)
		backupShaBefore := sha256OfFile(t, dest)

		injectRestoreRenameFailure(t, func(from, to string) bool {
			return from == archive && strings.HasPrefix(to, archive+restoreAsideInfix)
		})
		_, err := runRestore(t, dest, "--apply")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "injected rename failure")
		assert.Contains(t, err.Error(), "nothing was deleted")

		assert.Equal(t, liveShaBefore, sha256OfFile(t, archive))
		assert.Equal(t, backupShaBefore, sha256OfFile(t, dest))
		assert.Empty(t, countRestoreAsides(t, archive))
		assert.NoFileExists(t, archive+restoreReceiptSuffix)
	})

	t.Run("install rename fails after the aside rename", func(t *testing.T) {
		dir := testDataDir(t)
		seedBackupArchive(t, dir, 1)
		dest, _ := createRestoreBackup(t, dir)
		archive := filepath.Join(dir, "sessions.db")
		liveShaBefore := sha256OfFile(t, archive)
		backupShaBefore := sha256OfFile(t, dest)

		injectRestoreRenameFailure(t, func(from, to string) bool {
			return from == dest && to == archive
		})
		_, err := runRestore(t, dest, "--apply")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "injected rename failure")
		assert.Contains(t, err.Error(), "rolled back into place")

		// The rollback renamed the aside copy back onto the live path and
		// left the backup in place: nothing was deleted.
		assert.Equal(t, liveShaBefore, sha256OfFile(t, archive))
		assert.Equal(t, backupShaBefore, sha256OfFile(t, dest))
		assert.Empty(t, countRestoreAsides(t, archive))
		assert.NoFileExists(t, archive+restoreReceiptSuffix)
	})
}

func TestRestoreRefusesNewerBackupDataVersion(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	dest, _ := createRestoreBackup(t, dir)
	live := filepath.Join(dir, "sessions.db")
	reversionRestoreBackup(t, dest, db.CurrentDataVersion()+1)

	_, err := runRestore(t, dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "newer than this binary")
	assert.FileExists(t, live)
	assert.Empty(t, countRestoreAsides(t, live))
}

func TestRestoreAllowsOlderBackupWithResyncWarning(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	dest, _ := createRestoreBackup(t, dir)
	reversionRestoreBackup(t, dest, db.CurrentDataVersion()-1)

	out, err := runRestore(t, dest)
	require.NoError(t, err)
	assert.Contains(t, out, "older than this binary")
	assert.Contains(t, out, "non-destructive resync")
	assert.Contains(t, out, "Rehearsal only")
}

func TestRestoreRefusesBackupSidecarFiles(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	dest, _ := createRestoreBackup(t, dir)
	writeTestFile(t, dest+"-wal", []byte("stray sidecar"))

	_, err := runRestore(t, dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-wal sidecar")
	assert.Contains(t, err.Error(), "could lose committed transactions")
}

func TestRestoreRejectsInvalidArguments(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	dest, _ := createRestoreBackup(t, dir)

	tests := []struct {
		name    string
		args    []string
		message string
	}{
		{
			name:    "apply and undo together",
			args:    []string{dest, "--apply", "--undo"},
			message: "not both",
		},
		{
			name:    "expect-fingerprint without apply",
			args:    []string{dest, "--expect-fingerprint", strings.Repeat("a", 64)},
			message: "only valid together with --apply",
		},
		{
			name:    "malformed fingerprint",
			args:    []string{dest, "--apply", "--expect-fingerprint", "zz"},
			message: "64-character",
		},
		{
			name:    "missing backup path",
			args:    []string{},
			message: "requires the path",
		},
		{
			name:    "undo with a backup path",
			args:    []string{dest, "--undo"},
			message: "takes no backup path",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := runRestore(t, test.args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestRestoreUndoWithoutReceiptFailsClosed(t *testing.T) {
	dir := testDataDir(t)
	seedBackupArchive(t, dir, 1)
	live := filepath.Join(dir, "sessions.db")
	liveShaBefore := sha256OfFile(t, live)

	_, err := runRestore(t, "--undo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no restore receipt at")
	assert.Equal(t, liveShaBefore, sha256OfFile(t, live))
	assert.Empty(t, countRestoreUndones(t, live))
}
