// ABOUTME: `agentsview restore <backup>` — rehearse, apply, or undo a
// ABOUTME: verified replacement of the SQLite archive from a backup receipt.
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

	"github.com/spf13/cobra"
	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/db"
)

const (
	restoreReceiptSuffix    = ".restore-receipt.json"
	restoreReceiptTmpSuffix = ".restore-receipt.json.tmp"
	restoreAsideInfix       = ".replaced-"
	restoreUndoneInfix      = ".undone-"
	restoreTimestampLayout  = "20060102T150405Z"
)

// sqliteSidecarSuffixes are the sidecar files SQLite may keep next to a
// database file. A verified backup never has any: their presence means the
// file is a raw copy of a live database whose committed transactions may
// live in the sidecar, so restoring it would lose data.
var sqliteSidecarSuffixes = []string{"-wal", "-shm", "-journal"}

// restoreReceipt is the after-action record of an applied restore, written
// next to the live archive. It is the verification anchor for --undo: the
// undo only swaps when the live archive still hashes to after_sha256 (it is
// still exactly what the apply installed) and the aside copy still hashes to
// before_sha256 (the replaced archive was not touched). before_sha256 is the
// digest of the archive the apply renamed aside, after_sha256 the digest of
// the installed backup, and aside_path the undo anchor.
type restoreReceipt struct {
	// AppliedAt is when the swap finished, in UTC RFC 3339.
	AppliedAt string `json:"applied_at"`
	// LivePath is the archive path the backup was installed onto.
	LivePath string `json:"live_path"`
	// BackupPath is where the backup file was before the swap consumed it.
	BackupPath string `json:"backup_path"`
	// BackupSha256 is the digest verified against the backup receipt
	// immediately before the swap.
	BackupSha256 string `json:"backup_sha256"`
	// BeforeSha256 is the digest of the replaced live archive, which the
	// apply renamed aside and --undo restores.
	BeforeSha256 string `json:"before_sha256"`
	// AfterSha256 is the digest of the installed archive.
	AfterSha256 string `json:"after_sha256"`
	// AsidePath is the <live>.replaced-<timestamp> undo anchor.
	AsidePath string `json:"aside_path"`
	// Fingerprint is the plan fingerprint the apply re-verified
	// immediately before the swap.
	Fingerprint string `json:"fingerprint"`
	// DataVersion is the data version of the installed backup.
	DataVersion int `json:"data_version"`
	// ToolVersion is the agentsview version that performed the restore.
	ToolVersion string `json:"tool_version"`
}

// restorePlan is the verified rehearsal output: everything --apply needs,
// computed without touching a single file.
type restorePlan struct {
	LivePath          string        `json:"live_path"`
	BackupPath        string        `json:"backup_path"`
	ReceiptPath       string        `json:"receipt_path"`
	Receipt           backupReceipt `json:"receipt"`
	BackupSha256      string        `json:"backup_sha256"`
	BackupIntegrity   string        `json:"backup_integrity"`
	LiveSha256        string        `json:"live_sha256"`
	LiveSidecars      []string      `json:"live_sidecars,omitempty"`
	LiveReadable      bool          `json:"live_readable"`
	LiveDataVersion   int           `json:"live_data_version,omitempty"`
	LiveReadError     string        `json:"live_read_error,omitempty"`
	BinaryDataVersion int           `json:"binary_data_version"`
	AsideTarget       string        `json:"aside_target"`
	Fingerprint       string        `json:"fingerprint"`
	Warnings          []string      `json:"warnings,omitempty"`
}

func newRestoreCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <backup>",
		Short: "Rehearse, apply, or undo a verified restore of the SQLite archive",
		Long: "Rehearse or apply a verified restore of the SQLite archive from\n" +
			"<backup>, a snapshot written by 'agentsview backup', or undo the\n" +
			"most recent apply.\n\n" +
			"Without flags the command is a rehearsal: it verifies the backup and\n" +
			"prints the plan without touching anything. Verification reads the\n" +
			"sidecar receipt <backup>.receipt.json, hashes the backup file against\n" +
			"the recorded digest, runs PRAGMA quick_check on the backup over a\n" +
			"read-only connection, and compares data versions. A backup whose\n" +
			"bytes no longer match its receipt, whose recorded data version is\n" +
			"newer than this binary, or that carries -wal/-shm sidecars is\n" +
			"refused. A backup older than the current data version is allowed\n" +
			"with a warning: the engine marks such an archive stale and the next\n" +
			"sync performs a full non-destructive resync that reparses sources\n" +
			"and preserves archived sessions, so the restored archive self-heals.\n\n" +
			"The printed plan fingerprint binds the receipt bytes, the live\n" +
			"archive's stat identity, and the backup digest. Apply with\n" +
			"--expect-fingerprint <fingerprint>: apply re-verifies every check\n" +
			"immediately before the mutation and refuses when anything drifted\n" +
			"after the rehearsal.\n\n" +
			"With --apply the command takes the archive write-owner lock (a live\n" +
			"writable daemon must not own the archive), re-verifies the whole\n" +
			"plan, renames the live archive to <live>.replaced-<timestamp>\n" +
			"together with its sidecars, renames the backup onto the live path,\n" +
			"removes any stale sidecars left next to the restored file, verifies\n" +
			"the install with a hash and PRAGMA quick_check, and writes the\n" +
			"receipt <live>.restore-receipt.json. Any failure in the sequence\n" +
			"renames the aside copy back and fails loudly; nothing is deleted.\n\n" +
			"With --undo the command reads <live>.restore-receipt.json, verifies\n" +
			"the live archive still hashes to the receipt's after_sha256 and the\n" +
			"aside copy still hashes to before_sha256, then swaps them back: the\n" +
			"applied archive is renamed to <live>.undone-<timestamp> and the\n" +
			"aside copy returns to the live path, where PRAGMA quick_check and\n" +
			"before_sha256 are verified again. The receipt stays in place, so a\n" +
			"second --undo fails closed.\n\n" +
			"The archive itself is only ever renamed, never deleted: the\n" +
			".replaced-<timestamp> and .undone-<timestamp> files are\n" +
			"operator-managed evidence.\n",
		GroupID:      groupData,
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			apply, err := cmd.Flags().GetBool("apply")
			if err != nil {
				return err
			}
			undo, err := cmd.Flags().GetBool("undo")
			if err != nil {
				return err
			}
			expect, err := cmd.Flags().GetString("expect-fingerprint")
			if err != nil {
				return err
			}
			if apply && undo {
				return errors.New("use either --apply or --undo, not both")
			}
			if !apply && expect != "" {
				return errors.New(
					"--expect-fingerprint is only valid together with --apply")
			}
			if apply && expect != "" &&
				(len(expect) != sha256.Size*2 || !isHex(expect)) {
				return errors.New("--expect-fingerprint must be the " +
					"64-character hex fingerprint printed by the rehearsal")
			}
			cfg, err := config.LoadReadOnly()
			if err != nil {
				return err
			}
			jsonOutput := outputFormat(cmd) == "json"
			switch {
			case undo:
				if len(args) != 0 {
					return errors.New("restore --undo takes no backup path: " +
						"it reads " + cfg.DBPath + restoreReceiptSuffix)
				}
				return runRestoreUndo(
					cmd.Context(), cfg, jsonOutput, cmd.OutOrStdout())
			default:
				if len(args) != 1 {
					return errors.New(
						"restore requires the path of the backup to restore")
				}
				if apply {
					return runRestoreApply(cmd.Context(), cfg, args[0], expect,
						jsonOutput, cmd.OutOrStdout())
				}
				return runRestoreRehearsal(
					cmd.Context(), cfg, args[0], jsonOutput, cmd.OutOrStdout())
			}
		},
	}
	cmd.Flags().Bool("apply", false,
		"Apply the verified plan: rename the live archive aside and install the backup")
	cmd.Flags().Bool("undo", false,
		"Swap the most recent applied restore back using <live>.restore-receipt.json")
	cmd.Flags().String("expect-fingerprint", "",
		"Refuse --apply when the fingerprint drifted since the rehearsal that printed it")
	registerFormatFlags(cmd.Flags())
	return cmd
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

// runRestoreRehearsal verifies the restore and prints the plan without
// touching anything. Both the live archive and the backup are only ever
// opened read-only.
func runRestoreRehearsal(
	ctx context.Context, cfg config.Config, backupPath string,
	jsonOutput bool, out io.Writer,
) error {
	plan, err := verifyRestorePlan(ctx, cfg, backupPath)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeRestorePlanJSON(out, plan)
	}
	return writeRestorePlanHuman(out, plan)
}

