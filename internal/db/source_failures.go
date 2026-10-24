package db

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// SourceFailure is the identity a source file had when its parse last failed.
// Missing records a source that did not exist; MTimeNS is zero then.
type SourceFailure struct {
	MTimeNS     int64
	Size        int64
	Fingerprint string
	Missing     bool
}

// sourceFailureExtraSeparator joins a source failure's fingerprint with its
// bounded-retry ledger extension inside the fingerprint column. The unit
// separator control byte never appears in a fingerprint hash, so rows
// written before the ledger existed decode as plain fingerprints.
const sourceFailureExtraSeparator = "\x1f"

// SourceFailureExtra is the quarantine ledger state persisted beside a
// source failure identity in the source_failures fingerprint column: the
// failure class, how many parse attempts the recorded identity has spent,
// and the archive data version the attempts were counted at. It rides the
// existing column so the quarantine survives a restart without a schema
// change.
type SourceFailureExtra struct {
	Class       string
	Attempts    int
	DataVersion int
}

// EncodeSourceFailureFingerprint appends extra to hash as the extension
// segment stored in the source_failures fingerprint column. An empty extra
// stores hash unchanged, so rows without quarantine state stay byte-identical
// to the pre-ledger format.
func EncodeSourceFailureFingerprint(hash string, extra SourceFailureExtra) string {
	if extra == (SourceFailureExtra{}) {
		return hash
	}
	return hash + sourceFailureExtraSeparator + extra.Class +
		sourceFailureExtraSeparator + strconv.Itoa(extra.Attempts) +
		sourceFailureExtraSeparator + strconv.Itoa(extra.DataVersion)
}

// DecodeSourceFailureFingerprint splits a stored fingerprint column value
// into the identity hash and its quarantine ledger extension. A value
// without the extension segment decodes to a zero extra.
func DecodeSourceFailureFingerprint(stored string) (string, SourceFailureExtra) {
	hash, suffix, ok := strings.Cut(stored, sourceFailureExtraSeparator)
	if !ok {
		return stored, SourceFailureExtra{}
	}
	parts := strings.SplitN(suffix, sourceFailureExtraSeparator, 3)
	if len(parts) != 3 {
		return stored, SourceFailureExtra{}
	}
	attempts, attemptsErr := strconv.Atoi(parts[1])
	version, versionErr := strconv.Atoi(parts[2])
	if attemptsErr != nil || versionErr != nil {
		return stored, SourceFailureExtra{}
	}
	return hash, SourceFailureExtra{
		Class:       parts[0],
		Attempts:    attempts,
		DataVersion: version,
	}
}

// LoadSourceFailureDetails returns every persisted source failure keyed by
// cache key, together with each row's quarantine ledger extension.
func (db *DB) LoadSourceFailureDetails(
	ctx context.Context,
) (map[string]SourceFailure, map[string]SourceFailureExtra, error) {
	rows, err := db.getReader().Query(ctx,
		"SELECT cache_key, file_mtime, file_size, fingerprint, missing FROM source_failures",
	)
	if err != nil {
		return nil, nil, fmt.Errorf("loading source failures: %w", err)
	}
	defer rows.Close()

	result := make(map[string]SourceFailure)
	extras := make(map[string]SourceFailureExtra)
	for rows.Next() {
		var key string
		var failure SourceFailure
		var stored string
		if err := rows.Scan(
			&key, &failure.MTimeNS, &failure.Size, &stored, &failure.Missing,
		); err != nil {
			return nil, nil, fmt.Errorf("scanning source failure: %w", err)
		}
		hash, extra := DecodeSourceFailureFingerprint(stored)
		failure.Fingerprint = hash
		if extra != (SourceFailureExtra{}) {
			extras[key] = extra
		}
		result[key] = failure
	}
	return result, extras, rows.Err()
}

// LoadSourceFailures returns all persisted source failures keyed by cache key.
func (db *DB) LoadSourceFailures(
	ctx context.Context,
) (map[string]SourceFailure, error) {
	loaded, _, err := db.LoadSourceFailureDetails(ctx)
	return loaded, err
}

// ReplaceSourceFailures replaces all persisted source failures in a single
// transaction.
func (db *DB) ReplaceSourceFailures(
	ctx context.Context,
	entries map[string]SourceFailure,
) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	tx, err := db.getWriter().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "DELETE FROM source_failures"); err != nil {
		return fmt.Errorf("clearing source failures: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx,
		"INSERT INTO source_failures"+
			" (cache_key, file_mtime, file_size, fingerprint, missing) VALUES (?, ?, ?, ?, ?)",
	)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for key, failure := range entries {
		if _, err := stmt.ExecContext(
			ctx, key, failure.MTimeNS, failure.Size, failure.Fingerprint, failure.Missing,
		); err != nil {
			return fmt.Errorf("inserting source failure %s: %w", key, err)
		}
	}

	return tx.Commit()
}
