package parser

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeStatDigestFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestFileStatTupleDigestRejectsUnavailableChangeTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeStatDigestFile(t, path, "unchanged\n")

	digest := fileStatTupleDigestWithChangeTime(
		func(string, os.FileInfo) (int64, bool) {
			return 0, false
		},
		0xC1,
		path,
	)

	assert.Zero(t, digest,
		"size and mtime alone must not produce a trusted digest")
}

func TestFileStatTupleDigestDetectsSameStatReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeStatDigestFile(t, path, "original\n")
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, os.Chtimes(path, mtime, mtime))
	// A coarse clock can give the original and its replacement the same
	// change time, so only the file identity tells them apart.
	sameTick := func(string, os.FileInfo) (int64, bool) {
		return 1, true
	}
	before := fileStatTupleDigestWithChangeTime(sameTick, 0xC1, path)
	require.NotZero(t, before)

	tmp := path + ".tmp"
	writeStatDigestFile(t, tmp, "replaced\n")
	require.NoError(t, os.Rename(tmp, path))
	require.NoError(t, os.Chtimes(path, mtime, mtime))

	assert.NotEqual(t, before,
		fileStatTupleDigestWithChangeTime(sameTick, 0xC1, path),
		"a same-size, same-time replacement must change the digest")
}

func TestClaudeProviderComputesMultiFileStatHash(t *testing.T) {
	provider, ok := NewProvider(AgentClaude, ProviderConfig{
		Machine: "local",
	})
	require.True(t, ok)
	hasher, ok := provider.(MultiFileStatHasher)
	require.True(t, ok, "claude provider must implement MultiFileStatHasher")
	assert.Equal(t, CapabilitySupported,
		provider.Capabilities().Source.MultiFileStatHash,
		"claude must declare MultiFileStatHash so the engine registers the hasher")

	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeStatDigestFile(t, path, "line one\n")

	first := hasher.ComputeMultiFileStatHash(path)
	info, err := os.Stat(path)
	require.NoError(t, err)
	if _, changeTimeOK := codexIndexChangeTime(path, info); !changeTimeOK {
		assert.Zero(t, first,
			"unavailable change-time must disable the stat digest")
		return
	}
	require.NotZero(t, first)
	assert.Equal(t, first, hasher.ComputeMultiFileStatHash(path),
		"digest must be stable while the transcript stat is unchanged")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("line two\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.NotEqual(t, first, hasher.ComputeMultiFileStatHash(path),
		"an appended transcript must change the digest")
}

func TestCodexProviderComputesMultiFileStatHash(t *testing.T) {
	root := t.TempDir()
	provider := newCodexTestProvider(t, root)
	hasher, ok := any(provider).(MultiFileStatHasher)
	require.True(t, ok, "codex provider must implement MultiFileStatHasher")
	assert.Equal(t, CapabilitySupported,
		provider.Capabilities().Source.MultiFileStatHash,
		"codex must declare MultiFileStatHash so the engine registers the hasher")

	path := filepath.Join(root, "sessions", "rollout-2026-01-01.jsonl")
	writeStatDigestFile(t, path, "{}\n")

	first := hasher.ComputeMultiFileStatHash(path)
	info, err := os.Stat(path)
	require.NoError(t, err)
	if _, changeTimeOK := codexIndexChangeTime(path, info); !changeTimeOK {
		assert.Zero(t, first,
			"unavailable change-time must disable the stat digest")
		return
	}
	require.NotZero(t, first)
	assert.Equal(t, first, hasher.ComputeMultiFileStatHash(path),
		"digest must be stable while transcript and index stats are unchanged")

	// A session_index.jsonl change (e.g. a thread title rename) must break
	// the digest even when the transcript itself is untouched, so the warm
	// short-circuit can never mask an index-only metadata refresh.
	indexPath := filepath.Join(root, CodexSessionIndexFilename)
	writeStatDigestFile(t, indexPath, `{"id":"x","name":"Renamed"}`+"\n")
	withIndex := hasher.ComputeMultiFileStatHash(path)
	indexInfo, err := os.Stat(indexPath)
	require.NoError(t, err)
	if _, changeTimeOK := codexIndexChangeTime(
		indexPath, indexInfo,
	); !changeTimeOK {
		assert.Zero(t, withIndex,
			"an existing sidecar without change-time must disable the digest")
		return
	}
	assert.NotEqual(t, first, withIndex,
		"an appearing session index must change the digest")

	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(indexPath, future, future))
	assert.NotEqual(t, withIndex, hasher.ComputeMultiFileStatHash(path),
		"an index mtime change must change the digest")
}

