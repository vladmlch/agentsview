package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoRowLevelFTSMessageTriggers(t *testing.T) {
	t.Parallel()
	d := testDB(t)
	requireFTS(t, d)

	// Verify that row-level triggers on messages (which cause write amplification)
	// are not present.
	var count int
	err := d.getReader().QueryRow(t.Context(), `
		SELECT count(*) FROM sqlite_master
		WHERE type = 'trigger' AND name IN ('messages_ai', 'messages_au', 'messages_ad')
	`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "messages table should not have row-level FTS triggers")
}

func TestBatchFTSIndexing_Operations(t *testing.T) {
	t.Parallel()
	d := testDB(t)
	requireFTS(t, d)

	const (
		session1 = "session-fts-1"
		token1   = "uniqueftsalpha"
		session2 = "session-fts-2"
		token2   = "uniqueftsbeta"
	)

	// 1. Fresh write via WriteSessionBatch
	insertSession(t, d, session1, "proj")
	res, err := d.WriteSessionBatch([]SessionBatchWrite{{
		Session: Session{
			ID:               session1,
			Project:          "proj",
			MessageCount:     2,
			UserMessageCount: 1,
		},
		Messages: []Message{
			userMsg(session1, 0, "first message "+token1),
			asstMsg(session1, 1, "reply to alpha"),
		},
		DataVersion:     CurrentDataVersion(),
		ReplaceMessages: true,
	}})
	require.NoError(t, err)
	assert.Equal(t, 2, res.WrittenMessages)

	// Verify token1 is indexed
	var matchCount int
	err = d.getReader().QueryRow(t.Context(), `
		SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ?
	`, token1).Scan(&matchCount)
	require.NoError(t, err)
	assert.Equal(t, 1, matchCount, "token1 should be indexed by WriteSessionBatch")

	// 2. Incremental append via InsertMessages
	err = d.InsertMessages(t.Context(), []Message{
		userMsg(session1, 2, "second message with "+token2),
	})
	require.NoError(t, err)

	// Verify token2 is indexed and token1 is still there
	err = d.getReader().QueryRow(t.Context(), `
		SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ?
	`, token2).Scan(&matchCount)
	require.NoError(t, err)
	assert.Equal(t, 1, matchCount, "token2 should be indexed by InsertMessages")

	err = d.getReader().QueryRow(t.Context(), `
		SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ?
	`, token1).Scan(&matchCount)
	require.NoError(t, err)
	assert.Equal(t, 1, matchCount, "token1 should still be indexed")

	// 3. ReplaceSessionMessages replacing all messages of session1 with new token
	const token3 = "uniqueftsgamma"
	err = d.ReplaceSessionMessages(t.Context(), session1, []Message{
		userMsg(session1, 0, "replaced message "+token3),
	})
	require.NoError(t, err)

	// Old tokens should be gone (no leak)
	assertNoFTSLeak(t, d, token1)
	assertNoFTSLeak(t, d, token2)

	// token3 should be indexed
	err = d.getReader().QueryRow(t.Context(), `
		SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ?
	`, token3).Scan(&matchCount)
	require.NoError(t, err)
	assert.Equal(t, 1, matchCount, "token3 should be indexed by ReplaceSessionMessages")

	// 4. DeleteSession should clean up FTS entries
	err = d.DeleteSession(t.Context(), session1)
	require.NoError(t, err)
	assertNoFTSLeak(t, d, token3)

	var totalFTS int
	err = d.getReader().QueryRow(t.Context(), `SELECT count(*) FROM messages_fts`).Scan(&totalFTS)
	require.NoError(t, err)
	assert.Equal(t, 0, totalFTS, "messages_fts should be empty after deleting session")
}

func assertFTSMatchCount(t *testing.T, d *DB, token string, want int) {
	t.Helper()
	var count int
	err := d.getReader().QueryRow(t.Context(), `
		SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ?
	`, token).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, want, count, "unexpected messages_fts hits for %q", token)
}

func assertFTSRowCount(t *testing.T, d *DB, want int) {
	t.Helper()
	var count int
	err := d.getReader().QueryRow(t.Context(),
		`SELECT count(*) FROM messages_fts`,
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, want, count, "unexpected messages_fts row count")
}

