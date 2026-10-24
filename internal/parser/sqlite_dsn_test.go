package parser

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLiteURIPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "/data/opencode.db", want: "/data/opencode.db"},
		{name: "hash", in: "/data/pro#ject/x.db", want: "/data/pro%23ject/x.db"},
		{name: "question mark", in: "/data/a?b/x.db", want: "/data/a%3Fb/x.db"},
		{name: "percent", in: "/data/100%/x.db", want: "/data/100%25/x.db"},
		{
			name: "percent sequence stays literal",
			in:   "/data/a%3Fb/x.db",
			want: "/data/a%253Fb/x.db",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sqliteURIPath(tc.in))
		})
	}
}

func TestOpenSQLiteWithSpecialCharPath(t *testing.T) {
	// '#' would end the URI path (dropping mode=ro into the fragment) and
	// '%41' would percent-decode to 'A' if the path were not escaped.
	dir := filepath.Join(t.TempDir(), "pro#ject %41")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	dbPath := filepath.Join(dir, "opencode.db")

	writer, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	_, err = writer.ExecContext(t.Context(), "CREATE TABLE t (x INTEGER); INSERT INTO t VALUES (1)")
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	db, err := openOpenCodeDB(t.Context(), dbPath)
	require.NoError(t, err)
	defer db.Close()

	var n int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM t").Scan(&n))
	assert.Equal(t, 1, n)

	_, err = db.ExecContext(t.Context(), "INSERT INTO t VALUES (2)")
	require.Error(t, err, "mode=ro must survive special characters in the path")
}

func TestOpenSQLiteReadOnlyStableSnapshot(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "snapshot.db")
	writer, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	_, err = writer.ExecContext(t.Context(), `PRAGMA journal_mode=WAL;
		CREATE TABLE messages (content TEXT);
		INSERT INTO messages VALUES ('snapshot content')`)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	reader, err := openSQLiteReadOnly(dbPath, sqliteReadOptions{
		stableSnapshot: true,
		busyTimeoutMS:  3000,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	var content string
	require.NoError(t, reader.QueryRowContext(t.Context(), "SELECT content FROM messages").Scan(&content))
	assert.Equal(t, "snapshot content", content)
	// Immutable archive copies must not create live WAL coordination files.
	assert.NoFileExists(t, dbPath+"-wal")
	assert.NoFileExists(t, dbPath+"-shm")
	_, err = reader.ExecContext(t.Context(), "DELETE FROM messages")
	require.Error(t, err)
}

func TestWithSQLiteReadOnly_FallbackOnBusy(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "locked.db")

	writer, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	defer writer.Close()
	writer.SetMaxOpenConns(1)

	_, err = writer.ExecContext(t.Context(), `
		PRAGMA journal_mode=WAL;
		CREATE TABLE items (id TEXT PRIMARY KEY, value TEXT);
		INSERT INTO items VALUES ('item1', 'committed_value');
	`)
	require.NoError(t, err)

	// Acquire an exclusive lock on the database to simulate an active writer holding lock.
	_, err = writer.ExecContext(t.Context(), "PRAGMA locking_mode=EXCLUSIVE")
	require.NoError(t, err)

	tx, err := writer.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(t.Context(), "INSERT INTO items VALUES ('item2', 'uncommitted_value')")
	require.NoError(t, err)

	// A normal connection with a low busy timeout should fail with SQLITE_BUSY / database is locked.
	normalConn, err := openSQLiteReadOnly(dbPath, sqliteReadOptions{busyTimeoutMS: 50})
	require.NoError(t, err)
	defer normalConn.Close()

	var dummy string
	err = normalConn.QueryRowContext(t.Context(), "SELECT value FROM items WHERE id = 'item1'").Scan(&dummy)
	require.Error(t, err, "normal read must fail when writer holds exclusive transaction")
	assert.Contains(t, err.Error(), "locked")

	// WithSQLiteReadOnly must catch the busy/locked error, make an isolated scratch copy,
	// and read the committed data successfully without failing.
	var readVal string
	err = WithSQLiteReadOnly(t.Context(), dbPath, sqliteReadOptions{busyTimeoutMS: 50}, func(db *sql.DB) error {
		return db.QueryRowContext(t.Context(), "SELECT value FROM items WHERE id = 'item1'").Scan(&readVal)
	})
	require.NoError(t, err, "WithSQLiteReadOnly must succeed via scratch copy fallback")
	assert.Equal(t, "committed_value", readVal)
}