// runRestoreApply applies the verified plan: the live archive is renamed
// aside (never deleted) and the backup takes its place. Every plan check is
// re-verified immediately pre-mutation under the write-owner lock, and the
// whole swap rolls back on any failure.
func runRestoreApply(
	ctx context.Context, cfg config.Config, backupPath, expectFingerprint string,
	jsonOutput bool, out io.Writer,
) (err error) {
	plan, err := verifyRestorePlan(ctx, cfg, backupPath)
	if err != nil {
		return err
	}
	if expectFingerprint != "" && expectFingerprint != plan.Fingerprint {
		return fmt.Errorf(
			"the archive or the backup drifted since the rehearsal that "+
				"printed fingerprint %s (current fingerprint %s); "+
				"re-run the rehearsal and re-apply",
			expectFingerprint, plan.Fingerprint)
	}
	// The swap replaces the archive path, so it goes through the same
	// writer-exclusion seam as every other direct archive write.
	if err := rejectLiveWritableDaemonBeforeDirectWrite(cfg); err != nil {
		return err
	}
	lock, err := acquireWriteOwnerLock(ctx, writeLockDataDir(cfg))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	// Settle an interrupted staged compaction before deciding what the
	// live archive file even is.
	if err := db.RecoverCompactManifest(cfg.DBPath); err != nil {
		return fmt.Errorf("recovering interrupted archive compaction: %w", err)
	}

	// The plan fingerprint is re-verified immediately pre-mutation, under
	// the write lock: every plan check runs again against the current
	// bytes, and the fingerprints must match.
	current, err := verifyRestorePlan(ctx, cfg, backupPath)
	if err != nil {
		return fmt.Errorf("re-verifying the restore plan before the swap: %w", err)
	}
	if current.Fingerprint != plan.Fingerprint {
		return fmt.Errorf(
			"the archive or the backup changed while the restore lock was "+
				"acquired (fingerprint %s -> %s); refusing to swap",
			plan.Fingerprint, current.Fingerprint)
	}

	aside := current.LivePath + restoreAsideInfix + restoreTimestamp()
	if err := refuseExistingRestoreTarget(aside); err != nil {
		return err
	}

	var swap restoreSwap
	fail := func(cause error) error {
		if rollErr := swap.rollback(); rollErr != nil {
			return errors.Join(cause, fmt.Errorf(
				"rolling back the restore of %s FAILED; the aside copy %s "+
					"holds the replaced archive and must be restored by hand",
				current.LivePath, aside))
		}
		return fmt.Errorf(
			"%w (the previous archive was rolled back into place at %s; "+
				"nothing was deleted)", cause, current.LivePath)
	}

	// Rename the live archive aside with its sidecars. The aside copy is
	// the undo anchor and is never deleted; renaming (not dropping) the
	// sidecars keeps the pair a complete snapshot of the replaced state,
	// including transactions a checkpoint had not yet written back.
	if err := swap.step(current.LivePath, aside); err != nil {
		return fail(err)
	}
	for _, sidecar := range current.LiveSidecars {
		if err := swap.step(
			sidecar, aside+strings.TrimPrefix(sidecar, current.LivePath)); err != nil {
			return fail(err)
		}
	}
	// Install the verified backup onto the live path.
	if err := swap.step(current.BackupPath, current.LivePath); err != nil {
		return fail(err)
	}
	// Remove sidecars left next to the restored file: after the renames
	// above any sidecar there belongs to the replaced file, and SQLite
	// must not replay it onto the restored snapshot.
	removedSidecars, err := removeStaleSidecars(current.LivePath)
	if err != nil {
		return fail(err)
	}

	installedSha, _, err := hashFileSHA256(current.LivePath)
	if err != nil {
		return fail(fmt.Errorf("hashing the installed archive: %w", err))
	}
	if installedSha != current.BackupSha256 {
		return fail(fmt.Errorf(
			"the installed archive hashes to %s, not the verified %s",
			installedSha, current.BackupSha256))
	}
	integrity, err := quickCheckInstalled(ctx, current.LivePath)
	if err != nil {
		return fail(err)
	}
	if integrity != "ok" {
		return fail(fmt.Errorf(
			"the installed archive failed verification: PRAGMA quick_check "+
				"reported %q", integrity))
	}

	receipt := restoreReceipt{
		AppliedAt:    time.Now().UTC().Format(time.RFC3339),
		LivePath:     current.LivePath,
		BackupPath:   current.BackupPath,
		BackupSha256: current.BackupSha256,
		BeforeSha256: current.LiveSha256,
		AfterSha256:  installedSha,
		AsidePath:    aside,
		Fingerprint:  current.Fingerprint,
		DataVersion:  current.Receipt.DataVersion,
		ToolVersion:  version,
	}
	if err := writeRestoreReceipt(current.LivePath, receipt); err != nil {
		// Without the receipt the undo anchor has no verification chain,
		// so the swap does not count as applied.
		return fail(err)
	}

	if jsonOutput {
		return writeRestoreReceiptJSON(out, receipt)
	}
	return writeRestoreAppliedHuman(out, current.LivePath, aside,
		current.LiveSha256, installedSha, current.Fingerprint, integrity,
		removedSidecars)
}

