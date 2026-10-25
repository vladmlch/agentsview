// ABOUTME: `agentsview backup <dest>` — snapshot the SQLite archive through
// ABOUTME: the online-backup API and write a verified sidecar receipt.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/remotesync"
)

const (
	backupTmpSuffix        = ".tmp"
	backupReceiptSuffix    = ".receipt.json"
	backupReceiptTmpSuffix = ".receipt.json.tmp"
	backupLockSuffix       = ".lock"
)

// backupReceipt is the sidecar record written next to a backup. It is the
// verification anchor for putting the backup back onto the archive: a
// restore can hash the backup file and refuse it when the digest or the
// recorded verification does not match.
type backupReceipt struct {
	// CreatedAt is when the backup finished, in UTC RFC 3339.
	CreatedAt string `json:"created_at"`
	// SourceDB is the archive path the backup was taken from.
	SourceDB string `json:"source_db"`
	// SourceSha256 is the digest of the snapshot content as written. It is
	// taken over the online-backup copy, not the live file bytes: hashing
	// the live archive would race with writers and could never be
	// reproduced.
	SourceSha256 string `json:"source_sha256"`
	// BackupSha256 is the digest recomputed from the installed destination
	// file after the rename. It must equal SourceSha256; the command fails
	// otherwise.
	BackupSha256 string `json:"backup_sha256"`
	// Integrity is the PRAGMA quick_check result on the backup copy:
	// "ok", or the reported problems joined for a failed check.
	Integrity string `json:"integrity"`
	// DataVersion is the SQLite user_version of the backup — the archive
	// data version a restoring binary must be compatible with.
	DataVersion int `json:"data_version"`
	// ToolVersion is the agentsview version that produced the backup.
	ToolVersion string `json:"tool_version"`
}

func newBackupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup <destination>",
		Short: "Write a verified snapshot copy of the SQLite archive",
		Long: "Write a verified snapshot copy of the SQLite archive to\n" +
			"<destination>.\n\n" +
			"The snapshot is taken with the SQLite online-backup API over a\n" +
			"read-only connection to the live archive: it copies committed\n" +
			"content to one consistent point in time without stopping the\n" +
			"daemon and without writing to the archive, so the archive's\n" +
			"-wal and -shm sidecars are not needed and are not copied. The\n" +
			"copy works even when the application schema layer refuses the\n" +
			"archive, because the command reads the database file directly\n" +
			"instead of opening it through the archive schema.\n\n" +
			"The copy is written to <destination>.tmp, verified with\n" +
			"PRAGMA quick_check, hashed, and only then renamed into place, so\n" +
			"the destination never holds a partial backup. A sidecar receipt\n" +
			"<destination>.receipt.json records the SHA-256 of the backup\n" +
			"content and the verification result, so a backup can be verified\n" +
			"before it is ever restored onto the archive.\n\n" +
			"The command refuses to overwrite an existing destination and\n" +
			"holds an exclusive lock on <destination>.lock while writing. The\n" +
			"lock file is left in place after the run so a racing process\n" +
			"cannot lock a replaced inode.",
		GroupID:      groupData,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadReadOnly()
			if err != nil {
				return err
			}
			return runBackup(
				cmd.Context(), cfg, args[0],
				outputFormat(cmd) == "json", cmd.OutOrStdout(),
			)
		},
	}
	registerFormatFlags(cmd.Flags())
	return cmd
}

// runBackup snapshots the archive into dest and writes the receipt. The
// live archive is only ever opened read-only.
func runBackup(
	ctx context.Context, cfg config.Config, dest string,
	jsonOutput bool, out io.Writer,
) (err error) {
	dest = filepath.Clean(dest)
	if dest == "." || dest == string(filepath.Separator) {
		return errors.New("backup destination must be a file path")
	}
	source := cfg.DBPath
	if err := checkBackupSource(source); err != nil {
		return err
	}
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("destination %s already exists; refusing to overwrite", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspecting backup destination: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("creating backup destination directory: %w", err)
	}
	lock, err := acquireBackupLock(dest)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()

	// Re-check under the lock: another backup could have created the
	// destination between the first check and the lock acquisition.
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("destination %s already exists; refusing to overwrite", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspecting backup destination: %w", err)
	}

	tmp := dest + backupTmpSuffix
	digest, integrity, dataVersion, err := writeVerifiedSnapshot(ctx, tmp, source)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("installing backup %s: %w", dest, err)
	}
	digest, err = verifyInstalledBackup(dest, digest)
	if err != nil {
		_ = os.Remove(dest)
		return err
	}
	receipt := backupReceipt{
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		SourceDB:     source,
		SourceSha256: digest,
		BackupSha256: digest,
		Integrity:    integrity,
		DataVersion:  dataVersion,
		ToolVersion:  version,
	}
	if err := writeBackupReceipt(dest, receipt); err != nil {
		_ = os.Remove(dest)
		return err
	}
	if jsonOutput {
		return writeBackupReceiptJSON(out, receipt)
	}
	return writeBackupHuman(out, dest, receipt)
}

// checkBackupSource fails before any destination work when the archive
// path is missing or not a regular file.
func checkBackupSource(source string) error {
	info, err := os.Stat(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no archive to back up at %s", source)
		}
		return fmt.Errorf("inspecting archive %s: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("archive %s is not a regular file", source)
	}
	return nil
}

