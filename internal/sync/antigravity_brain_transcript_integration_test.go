package sync_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/dbtest"
	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/sync"
)

// TestAntigravitySameStatRewriteReparses pins the av-3 fix for the IDE lane:
// the freshness gate's stored size/mtime composite cannot distinguish a
// same-size, same-mtime rewrite of the source, so the per-component stat
// digest (ctime+inode per file) must break the skip and the required
// fingerprint content hash must force a reparse. Before the antigravity
// MultiFileStatHasher and FingerprintHashRequiredForFreshness semantics, such
// a rewrite stayed skipped forever and the archive went stale.
func TestAntigravitySameStatRewriteReparses(t *testing.T) {
	root := t.TempDir()
	const id = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	const sessionID = "antigravity:" + id
	path := filepath.Join(root, "brain", id, ".system_generated", "logs", "transcript.jsonl")
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)

	// The rename-swap keeps size and mtime identical and only moves the
	// identity fields a size/mtime composite cannot observe.
	swapTranscript := func(content string) {
		payload := []byte(
			`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"` +
				content + `"}` + "\n")
		tmp := path + ".swap"
		dbtest.WriteTestFile(t, tmp, payload)
		require.NoError(t, os.Rename(tmp, path))
		require.NoError(t, os.Chtimes(path, mtime, mtime))
	}
	swapTranscript("same stat message one")

	archive := dbtest.OpenTestDB(t)
	engine := sync.NewEngine(t.Context(), archive, sync.EngineConfig{
		AgentDirs: map[parser.AgentType][]string{parser.AgentAntigravity: {root}},
		Machine:   "host-a",
	})
	t.Cleanup(engine.Close)

	ctx := t.Context()
	first := engine.SyncAll(ctx, nil)
	require.Equal(t, 1, first.Synced, "first sync parses and stores the session")
	before, err := archive.GetMessages(ctx, sessionID, 0, 10, true)
	require.NoError(t, err)
	require.Len(t, before, 1)
	require.Equal(t, "same stat message one", before[0].Content)

	swapTranscript("same stat message twu")
	second := engine.SyncAll(ctx, nil)
	require.Equal(t, 1, second.Synced,
		"a same-size, same-mtime rewrite must not stay skipped")
	after, err := archive.GetMessages(ctx, sessionID, 0, 10, true)
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, "same stat message twu", after[0].Content,
		"the rewrite's content must reach the archive")

	// The reparse re-stamped the digest, so an unchanged source skips again.
	third := engine.SyncAll(ctx, nil)
	assert.Equal(t, 0, third.Synced,
		"an unchanged source must skip once the digest converged")
	assert.GreaterOrEqual(t, third.Skipped, 1)
}

func TestAntigravityBrainReadFailurePreservesArchive(t *testing.T) {
	for _, withDatabase := range []bool{false, true} {
		name := "standalone"
		if withDatabase {
			name = "database companion"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			const id = dbtest.AntigravityTitleID
			path := dbtest.WriteAntigravityTitleTranscript(t, root, "keep this archived message")
			if withDatabase {
				dbPath := filepath.Join(root, "conversations", id+".db")
				require.NoError(t, os.MkdirAll(filepath.Dir(dbPath), 0o755))
				copyAntigravityCLITestSchemaTemplate(t, dbPath)
			}
			provider, ok := parser.NewProvider(parser.AgentAntigravity, parser.ProviderConfig{Roots: []string{root}})
			require.True(t, ok)
			sources, err := provider.Discover(t.Context())
			require.NoError(t, err)
			require.Len(t, sources, 1)
			parsed, err := provider.Parse(t.Context(), parser.ParseRequest{Source: sources[0]})
			require.NoError(t, err)
			require.Len(t, parsed.Results, 1)
			sessionID := parsed.Results[0].Result.Session.ID
			archive := dbtest.OpenTestDB(t)
			engine := sync.NewEngine(t.Context(), archive, sync.EngineConfig{
				AgentDirs: map[parser.AgentType][]string{parser.AgentAntigravity: {root}}, Machine: "host-a",
			})
			require.NoError(t, engine.SyncPathsContext(t.Context(), []string{path}))
			before, err := archive.GetMessages(t.Context(), sessionID, 0, 10, true)
			require.NoError(t, err)
			require.Len(t, before, 1)
			require.Equal(t, "keep this archived message", before[0].Content)

			require.NoError(t, os.Truncate(path, (64<<20)+1))
			_, err = provider.Parse(t.Context(), parser.ParseRequest{Source: sources[0]})
			require.Error(t, err, "an oversized transcript must fail the parse")
			_ = engine.SyncPathsContext(t.Context(), []string{path})
			after, err := archive.GetMessages(t.Context(), sessionID, 0, 10, true)
			require.NoError(t, err)
			require.Len(t, after, 1, "failed reads must retain the archived message")
			assert.Equal(t, "keep this archived message", after[0].Content)
		})
	}
}

func TestAntigravityBrainDatabaseTransitionsKeepOneSession(t *testing.T) {
	root := t.TempDir()
	const id = dbtest.AntigravityTitleID
	const sessionID = "antigravity:" + id
	path := dbtest.WriteAntigravityTitleTranscript(t, root, "one conversation")
	archive := dbtest.OpenTestDB(t)
	engine := sync.NewEngine(t.Context(), archive, sync.EngineConfig{
		AgentDirs: map[parser.AgentType][]string{parser.AgentAntigravity: {root}}, Machine: "host-a",
	})
	require.NoError(t, engine.SyncPathsContext(t.Context(), []string{path}))
	before, err := archive.GetSession(t.Context(), sessionID)
	require.NoError(t, err)
	require.NotNil(t, before, "transcripts use the existing conversation ID")

	dbPath := filepath.Join(root, "conversations", id+".db")
	require.NoError(t, os.MkdirAll(filepath.Dir(dbPath), 0o755))
	copyAntigravityCLITestSchemaTemplate(t, dbPath)
	require.NoError(t, engine.SyncPathsContext(t.Context(), []string{dbPath}))
	oldIDs, err := archive.ListSessionIDsByFilePath(t.Context(), path, "antigravity")
	require.NoError(t, err)
	assert.Empty(t, oldIDs, "database arrival must move the existing session")
	dbIDs, err := archive.ListSessionIDsByFilePath(t.Context(), dbPath, "antigravity")
	require.NoError(t, err)
	assert.Equal(t, []string{sessionID}, dbIDs)

	require.NoError(t, os.Remove(dbPath))
	require.NoError(t, engine.SyncPathsContext(t.Context(), []string{dbPath}))
	after, err := archive.GetSessionFull(t.Context(), sessionID)
	require.NoError(t, err)
	require.NotNil(t, after, "database removal must retain the surviving transcript")
	assert.Nil(t, after.SourceMissingAt)
	require.NotNil(t, after.FilePath)
	assert.Equal(t, path, *after.FilePath)
	assert.Equal(t, 1, after.MessageCount)
}