// runRestoreUndo swaps the most recent applied restore back. The
// verification chain fails closed unless the live archive is still exactly
// what the apply installed and the aside copy is still exactly the archive
// the apply replaced.
func runRestoreUndo(
	ctx context.Context, cfg config.Config, jsonOutput bool, out io.Writer,
) (err error) {
	live := filepath.Clean(cfg.DBPath)
	receiptPath := live + restoreReceiptSuffix
	receipt, err := readRestoreReceipt(receiptPath)
	if err != nil {
		return err
	}
	if err := rejectLiveWritableDaemonBeforeDirectWrite(cfg); err != nil {
		return err
	}
	lock, err := acquireWriteOwnerLock(ctx, writeLockDataDir(cfg))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if err := db.RecoverCompactManifest(cfg.DBPath); err != nil {
		return fmt.Errorf("recovering interrupted archive compaction: %w", err)
	}

	if _, err := checkRegularFile(live, "live archive"); err != nil {
		return err
	}
	liveSha, _, err := hashFileSHA256(live)
	if err != nil {
		return fmt.Errorf("hashing live archive: %w", err)
	}
	if liveSha != receipt.AfterSha256 {
		return fmt.Errorf(
			"live archive %s does not match the after_sha256 %s recorded in "+
				"%s; it was modified or replaced after the apply, and the "+
				"undo refuses to swap", live, receipt.AfterSha256, receiptPath)
	}
	if receipt.AfterSha256 != receipt.BackupSha256 {
		return fmt.Errorf(
			"restore receipt %s is inconsistent: after_sha256 %s does not "+
				"match the applied backup digest %s",
			receiptPath, receipt.AfterSha256, receipt.BackupSha256)
	}
	if _, err := checkRegularFile(receipt.AsidePath, "aside copy"); err != nil {
		return fmt.Errorf("the undo anchor is unusable: %w", err)
	}
	asideSha, _, err := hashFileSHA256(receipt.AsidePath)
	if err != nil {
		return fmt.Errorf("hashing aside copy: %w", err)
	}
	if asideSha != receipt.BeforeSha256 {
		return fmt.Errorf(
			"aside copy %s does not match the before_sha256 %s recorded in "+
				"%s; refusing to swap it back",
			receipt.AsidePath, receipt.BeforeSha256, receiptPath)
	}

	undone := live + restoreUndoneInfix + restoreTimestamp()
	if err := refuseExistingRestoreTarget(undone); err != nil {
		return err
	}

	var swap restoreSwap
	fail := func(cause error) error {
		if rollErr := swap.rollback(); rollErr != nil {
			return errors.Join(cause, fmt.Errorf(
				"rolling back the undo of %s FAILED; the applied archive %s "+
					"must be restored by hand", live, undone))
		}
		return fmt.Errorf(
			"%w (the applied archive was rolled back into place at %s; "+
				"nothing was deleted)", cause, live)
	}

	liveSidecars := existingSidecars(live)
	asideSidecars := existingSidecars(receipt.AsidePath)
	if err := swap.step(live, undone); err != nil {
		return fail(err)
	}
	for _, sidecar := range liveSidecars {
		if err := swap.step(
			sidecar, undone+strings.TrimPrefix(sidecar, live)); err != nil {
			return fail(err)
		}
	}
	if err := swap.step(receipt.AsidePath, live); err != nil {
		return fail(err)
	}
	for _, sidecar := range asideSidecars {
		if err := swap.step(
			sidecar, live+strings.TrimPrefix(sidecar, receipt.AsidePath)); err != nil {
			return fail(err)
		}
	}

	restoredSha, _, err := hashFileSHA256(live)
	if err != nil {
		return fail(fmt.Errorf("hashing the restored archive: %w", err))
	}
	if restoredSha != receipt.BeforeSha256 {
		return fail(fmt.Errorf(
			"the restored archive hashes to %s, not the receipt's "+
				"before_sha256 %s", restoredSha, receipt.BeforeSha256))
	}
	integrity, err := quickCheckInstalled(ctx, live)
	if err != nil {
		return fail(err)
	}
	if integrity != "ok" {
		return fail(fmt.Errorf(
			"the restored archive failed verification: PRAGMA quick_check "+
				"reported %q", integrity))
	}

	if jsonOutput {
		return writeRestoreReceiptJSON(out, receipt)
	}
	return writeRestoreUndoHuman(out, live, undone, restoredSha, integrity,
		receiptPath)
}