// acquireBackupLock takes the exclusive per-destination flock. The lock
// file is intentionally kept after unlocking: removing it would let a
// racing process lock the replaced inode while another holder still
// believes it owns the original one.
func acquireBackupLock(dest string) (*flock.Flock, error) {
	lock := flock.New(dest + backupLockSuffix)
	locked, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("acquiring backup lock %s: %w",
			dest+backupLockSuffix, err)
	}
	if !locked {
		return nil, fmt.Errorf("another backup of %s appears to be in "+
			"progress: %s is held", dest, dest+backupLockSuffix)
	}
	return lock, nil
}

// writeVerifiedSnapshot copies the archive into tmp and verifies the copy:
// the digest is taken over the snapshot bytes, and the copy must pass
// PRAGMA quick_check. The tmp file is the caller's to remove on failure.
func writeVerifiedSnapshot(
	ctx context.Context, tmp, source string,
) (digest, integrity string, dataVersion int, err error) {
	if err := remotesync.WriteSQLiteSnapshot(tmp, source); err != nil {
		return "", "", 0, fmt.Errorf("snapshotting archive: %w", err)
	}
	digest, _, err = hashFileSHA256(tmp)
	if err != nil {
		return "", "", 0, fmt.Errorf("hashing backup snapshot: %w", err)
	}
	integrity, dataVersion, err = inspectBackupCopy(ctx, tmp)
	if err != nil {
		return "", "", 0, err
	}
	if integrity != "ok" {
		return "", "", 0, fmt.Errorf(
			"backup copy failed verification: PRAGMA quick_check reported %q; "+
				"the archive may be corrupt and no backup was kept", integrity)
	}
	return digest, integrity, dataVersion, nil
}

// inspectBackupCopy opens the snapshot copy read-only and reports its
// PRAGMA quick_check result and user_version.
func inspectBackupCopy(
	ctx context.Context, path string,
) (integrity string, dataVersion int, err error) {
	database, err := sql.Open("sqlite3", doctorReadOnlyDSN(path))
	if err != nil {
		return "", 0, fmt.Errorf("opening backup copy for verification: %w", err)
	}
	defer func() { err = errors.Join(err, database.Close()) }()

	integrity, err = quickCheckResult(ctx, database)
	if err != nil {
		return "", 0, fmt.Errorf("verifying backup copy: %w", err)
	}
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").
		Scan(&dataVersion); err != nil {
		return "", 0, fmt.Errorf("reading backup copy data version: %w", err)
	}
	return integrity, dataVersion, nil
}

// quickCheckResult joins every PRAGMA quick_check row. A healthy database
// reports the single row "ok"; problems arrive one row each.
func quickCheckResult(ctx context.Context, database *sql.DB) (string, error) {
	rows, err := database.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var findings []string
	for rows.Next() {
		var finding string
		if err := rows.Scan(&finding); err != nil {
			return "", err
		}
		findings = append(findings, finding)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(findings) == 0 {
		return "", errors.New("PRAGMA quick_check returned no rows")
	}
	return strings.Join(findings, "; "), nil
}

// verifyInstalledBackup re-hashes the destination after the rename and
// returns its digest; a mismatch means the installed file is not the
// verified snapshot.
func verifyInstalledBackup(dest, snapshotDigest string) (string, error) {
	digest, _, err := hashFileSHA256(dest)
	if err != nil {
		return "", fmt.Errorf("hashing installed backup: %w", err)
	}
	if digest != snapshotDigest {
		return "", fmt.Errorf(
			"installed backup %s does not match the verified snapshot", dest)
	}
	return digest, nil
}

// hashFileSHA256 streams the file through SHA-256 and returns the hex
// digest and byte size.
func hashFileSHA256(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

// writeBackupReceipt records the receipt atomically next to the backup:
// the .tmp file is written first and renamed, so a reader never sees a
// partial receipt.
func writeBackupReceipt(dest string, receipt backupReceipt) error {
	encoded, err := marshalBackupReceipt(receipt)
	if err != nil {
		return fmt.Errorf("encoding backup receipt: %w", err)
	}
	receiptTmp := dest + backupReceiptTmpSuffix
	if err := os.WriteFile(receiptTmp, encoded, 0o600); err != nil {
		return fmt.Errorf("writing backup receipt: %w", err)
	}
	if err := os.Rename(receiptTmp, dest+backupReceiptSuffix); err != nil {
		_ = os.Remove(receiptTmp)
		return fmt.Errorf("installing backup receipt: %w", err)
	}
	return nil
}

// marshalBackupReceipt renders the receipt as indented JSON.
func marshalBackupReceipt(receipt backupReceipt) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeBackupReceiptJSON(&buf, receipt); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeBackupReceiptJSON(w io.Writer, receipt backupReceipt) error {
	return json.MarshalEncode(
		jsontext.NewEncoder(w, jsontext.WithIndent("  ")), receipt)
}

func writeBackupHuman(
	w io.Writer, dest string, receipt backupReceipt,
) error {
	size, err := os.Stat(dest)
	if err != nil {
		return fmt.Errorf("stating backup %s: %w", dest, err)
	}
	fmt.Fprintln(w, "Backup complete.")
	fmt.Fprintf(w, "  Source:       %s\n", receipt.SourceDB)
	fmt.Fprintf(w, "  Destination:  %s\n", dest)
	fmt.Fprintf(w, "  Size:         %s\n", formatBytes(size.Size()))
	fmt.Fprintf(w, "  SHA-256:      %s\n", receipt.BackupSha256)
	fmt.Fprintf(w, "  Integrity:    %s (PRAGMA quick_check)\n", receipt.Integrity)
	fmt.Fprintf(w, "  Data version: %d\n", receipt.DataVersion)
	fmt.Fprintf(w, "  Tool version: %s\n", receipt.ToolVersion)
	fmt.Fprintf(w, "  Receipt:      %s\n", dest+backupReceiptSuffix)
	return nil
}