func TestWithSQLiteReadOnly_ScratchCopyFailurePropagates(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "locked.db")

	writer, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	defer writer.Close()
	writer.SetMaxOpenConns(1)

	_, err = writer.ExecContext(t.Context(), `
		PRAGMA journal_mode=WAL;
		CREATE TABLE items (id TEXT PRIMARY KEY, value TEXT);
		INSERT INTO items VALUES ('item1', 'committed_value');
	`)
	require.NoError(t, err)

	_, err = writer.ExecContext(t.Context(), "PRAGMA locking_mode=EXCLUSIVE")
	require.NoError(t, err)

	tx, err := writer.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(t.Context(), "INSERT INTO items VALUES ('item2', 'uncommitted_value')")
	require.NoError(t, err)

	// The scratch fallback copies the database and its -wal/-journal siblings.
	// A directory where the journal sibling should be cannot be copied as a
	// file, so the copy fails while the live query is still failing busy. The
	// call must surface a real error — a false success would read as an empty
	// container to callers like cursorIDEBatchMemberPresent.
	require.NoError(t, os.Mkdir(dbPath+"-journal", 0o755))

	err = WithSQLiteReadOnly(t.Context(), dbPath, sqliteReadOptions{busyTimeoutMS: 50}, func(db *sql.DB) error {
		var v string
		return db.QueryRowContext(t.Context(), "SELECT value FROM items WHERE id = 'item1'").Scan(&v)
	})
	require.Error(t, err, "a failed scratch fallback must surface an error, not a false success")
	assert.ErrorContains(t, err, "locked", "the original busy error must survive in the joined error")
	assert.ErrorContains(t, err, "copying sqlite db")
}

func TestWithSQLiteReadOnly_ScratchCopyFailureJoinsFnError(t *testing.T) {
	// Without a real lock the busy signal is synthetic; the point is that the
	// caller-visible error still carries it when the copy fails.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "plain.db")
	require.NoError(t, os.WriteFile(dbPath, []byte("stub"), 0o600))
	require.NoError(t, os.Mkdir(dbPath+"-journal", 0o755))

	errFakeBusy := errors.New("database is locked")
	err := WithSQLiteReadOnly(t.Context(), dbPath, sqliteReadOptions{}, func(db *sql.DB) error {
		return errFakeBusy
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, errFakeBusy)
	assert.ErrorContains(t, err, "copying sqlite db")
}

func TestIsSQLiteBusyOrLocked(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{
			name: "typed busy",
			err:  sqlite3.Error{Code: sqlite3.ErrBusy},
			want: true,
		},
		{
			name: "typed locked",
			err:  sqlite3.Error{Code: sqlite3.ErrLocked},
			want: true,
		},
		{
			name: "typed extended busy code",
			err:  sqlite3.Error{Code: sqlite3.ErrBusy, ExtendedCode: sqlite3.ErrBusySnapshot},
			want: true,
		},
		{
			name: "typed non-busy code",
			err:  sqlite3.Error{Code: sqlite3.ErrCantOpen},
			want: false,
		},
		{
			name: "typed io error with busy wording",
			err: fmt.Errorf("read: %w: resource busy",
				sqlite3.Error{Code: sqlite3.ErrIoErr}),
			want: false,
		},
		{
			name: "os-level resource busy",
			err: &os.PathError{
				Op: "open", Path: "/tmp/x",
				Err: errors.New("resource busy"),
			},
			want: false,
		},
		{name: "bare busy word", err: errors.New("device or resource busy"), want: false},
		{name: "database is locked", err: errors.New("database is locked"), want: true},
		{
			name: "database table is locked",
			err:  errors.New("database table is locked"),
			want: true,
		},
		{name: "database is busy", err: errors.New("database is busy"), want: true},
		{name: "SQLITE_BUSY literal", err: errors.New("SQLITE_BUSY"), want: true},
		{name: "SQLITE_LOCKED literal", err: errors.New("SQLITE_LOCKED"), want: true},
		{
			name: "wrapped locked message",
			err:  fmt.Errorf("query failed: %w", errors.New("database is locked")),
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isSQLiteBusyOrLocked(tc.err))
		})
	}
}