// verifyRestorePlan performs every read-only check of a restore against the
// current bytes on disk and returns the plan. It opens the live archive and
// the backup read-only only, and never renames or writes a file.
func verifyRestorePlan(
	ctx context.Context, cfg config.Config, backupPath string,
) (*restorePlan, error) {
	live := filepath.Clean(cfg.DBPath)
	backupPath = filepath.Clean(backupPath)
	plan := &restorePlan{
		LivePath:          live,
		BackupPath:        backupPath,
		ReceiptPath:       backupPath + backupReceiptSuffix,
		BinaryDataVersion: db.CurrentDataVersion(),
	}

	liveInfo, err := checkRegularFile(live, "live archive")
	if err != nil {
		return nil, err
	}
	backupInfo, err := checkRegularFile(backupPath, "backup")
	if err != nil {
		return nil, err
	}
	// Same-path guard: the backup must not be the live file itself, not
	// even through a link, or the swap would rename the archive aside and
	// install it onto an empty path.
	if backupPath == live {
		return nil, fmt.Errorf(
			"backup %s is the live archive itself; restoring it would "+
				"replace the archive with itself", backupPath)
	}
	if os.SameFile(liveInfo, backupInfo) {
		return nil, fmt.Errorf(
			"backup %s resolves to the live archive itself; restoring it "+
				"would replace the archive with itself", backupPath)
	}
	for _, suffix := range sqliteSidecarSuffixes {
		if _, err := os.Lstat(backupPath + suffix); err == nil {
			return nil, fmt.Errorf(
				"backup %s has a %s sidecar; it is not the standalone "+
					"verified snapshot the receipt describes, and restoring "+
					"it could lose committed transactions",
				backupPath, suffix)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf(
				"inspecting backup sidecar %s: %w", backupPath+suffix, err)
		}
	}

	// (a) The receipt must exist and parse.
	receiptBytes, err := os.ReadFile(plan.ReceiptPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf(
				"no backup receipt at %s; a backup without its receipt "+
					"cannot be verified for restore", plan.ReceiptPath)
		}
		return nil, fmt.Errorf("reading backup receipt: %w", err)
	}
	var receipt backupReceipt
	if err := json.UnmarshalDecode(
		jsontext.NewDecoder(bytes.NewReader(receiptBytes)), &receipt); err != nil {
		return nil, fmt.Errorf(
			"parsing backup receipt %s: %w", plan.ReceiptPath, err)
	}
	if len(receipt.BackupSha256) != sha256.Size*2 {
		return nil, fmt.Errorf(
			"backup receipt %s does not record a usable sha256 digest",
			plan.ReceiptPath)
	}
	plan.Receipt = receipt

	// (b) The backup file must hash to the digest recorded in the receipt:
	// any other content is third-party modification and fails closed.
	plan.BackupSha256, _, err = hashFileSHA256(backupPath)
	if err != nil {
		return nil, fmt.Errorf("hashing backup: %w", err)
	}
	if plan.BackupSha256 != receipt.BackupSha256 {
		return nil, fmt.Errorf(
			"backup %s does not match the digest recorded in %s; the file "+
				"changed after the backup was verified and cannot be restored",
			backupPath, plan.ReceiptPath)
	}

	// (c) PRAGMA quick_check on the backup, read-only, and (d) data-version
	// compatibility. The file's own user_version must also match the
	// receipt, so a swapped-in file cannot ride on a receipt it never
	// described.
	integrity, backupFileVersion, err := inspectBackupCopy(ctx, backupPath)
	if err != nil {
		return nil, fmt.Errorf("verifying backup: %w", err)
	}
	plan.BackupIntegrity = integrity
	if integrity != "ok" {
		return nil, fmt.Errorf(
			"backup failed verification: PRAGMA quick_check reported %q; "+
				"refusing to restore it", integrity)
	}
	if backupFileVersion != receipt.DataVersion {
		return nil, fmt.Errorf(
			"backup data version %d does not match the %d recorded in its "+
				"receipt; the file is not the snapshot the receipt verified",
			backupFileVersion, receipt.DataVersion)
	}

	// The live archive's own data version is best-effort: a live archive
	// that no longer opens as SQLite is exactly the case a restore fixes.
	liveVersion, liveErr := readArchiveUserVersion(ctx, live)
	plan.LiveDataVersion = liveVersion
	if liveErr != nil {
		plan.LiveReadError = liveErr.Error()
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"the live archive could not be read as SQLite (%v); the restore "+
				"replaces it and the old file is kept as the aside copy",
			liveErr))
	} else {
		plan.LiveReadable = true
	}

	switch {
	case receipt.DataVersion > plan.BinaryDataVersion:
		return nil, fmt.Errorf(
			"backup data version %d is newer than this binary's %d; the "+
				"installed archive cannot be opened by this build, so "+
				"upgrade agentsview first",
			receipt.DataVersion, plan.BinaryDataVersion)
	case receipt.DataVersion < plan.BinaryDataVersion:
		// An older data version only marks the archive stale: Open keeps
		// the old user_version, logs "database upgrade requires full
		// resync", and the resync reparses sources without destroying
		// archived sessions. Allow the restore and say so in the plan.
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"backup data version %d is older than this binary's %d; "+
				"restoring is allowed because the next sync performs a full "+
				"non-destructive resync (sources are reparsed, archived "+
				"sessions are preserved), so the restored archive self-heals",
			receipt.DataVersion, plan.BinaryDataVersion))
	}

	plan.LiveSha256, _, err = hashFileSHA256(live)
	if err != nil {
		return nil, fmt.Errorf("hashing live archive: %w", err)
	}
	plan.LiveSidecars = existingSidecars(live)
	// The printed aside target is informational; --apply stamps its own
	// timestamp at mutation time.
	plan.AsideTarget = live + restoreAsideInfix + restoreTimestamp()
	plan.Fingerprint, err = restorePlanFingerprint(
		live, receiptBytes, liveInfo, plan.BackupSha256)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

