package parser

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

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

	db, err := openOpenCodeDB(dbPath)
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
