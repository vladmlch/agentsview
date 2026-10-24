package parser

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
)

type sqliteReadOptions struct {
	// Only archive copies that cannot change may skip SQLite locking and WAL reads.
	stableSnapshot bool
	// Zero preserves the driver's busy timeout rather than overriding it.
	busyTimeoutMS int
}

// openSQLiteReadOnly shares source-database URI and read policies. Opening is
// lazy, as with sql.Open; callers retain ownership of queries and Close.
func openSQLiteReadOnly(path string, options sqliteReadOptions) (*sql.DB, error) {
	dsn := "file:" + sqliteURIPath(path) + "?mode=ro"
	if options.stableSnapshot {
		dsn += "&immutable=1"
	}
	if options.busyTimeoutMS != 0 {
		dsn += "&_busy_timeout=" + strconv.Itoa(options.busyTimeoutMS)
	}
	return sql.Open("sqlite3", dsn)
}

// WithSQLiteReadOnly opens the SQLite database at path for reading and executes fn.
// If opening or querying fails due to SQLite lock contention (SQLITE_BUSY or SQLITE_LOCKED),
// WithSQLiteReadOnly creates an isolated scratch copy of the database (including its WAL
// and journal files if present) in a temporary directory and retries fn against the scratch copy.
// The scratch copy is guaranteed to be cleaned up when fn completes.
func WithSQLiteReadOnly(
	ctx context.Context,
	path string,
	options sqliteReadOptions,
	fn func(db *sql.DB) error,
) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	db, err := openSQLiteReadOnly(path, options)
	if err == nil {
		fnErr := fn(db)
		_ = db.Close()
		if fnErr == nil {
			return nil
		}
		if ctx.Err() != nil || !isSQLiteBusyOrLocked(fnErr) {
			return fnErr
		}
		err = fnErr
	} else if ctx.Err() != nil || !isSQLiteBusyOrLocked(err) {
		return err
	}

	// err always holds the busy/locked failure that motivated the scratch
	// retry here — either the open error or the fn error. A fallback failure
	// joins it so the caller sees both the lock contention and why the
	// isolated retry could not run, instead of a bare nil success.
	scratchDir, tmpErr := os.MkdirTemp("", "agentsview-sqlite-scratch-*")
	if tmpErr != nil {
		return errors.Join(err, fmt.Errorf("creating sqlite scratch dir: %w", tmpErr))
	}
	defer os.RemoveAll(scratchDir)

	scratchDB := filepath.Join(scratchDir, filepath.Base(path))
	if copyErr := copySQLiteContainerFiles(path, scratchDB); copyErr != nil {
		return errors.Join(err, fmt.Errorf(
			"copying sqlite db %s to scratch: %w", path, copyErr))
	}

	scratchConn, openErr := openSQLiteReadOnly(scratchDB, sqliteReadOptions{
		stableSnapshot: false,
		busyTimeoutMS:  options.busyTimeoutMS,
	})
	if openErr != nil {
		return errors.Join(err, fmt.Errorf(
			"opening scratch copy of %s: %w", path, openErr))
	}
	defer scratchConn.Close()

	return fn(scratchConn)
}

// sqliteBusyRetryDelays is the stepped pause between retries of a read-only
// open that failed on SQLite lock contention. The 50+150+300ms ladder totals
// 500ms, at (not past) the watch layer's ~500ms debounce window: a transiently
// locked store can delay one scan batch up to the window, but never stall the
// next one. Contention that outlives the ladder is already absorbed by the
// caller's busy_timeout inside SQLite, and callers that need a guaranteed read
// use the WithSQLiteReadOnly scratch-copy lane instead.
var sqliteBusyRetryDelays = []time.Duration{
	50 * time.Millisecond,
	150 * time.Millisecond,
	300 * time.Millisecond,
}

// openSQLiteReadOnlyWithBusyRetry opens the SQLite database at path read-only
// and performs one initial read, retrying that open+initial-read seam while it
// fails with SQLITE_BUSY/SQLITE_LOCKED (the same classification as the
// WithSQLiteReadOnly scratch-copy lane). sql.Open never touches the file, so
// without the initial read a momentary SQLITE_BUSY from a live writer process
// — the Devin CLI holding its sessions.db — surfaces later as the caller's
// first-query parse failure, outside any retry. Non-busy errors fail
// immediately with unchanged behavior.
func openSQLiteReadOnlyWithBusyRetry(
	ctx context.Context, path string, options sqliteReadOptions,
) (*sql.DB, error) {
	return openSQLiteReadOnlyWithBusyRetryDelays(
		ctx, path, options, sqliteBusyRetryDelays, time.Sleep,
	)
}