// restorePlanFingerprint binds the receipt bytes, the live archive's stat
// identity, and the verified backup digest into one printed value. The
// rehearsal prints it; --apply recomputes it immediately pre-mutation and
// refuses on drift, so neither the archive nor the backup can change between
// the plan and the swap without failing the restore.
func restorePlanFingerprint(
	live string, receiptBytes []byte, liveInfo os.FileInfo, backupSha string,
) (string, error) {
	hasher := sha256.New()
	fmt.Fprintf(hasher, "receipt-sha256 %x\n", sha256.Sum256(receiptBytes))
	fmt.Fprintf(hasher, "live-path %s\n", live)
	fmt.Fprintf(hasher, "live-size %d\n", liveInfo.Size())
	fmt.Fprintf(hasher, "live-mtime-ns %d\n", liveInfo.ModTime().UnixNano())
	fmt.Fprintf(hasher, "live-mode %s\n", liveInfo.Mode().String())
	fmt.Fprintf(hasher, "backup-sha256 %s\n", backupSha)
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// restoreRename is the seam tests use to inject rename failures into the
// middle of a restore swap; production always renames with os.Rename.
var restoreRename = os.Rename

// restoreRenameStep is one completed rename of a swap, remembered so the
// rollback can replay it in reverse.
type restoreRenameStep struct {
	from, to string
}

// restoreSwap performs the ordered renames of a restore mutation. On the
// first failure the completed renames are replayed in reverse, so the swap
// either fully lands or fully unwinds and the archive is only ever renamed,
// never deleted.
type restoreSwap struct {
	done []restoreRenameStep
}

func (s *restoreSwap) step(from, to string) error {
	if err := restoreRename(from, to); err != nil {
		return err
	}
	s.done = append(s.done, restoreRenameStep{from: from, to: to})
	return nil
}

func (s *restoreSwap) rollback() error {
	var errs []error
	for i := len(s.done) - 1; i >= 0; i-- {
		if err := restoreRename(s.done[i].to, s.done[i].from); err != nil {
			errs = append(errs, fmt.Errorf(
				"rollback rename %s -> %s: %w", s.done[i].to, s.done[i].from, err))
		}
	}
	return errors.Join(errs...)
}

// checkRegularFile fails when path is missing or not a regular file.
func checkRegularFile(path, what string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no %s at %s", what, path)
		}
		return nil, fmt.Errorf("inspecting %s %s: %w", what, path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s %s is not a regular file", what, path)
	}
	return info, nil
}

// readArchiveUserVersion reads the live archive's PRAGMA user_version over a
// read-only connection.
func readArchiveUserVersion(ctx context.Context, path string) (int, error) {
	database, err := sql.Open("sqlite3", doctorReadOnlyDSN(path))
	if err != nil {
		return 0, fmt.Errorf("opening archive for data version: %w", err)
	}
	defer database.Close()
	var dataVersion int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").
		Scan(&dataVersion); err != nil {
		return 0, fmt.Errorf("reading archive data version: %w", err)
	}
	return dataVersion, nil
}

// quickCheckInstalled verifies the archive at path with PRAGMA quick_check
// over a read-only connection.
func quickCheckInstalled(ctx context.Context, path string) (string, error) {
	database, err := sql.Open("sqlite3", doctorReadOnlyDSN(path))
	if err != nil {
		return "", fmt.Errorf(
			"opening the installed archive for verification: %w", err)
	}
	defer database.Close()
	integrity, err := quickCheckResult(ctx, database)
	if err != nil {
		return "", fmt.Errorf("verifying the installed archive: %w", err)
	}
	return integrity, nil
}