// sameStatSwap replaces path with newContent of identical size, restores the
// original mtime, and lets the rename swap the inode: the on-disk stat a
// size/mtime-only composite observes is indistinguishable from before, which
// is exactly the rewrite class av-3 reports.
func sameStatSwap(t *testing.T, path string, newContent []byte) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Len(t, newContent, int(info.Size()),
		"the swap payload must preserve the file size")
	tmp := path + ".swap"
	require.NoError(t, os.WriteFile(tmp, newContent, 0o644))
	require.NoError(t, os.Rename(tmp, path))
	require.NoError(t, os.Chtimes(path, info.ModTime(), info.ModTime()))
}

func requireAntigravityStatDigestPlatform(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		info, err := os.Stat(path)
		require.NoError(t, err)
		if _, changeTimeOK := codexIndexChangeTime(path, info); !changeTimeOK {
			return
		}
	}
}

func TestAntigravityProviderComputesMultiFileStatHash(t *testing.T) {
	root := t.TempDir()
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	writeAntigravityIDEProviderFixture(t, root, id)
	dbPath := filepath.Join(root, "conversations", id+".db")
	annotationPath := filepath.Join(root, "annotations", id+".pbtxt")
	transcriptPath := antigravityBrainTranscriptPath(root, id)
	writeStatDigestFile(t, transcriptPath, "line one\n")

	provider, ok := NewProvider(AgentAntigravity, ProviderConfig{
		Roots:   []string{root},
		Machine: "devbox",
	})
	require.True(t, ok)
	hasher, ok := provider.(MultiFileStatHasher)
	require.True(t, ok, "antigravity provider must implement MultiFileStatHasher")
	require.Equal(t, CapabilitySupported,
		provider.Capabilities().Source.MultiFileStatHash,
		"antigravity must declare MultiFileStatHash so the engine registers the hasher")
	require.True(t,
		provider.Capabilities().Sync.FingerprintHashRequiredForFreshness,
		"a same-stat rewrite is only caught when the content hash gates the skip")

	first := hasher.ComputeMultiFileStatHash(dbPath)
	requireAntigravityStatDigestPlatform(t, dbPath)
	if first == 0 {
		assert.Zero(t, first,
			"unavailable identity fields must disable the stat digest")
		return
	}
	require.NotZero(t, first)
	assert.Equal(t, first, hasher.ComputeMultiFileStatHash(dbPath),
		"digest must be stable while the composite stat is unchanged")

	// A companion-only, same-size, same-mtime rewrite must break the digest:
	// the size/mtime skip composite cannot see it at all.
	sameStatSwap(t, annotationPath, []byte(
		"last_user_view_time:{seconds:1779326586 nanos:1}\n"))
	assert.NotEqual(t, first, hasher.ComputeMultiFileStatHash(dbPath),
		"a same-stat companion rewrite must change the digest")

	// Same class on the brain transcript companion.
	withAnnotation := hasher.ComputeMultiFileStatHash(dbPath)
	sameStatSwap(t, transcriptPath, []byte("line twu\n"))
	assert.NotEqual(t, withAnnotation, hasher.ComputeMultiFileStatHash(dbPath),
		"a same-stat transcript rewrite must change the digest")
}