// A ranged replacement deletes the session's tail rows and reinserts new
// ones that reuse the same rowids, so the deleted rows' FTS entries must
// be retired or the stale tokens glue onto the new content.
func TestBatchFTSIndexing_RangedReplace(t *testing.T) {
	t.Parallel()
	d := testDB(t)
	requireFTS(t, d)

	const (
		sessionID = "session-fts-ranged"
		keepToken = "rangedkeepalpha"
		oldToken  = "rangedoldbeta"
		newToken  = "rangednewgamma"
	)
	insertSession(t, d, sessionID, "proj")
	insertMessages(t, d,
		userMsg(sessionID, 0, "kept message "+keepToken),
		asstMsg(sessionID, 1, "old tail "+oldToken),
		asstMsg(sessionID, 2, "second old tail "+oldToken),
	)

	from := 1
	_, err := d.WriteSessionIncremental(t.Context(), sessionID, []Message{
		asstMsg(sessionID, 1, "replacement tail "+newToken),
	}, IncrementalSessionUpdate{
		NextOrdinal:        2,
		ReplaceFromOrdinal: &from,
	})
	require.NoError(t, err)

	// The deleted tail must leave no FTS entries behind.
	assertNoFTSLeak(t, d, oldToken)
	// Rows before fromOrdinal keep their index entries.
	assertFTSMatchCount(t, d, keepToken, 1)
	// The replacement row is indexed under its recycled rowid.
	assertFTSMatchCount(t, d, newToken, 1)
	// Exactly one index entry per surviving message.
	assertFTSRowCount(t, d, 2)
}

// Completing a truncated stored row updates its content in place, so the
// FTS entry must be reindexed or the appended tail stays unsearchable.
func TestBatchFTSIndexing_ExtendTruncatedText(t *testing.T) {
	t.Parallel()
	d := testDB(t)
	requireFTS(t, d)

	const (
		sessionID = "session-fts-extend"
		stemToken = "extendstemalpha"
		tailToken = "extendtailbeta"
	)
	stored := "the reply was " + stemToken
	full := stored + " and finished with " + tailToken

	_, err := d.WriteSessionBatchAtomic(t.Context(), []SessionBatchWrite{{
		Session:         extendTestSession(sessionID, 1),
		Messages:        []Message{extendTestMsg(sessionID, 0, stored)},
		ReplaceMessages: true,
	}})
	require.NoError(t, err)
	assertFTSMatchCount(t, d, stemToken, 1)
	assertFTSMatchCount(t, d, tailToken, 0)

	_, err = d.WriteSessionBatchAtomic(t.Context(), []SessionBatchWrite{{
		Session:            extendTestSession(sessionID, 1),
		Messages:           []Message{extendTestMsg(sessionID, 0, full)},
		CompleteStoredRows: true,
	}})
	require.NoError(t, err)

	// The completed tail is now indexed, with exactly one entry for the row.
	assertFTSMatchCount(t, d, tailToken, 1)
	assertFTSRowCount(t, d, 1)
}

// The orphan/trash copies bulk-insert messages with INSERT...SELECT and
// must index the copied rows themselves.
func TestBatchFTSIndexing_OrphanCopy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	const (
		sessionID = "orphan-fts"
		token     = "orphanftsalpha"
	)
	srcPath := filepath.Join(dir, "old.db")
	srcDB := testDBAtPath(t, srcPath, "src")
	requireFTS(t, srcDB)
	insertSession(t, srcDB, sessionID, "proj")
	insertMessages(t, srcDB, userMsg(sessionID, 0, "archived "+token))
	require.NoError(t, srcDB.Close())

	dstPath := filepath.Join(dir, "new.db")
	dstDB := testDBAtPath(t, dstPath, "dst")
	defer func() { require.NoError(t, dstDB.Close()) }()
	requireFTS(t, dstDB)

	count, err := dstDB.CopyOrphanedDataFrom(srcPath)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	assertFTSMatchCount(t, dstDB, token, 1)
	assertFTSRowCount(t, dstDB, 1)
}

func TestLegacyTriggersDroppedOnOpen(t *testing.T) {
	t.Parallel()
	d := testDB(t)
	requireFTS(t, d)

	// Artificially install legacy triggers
	require.NoError(t, d.Update(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `
			CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
				SELECT 1;
			END;
			CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
				SELECT 1;
			END;
			CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
				SELECT 1;
			END;
		`)
		return err
	}))

	// Close and re-open to trigger ensureFTS cleanup
	path := d.path
	require.NoError(t, d.Close())

	d2, err := Open(t.Context(), path)
	require.NoError(t, err)
	defer func() { _ = d2.Close() }()

	var count int
	err = d2.getReader().QueryRow(t.Context(), `
		SELECT count(*) FROM sqlite_master
		WHERE type = 'trigger' AND name IN ('messages_ai', 'messages_au', 'messages_ad')
	`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "legacy triggers should be dropped on open")
}