// existingSidecars lists the sidecar files that exist next to path.
func existingSidecars(path string) []string {
	var sidecars []string
	for _, suffix := range sqliteSidecarSuffixes {
		if _, err := os.Lstat(path + suffix); err == nil {
			sidecars = append(sidecars, path+suffix)
		}
	}
	return sidecars
}

func sidecarPaths(path string) []string {
	paths := make([]string, 0, len(sqliteSidecarSuffixes))
	for _, suffix := range sqliteSidecarSuffixes {
		paths = append(paths, path+suffix)
	}
	return paths
}

// refuseExistingRestoreTarget refuses to overwrite an aside or undone
// target: those files are operator-managed evidence and are never replaced.
func refuseExistingRestoreTarget(target string) error {
	for _, candidate := range append([]string{target}, sidecarPaths(target)...) {
		if _, err := os.Lstat(candidate); err == nil {
			return fmt.Errorf(
				"restore target %s already exists; refusing to overwrite "+
					"operator-managed evidence", candidate)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspecting restore target %s: %w", candidate, err)
		}
	}
	return nil
}

// removeStaleSidecars deletes sidecars left next to the restored archive.
// After the swap renames, any sidecar there belongs to the replaced file;
// replaying it onto the restored snapshot could corrupt it. The archive
// itself is only ever renamed, never deleted; these derived sidecar files
// follow the same handling as compaction cleanup.
func removeStaleSidecars(live string) ([]string, error) {
	var removed []string
	for _, sidecar := range existingSidecars(live) {
		if err := os.Remove(sidecar); err != nil {
			return removed, fmt.Errorf(
				"removing stale sidecar %s: %w", sidecar, err)
		}
		removed = append(removed, sidecar)
	}
	return removed, nil
}

func restoreTimestamp() string {
	return time.Now().UTC().Format(restoreTimestampLayout)
}

// readRestoreReceipt reads and validates the after-receipt of an applied
// restore. A missing or unusable receipt leaves no undo, which fails closed.
func readRestoreReceipt(path string) (restoreReceipt, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return restoreReceipt{}, fmt.Errorf(
				"no restore receipt at %s; there is no applied restore to undo",
				path)
		}
		return restoreReceipt{}, fmt.Errorf("reading restore receipt: %w", err)
	}
	var receipt restoreReceipt
	if err := json.UnmarshalDecode(
		jsontext.NewDecoder(bytes.NewReader(encoded)), &receipt); err != nil {
		return restoreReceipt{}, fmt.Errorf(
			"parsing restore receipt %s: %w", path, err)
	}
	if receipt.AsidePath == "" ||
		len(receipt.BeforeSha256) != sha256.Size*2 ||
		len(receipt.AfterSha256) != sha256.Size*2 {
		return restoreReceipt{}, fmt.Errorf(
			"restore receipt %s is missing the aside path or the digests; "+
				"it cannot be used for an undo", path)
	}
	return receipt, nil
}

// writeRestoreReceipt records the receipt atomically next to the live
// archive: the .tmp file is written first and renamed, so a reader never
// sees a partial receipt.
func writeRestoreReceipt(live string, receipt restoreReceipt) error {
	encoded, err := marshalRestoreReceipt(receipt)
	if err != nil {
		return fmt.Errorf("encoding restore receipt: %w", err)
	}
	receiptTmp := live + restoreReceiptTmpSuffix
	if err := os.WriteFile(receiptTmp, encoded, 0o600); err != nil {
		return fmt.Errorf("writing restore receipt: %w", err)
	}
	if err := os.Rename(receiptTmp, live+restoreReceiptSuffix); err != nil {
		_ = os.Remove(receiptTmp)
		return fmt.Errorf("installing restore receipt: %w", err)
	}
	return nil
}

func marshalRestoreReceipt(receipt restoreReceipt) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeRestoreReceiptJSON(&buf, receipt); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeRestoreReceiptJSON(w io.Writer, receipt restoreReceipt) error {
	return json.MarshalEncode(
		jsontext.NewEncoder(w, jsontext.WithIndent("  ")), receipt)
}

func writeRestorePlanJSON(w io.Writer, plan *restorePlan) error {
	return json.MarshalEncode(
		jsontext.NewEncoder(w, jsontext.WithIndent("  ")), plan)
}

