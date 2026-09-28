package db

import (
	"database/sql"
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