func TestDedupeRestartedStream(t *testing.T) {
	var got []string
	yield := dedupeRestartedStream(
		func(s string) error {
			got = append(got, s)
			return nil
		},
		func(s string) string { return s },
	)
	for _, k := range []string{"a", "b", "c"} {
		require.NoError(t, yield(k))
	}
	// A mid-iteration restart replays the listing from its first row; "aa"
	// stands for a row inserted between the two attempts — it sorts below the
	// last emitted key and must not move the stream backwards.
	for _, k := range []string{"a", "aa", "b", "d"} {
		require.NoError(t, yield(k))
	}
	assert.Equal(t, []string{"a", "b", "c", "d"}, got)
}

func TestWithSQLiteReadOnly_PropagatesNonBusyErrors(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	writer, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	_, err = writer.ExecContext(t.Context(), `
		CREATE TABLE items (id TEXT PRIMARY KEY);
		INSERT INTO items VALUES ('item1');
	`)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	// Syntax error or missing table should be returned directly.
	err = WithSQLiteReadOnly(t.Context(), dbPath, sqliteReadOptions{busyTimeoutMS: 100}, func(db *sql.DB) error {
		var x string
		return db.QueryRowContext(t.Context(), "SELECT nonexistent_column FROM items").Scan(&x)
	})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "locked")
}

func TestRetrySQLiteBusy(t *testing.T) {
	errBusy := sqlite3.Error{Code: sqlite3.ErrBusy}
	errBusySnapshot := sqlite3.Error{
		Code:         sqlite3.ErrBusy,
		ExtendedCode: sqlite3.ErrBusySnapshot,
	}
	errOther := errors.New("file is not a database")

	tests := []struct {
		name        string
		ctx         func() context.Context
		results     []error
		wantCalls   int
		wantSleeps  []time.Duration
		wantErr     error
		wantErrText string
	}{
		{
			name:       "succeeds on first attempt",
			results:    []error{nil},
			wantCalls:  1,
			wantSleeps: nil,
		},
		{
			name:       "retries busy then succeeds",
			results:    []error{errBusy, errBusy, nil},
			wantCalls:  3,
			wantSleeps: []time.Duration{50 * time.Millisecond, 150 * time.Millisecond},
		},
		{
			name:       "retries extended busy snapshot codes",
			results:    []error{fmt.Errorf("read: %w", errBusySnapshot), nil},
			wantCalls:  2,
			wantSleeps: []time.Duration{50 * time.Millisecond},
		},
		{
			name:        "exhausted ladder reports busy",
			results:     []error{errBusy, errBusy, errBusy, errBusy},
			wantCalls:   4,
			wantSleeps:  []time.Duration{50 * time.Millisecond, 150 * time.Millisecond, 300 * time.Millisecond},
			wantErr:     errBusy,
			wantErrText: "sqlite busy",
		},
		{
			name:       "non-busy error fails without retry",
			results:    []error{errOther},
			wantCalls:  1,
			wantSleeps: nil,
			wantErr:    errOther,
		},
		{
			name:       "non-busy error after a busy retry stops the ladder",
			results:    []error{errBusy, errOther},
			wantCalls:  2,
			wantSleeps: []time.Duration{50 * time.Millisecond},
			wantErr:    errOther,
		},
		{
			name: "canceled context stops the ladder",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			results:    []error{errBusy, nil},
			wantCalls:  1,
			wantSleeps: nil,
			wantErr:    errBusy,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.ctx != nil {
				ctx = tc.ctx()
			}
			calls := 0
			var sleeps []time.Duration
			err := retrySQLiteBusy(ctx, "testing", sqliteBusyRetryDelays,
				func(d time.Duration) { sleeps = append(sleeps, d) },
				func() error {
					i := calls
					calls++
					if i >= len(tc.results) {
						return errBusy
					}
					return tc.results[i]
				})
			assert.Equal(t, tc.wantCalls, calls, "op call count")
			assert.Equal(t, tc.wantSleeps, sleeps, "slept delays")
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			if tc.wantErrText != "" {
				assert.ErrorContains(t, err, tc.wantErrText)
			}
		})
	}
}