func writeRestorePlanHuman(w io.Writer, plan *restorePlan) error {
	fmt.Fprintln(w, "Restore rehearsal — nothing has been renamed or written.")
	fmt.Fprintf(w, "  Live archive:        %s\n", plan.LivePath)
	fmt.Fprintf(w, "  Backup:              %s\n", plan.BackupPath)
	fmt.Fprintf(w, "  Backup receipt:      %s\n", plan.ReceiptPath)
	fmt.Fprintf(w, "  Backup sha256:       %s (matches the receipt)\n",
		plan.BackupSha256)
	fmt.Fprintf(w, "  Backup integrity:    %s (PRAGMA quick_check)\n",
		plan.BackupIntegrity)
	fmt.Fprintf(w, "  Backup data version: %d (this binary: %d)\n",
		plan.Receipt.DataVersion, plan.BinaryDataVersion)
	if plan.LiveReadable {
		fmt.Fprintf(w, "  Live data version:   %d\n", plan.LiveDataVersion)
	} else {
		fmt.Fprintf(w, "  Live data version:   unreadable (%s)\n",
			plan.LiveReadError)
	}
	fmt.Fprintf(w, "  Backup taken from:   %s (source sha256 %s, taken %s)\n",
		plan.Receipt.SourceDB, plan.Receipt.SourceSha256, plan.Receipt.CreatedAt)
	fmt.Fprintf(w, "  Aside target:        %s (timestamp stamped at apply)\n",
		plan.AsideTarget)
	fmt.Fprintf(w, "  Plan fingerprint:    %s\n", plan.Fingerprint)
	for _, warning := range plan.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warning)
	}
	fmt.Fprintln(w, "Rehearsal only: no files were changed. Re-run with --apply "+
		"to install the backup and pass --expect-fingerprint <fingerprint> so "+
		"apply refuses when anything drifted after this rehearsal. Apply "+
		"refuses while a writable daemon owns the archive: stop the daemon "+
		"first, and any write to the live archive after this rehearsal "+
		"changes the fingerprint.")
	fmt.Fprintf(w, "With --apply: the current archive is renamed to "+
		"<live>.replaced-<timestamp> together with its -wal/-shm sidecars "+
		"(kept as the undo anchor, never deleted) and the backup takes its "+
		"place; a receipt at %s records both digests. A later `agentsview "+
		"restore --undo` verifies the live archive against that receipt and "+
		"swaps the aside copy back, renaming the applied archive to "+
		"<live>.undone-<timestamp>. The .replaced-<timestamp> and "+
		".undone-<timestamp> files are operator-managed evidence: agentsview "+
		"never deletes them.\n", plan.LivePath+restoreReceiptSuffix)
	return nil
}

func writeRestoreAppliedHuman(
	w io.Writer, live, aside, beforeSha, afterSha, fingerprint,
	integrity string, removedSidecars []string,
) error {
	fmt.Fprintln(w, "Restore applied.")
	fmt.Fprintf(w, "  Live archive:  %s (the verified backup is now the "+
		"archive; PRAGMA quick_check: %s)\n", live, integrity)
	fmt.Fprintf(w, "  Before sha256: %s\n", beforeSha)
	fmt.Fprintf(w, "  After sha256:  %s\n", afterSha)
	fmt.Fprintf(w, "  Fingerprint:   %s\n", fingerprint)
	fmt.Fprintf(w, "  Aside copy:    %s (the undo anchor; kept, never deleted)\n",
		aside)
	for _, sidecar := range removedSidecars {
		fmt.Fprintf(w, "  Removed stale sidecar: %s (belonged to the replaced "+
			"file)\n", sidecar)
	}
	fmt.Fprintf(w, "  Receipt:       %s\n", live+restoreReceiptSuffix)
	fmt.Fprintln(w, "  Undo:          run `agentsview restore --undo`; it verifies "+
		"the live archive against the receipt and swaps the aside copy back. "+
		"The .replaced-<timestamp> and .undone-<timestamp> files are "+
		"operator-managed evidence: agentsview never deletes them.")
	return nil
}

func writeRestoreUndoHuman(
	w io.Writer, live, undone, restoredSha, integrity, receiptPath string,
) error {
	fmt.Fprintln(w, "Undo complete.")
	fmt.Fprintf(w, "  Live archive:     %s (the aside copy is back; "+
		"PRAGMA quick_check: %s)\n", live, integrity)
	fmt.Fprintf(w, "  Restored sha256:  %s (matches the receipt's before_sha256)\n",
		restoredSha)
	fmt.Fprintf(w, "  Applied archive:  %s (kept as evidence; never deleted)\n",
		undone)
	fmt.Fprintf(w, "  Receipt:          %s stays in place; it no longer "+
		"matches the live archive, so a second --undo fails closed\n",
		receiptPath)
	return nil
}