func TestAntigravityCLIProviderComputesMultiFileStatHash(t *testing.T) {
	root := t.TempDir()
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	writeAntigravityCLIProviderFixture(t, root, id)
	pbPath := filepath.Join(root, "conversations", id+".pb")
	cachePath := filepath.Join(root, "cache", "last_conversations.json")

	provider, ok := NewProvider(AgentAntigravityCLI, ProviderConfig{
		Roots:   []string{root},
		Machine: "devbox",
	})
	require.True(t, ok)
	hasher, ok := provider.(MultiFileStatHasher)
	require.True(t, ok,
		"antigravity cli provider must implement MultiFileStatHasher")
	require.Equal(t, CapabilitySupported,
		provider.Capabilities().Source.MultiFileStatHash,
		"antigravity cli must declare MultiFileStatHash so the engine registers the hasher")
	require.True(t,
		provider.Capabilities().Sync.FingerprintHashRequiredForFreshness,
		"a same-stat rewrite is only caught when the content hash gates the skip")

	first := hasher.ComputeMultiFileStatHash(pbPath)
	requireAntigravityStatDigestPlatform(t, pbPath)
	if first == 0 {
		assert.Zero(t, first,
			"unavailable identity fields must disable the stat digest")
		return
	}
	require.NotZero(t, first)
	assert.Equal(t, first, hasher.ComputeMultiFileStatHash(pbPath),
		"digest must be stable while the composite stat is unchanged")

	sameStatSwap(t, pbPath, []byte("new-encrypted-placeholder"))
	assert.NotEqual(t, first, hasher.ComputeMultiFileStatHash(pbPath),
		"a same-size, same-mtime source replacement must change the digest")

	// A newly appearing companion (the workspace cache) must break the digest
	// exactly like it changes the effective stat composite.
	withSwap := hasher.ComputeMultiFileStatHash(pbPath)
	writeStatDigestFile(t, cachePath, "{}")
	assert.NotEqual(t, withSwap, hasher.ComputeMultiFileStatHash(pbPath),
		"an appearing workspace cache companion must change the digest")
}

func TestAntigravityStatTupleDigestDetectsInodeOnlySwap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.db")
	writeStatDigestFile(t, path, "original\n")
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, os.Chtimes(path, mtime, mtime))
	// A coarse clock can hand the original and its replacement the same
	// change time and an identical stat, so only the inode tells them apart.
	sameTick := func(string, os.FileInfo) (int64, bool) {
		return 1, true
	}
	before := antigravityStatTupleDigestWithChangeTime(
		sameTick, antigravityIDEStatDigestSep, path,
	)
	require.NotZero(t, before)

	tmp := path + ".tmp"
	writeStatDigestFile(t, tmp, "replaced\n")
	require.NoError(t, os.Rename(tmp, path))
	require.NoError(t, os.Chtimes(path, mtime, mtime))

	assert.NotEqual(t, before,
		antigravityStatTupleDigestWithChangeTime(
			sameTick, antigravityIDEStatDigestSep, path,
		),
		"a same-size, same-mtime, same-ctime replacement must still change "+
			"the digest through the inode")
}

func TestAntigravityStatTupleDigestDegradesWithoutChangeTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.db")
	writeStatDigestFile(t, path, "unchanged\n")

	digest := antigravityStatTupleDigestWithChangeTime(
		func(string, os.FileInfo) (int64, bool) {
			return 0, false
		},
		antigravityCLIStatDigestSep, path,
	)
	assert.Zero(t, digest,
		"size and mtime alone must not produce a trusted digest")
}

func TestAntigravityStatTupleDigestSeparatorIsFormatVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.db")
	writeStatDigestFile(t, path, "unchanged\n")
	sameTick := func(string, os.FileInfo) (int64, bool) {
		return 1, true
	}

	ide := antigravityStatTupleDigestWithChangeTime(
		sameTick, antigravityIDEStatDigestSep, path,
	)
	cli := antigravityStatTupleDigestWithChangeTime(
		sameTick, antigravityCLIStatDigestSep, path,
	)
	// The separator byte is the composite's format version. Antigravity
	// never persisted a provider_freshness row before this format existed,
	// and a digest computed from any other tuple shape (or any future
	// revision that forgets to bump the byte) must never compare equal to
	// the stored one, so a legacy composite can only force a re-check.
	foreign := fileStatTupleDigestWithChangeTime(sameTick, 0xC1, path)
	require.NotZero(t, ide)
	require.NotZero(t, cli)
	assert.NotEqual(t, ide, cli)
	assert.NotEqual(t, ide, foreign)
	assert.NotEqual(t, cli, foreign)
}