func TestOpenSQLiteReadOnlyWithBusyRetry_BusyThenSuccess(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "locked.db")

	writer, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	writer.SetMaxOpenConns(1)

	_, err = writer.ExecContext(t.Context(), `
		PRAGMA journal_mode=WAL;
		CREATE TABLE items (id TEXT PRIMARY KEY, value TEXT);
		INSERT INTO items VALUES ('item1', 'committed_value');
	`)
	require.NoError(t, err)

	// Acquire an exclusive lock to simulate the live writer process holding
	// sessions.db while agentsview reads it.
	_, err = writer.ExecContext(t.Context(), "PRAGMA locking_mode=EXCLUSIVE")
	require.NoError(t, err)
	tx, err := writer.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(t.Context(), "INSERT INTO items VALUES ('item2', 'uncommitted_value')")
	require.NoError(t, err)

	// The initial read of the retrying open must hit the same busy
	// classification the caller's first query would.
	probe, err := openSQLiteReadOnly(dbPath, sqliteReadOptions{busyTimeoutMS: 1})
	require.NoError(t, err)
	var one int
	err = probe.QueryRowContext(t.Context(), "SELECT 1").Scan(&one)
	require.Error(t, err, "the SELECT 1 initial read must fail while the writer holds the exclusive lock")
	assert.True(t, isSQLiteBusyOrLocked(err), "the failed initial read must classify as busy/locked, got: %v", err)
	require.NoError(t, probe.Close())

	// The first inter-attempt pause releases the writer, so attempt 2 must
	// succeed; the ladder values are recorded, never slept.
	var sleeps []time.Duration
	released := false
	db, err := openSQLiteReadOnlyWithBusyRetryDelays(
		t.Context(), dbPath, sqliteReadOptions{busyTimeoutMS: 1},
		sqliteBusyRetryDelays,
		func(d time.Duration) {
			sleeps = append(sleeps, d)
			if !released {
				released = true
				require.NoError(t, tx.Rollback())
				require.NoError(t, writer.Close())
			}
		},
	)
	require.NoError(t, err, "the retrying open must succeed once the writer releases the lock")
	defer db.Close()
	assert.Equal(t, []time.Duration{sqliteBusyRetryDelays[0]}, sleeps,
		"exactly one busy retry must precede the successful attempt")

	var value string
	require.NoError(t, db.QueryRowContext(t.Context(),
		"SELECT value FROM items WHERE id = 'item1'").Scan(&value))
	assert.Equal(t, "committed_value", value)
}

func TestOpenSQLiteReadOnlyWithBusyRetry_ExhaustedLadderReportsBusy(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "locked.db")

	writer, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	defer writer.Close()
	writer.SetMaxOpenConns(1)

	_, err = writer.ExecContext(t.Context(), `
		PRAGMA journal_mode=WAL;
		CREATE TABLE items (id TEXT PRIMARY KEY);
	`)
	require.NoError(t, err)

	_, err = writer.ExecContext(t.Context(), "PRAGMA locking_mode=EXCLUSIVE")
	require.NoError(t, err)
	tx, err := writer.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(t.Context(), "INSERT INTO items VALUES ('item1')")
	require.NoError(t, err)

	// Tiny delays keep the exhaustion path fast while still proving the
	// ladder runs to its end and reports the busy classification.
	db, err := openSQLiteReadOnlyWithBusyRetryDelays(
		t.Context(), dbPath, sqliteReadOptions{busyTimeoutMS: 1},
		[]time.Duration{time.Millisecond, time.Millisecond, time.Millisecond},
		time.Sleep,
	)
	require.Nil(t, db)
	require.Error(t, err, "a lock that outlives the ladder must surface as an error")
	assert.ErrorContains(t, err, "sqlite busy")
	assert.ErrorContains(t, err, "locked")
	assert.True(t, isSQLiteBusyOrLocked(err),
		"the exhausted error must keep the busy classification, got: %v", err)
}

func TestOpenSQLiteReadOnlyWithBusyRetry_NonBusyFailsWithoutRetry(t *testing.T) {
	// A file that is not a database fails the initial read with a non-busy
	// error, which must return immediately instead of burning the ladder.
	dbPath := filepath.Join(t.TempDir(), "not-a.db")
	require.NoError(t, os.WriteFile(dbPath, []byte("stub"), 0o600))

	var sleeps []time.Duration
	db, err := openSQLiteReadOnlyWithBusyRetryDelays(
		t.Context(), dbPath, sqliteReadOptions{busyTimeoutMS: 1},
		sqliteBusyRetryDelays,
		func(d time.Duration) { sleeps = append(sleeps, d) },
	)
	require.Nil(t, db)
	require.Error(t, err)
	assert.Empty(t, sleeps, "non-busy failures must not sleep")
	assert.False(t, isSQLiteBusyOrLocked(err))
	assert.ErrorContains(t, err, "not a database")
}