func openSQLiteReadOnlyWithBusyRetryDelays(
	ctx context.Context,
	path string,
	options sqliteReadOptions,
	delays []time.Duration,
	sleep func(time.Duration),
) (*sql.DB, error) {
	var db *sql.DB
	err := retrySQLiteBusy(ctx, "opening "+filepath.Base(path), delays, sleep, func() error {
		opened, openErr := openSQLiteReadOnly(path, options)
		if openErr != nil {
			return openErr
		}
		// The initial read forces the lazy sql.Open connection to prepare its
		// first statement, so busy lock contention surfaces here, inside the
		// retry window, instead of on the caller's first query.
		var one int
		if pingErr := opened.QueryRowContext(ctx, "SELECT 1").Scan(&one); pingErr != nil {
			_ = opened.Close()
			return pingErr
		}
		db = opened
		return nil
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

// retrySQLiteBusy runs op until it succeeds, fails with a non-busy error, or
// exhausts delays, sleeping delay[i] before retry i+1. sleep is injected so
// tests can run the ladder without real pauses. A canceled context stops the
// ladder after the current attempt; an exhausted ladder reports the busy
// classification alongside the final underlying error.
func retrySQLiteBusy(
	ctx context.Context,
	label string,
	delays []time.Duration,
	sleep func(time.Duration),
	op func() error,
) error {
	var err error
	for attempt := 0; ; attempt++ {
		if err = op(); err == nil {
			return nil
		}
		if ctx.Err() != nil || !isSQLiteBusyOrLocked(err) {
			return err
		}
		if attempt >= len(delays) {
			return fmt.Errorf("sqlite busy %s after %d retries: %w",
				label, len(delays), err)
		}
		log.Printf("sqlite: busy %s (attempt %d of %d), retrying in %s: %v",
			label, attempt+1, len(delays)+1, delays[attempt], err)
		sleep(delays[attempt])
	}
}

func copySQLiteContainerFiles(src, dst string) error {
	if err := copySingleFile(src, dst); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-journal"} {
		srcSibling := src + suffix
		if _, err := os.Stat(srcSibling); err == nil {
			if err := copySingleFile(srcSibling, dst+suffix); err != nil {
				return err
			}
		}
	}
	return nil
}

func copySingleFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func isSQLiteBusyOrLocked(err error) bool {
	if err == nil {
		return false
	}
	// A typed driver error is decided by its primary result code alone:
	// SQLITE_BUSY extended codes (recovery, snapshot, timeout) all share
	// ErrBusy, while an ErrIoErr whose appended system errno reads
	// "resource busy" must not trigger a scratch-copy retry that cannot
	// help it.
	if sqliteErr, ok := errors.AsType[sqlite3.Error](err); ok {
		return sqliteErr.Code == sqlite3.ErrBusy ||
			sqliteErr.Code == sqlite3.ErrLocked
	}
	if sqliteErrPtr, ok := errors.AsType[*sqlite3.Error](err); ok && sqliteErrPtr != nil {
		return sqliteErrPtr.Code == sqlite3.ErrBusy ||
			sqliteErrPtr.Code == sqlite3.ErrLocked
	}
	// Untyped errors qualify only on message shapes SQLite itself produces
	// for lock contention, never on a bare "busy" substring.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked") ||
		strings.Contains(msg, "database is busy") ||
		strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "sqlite_locked")
}

// dedupeRestartedStream wraps a yield callback whose listing runs inside
// WithSQLiteReadOnly. A lock failure mid-iteration reruns the whole listing
// against the scratch copy from its first row, so already-emitted keys would
// arrive twice — and a row inserted between the two attempts could arrive
// below the last emitted key — while consumers like the changed-path
// stored-freshness merge require a strictly ascending stream. Because a
// single run emits keys in ascending order, every replayed or inserted-early
// key on the retry sorts at or below the last emitted key and is dropped; an
// inserted-early row belongs to a write the next listing pass picks up, so
// dropping it loses no coverage but keeps the consumer's cursor moving only
// forward. key must extract the stream's ordering column.
func dedupeRestartedStream[T any](
	yield func(T) error,
	key func(T) string,
) func(T) error {
	var last string
	var haveLast bool
	return func(item T) error {
		k := key(item)
		if haveLast && k <= last {
			return nil
		}
		last, haveLast = k, true
		return yield(item)
	}
}

// sqliteURIPath escapes a filesystem path for use in a SQLite file: URI.
// SQLite percent-decodes URI paths and stops parsing them at '?' or '#',
// and go-sqlite3 splits its driver parameters at the first '?', so these
// characters in a real path would open the wrong file or silently drop
// options such as mode=ro.
func sqliteURIPath(path string) string {
	return strings.NewReplacer(
		"%", "%25",
		"?", "%3F",
		"#", "%23",
	).Replace(path)
}
