package sync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/testjsonl"
)

// batchExcludeTable is the shared semantics table for the watcher predicate
// and the batch matcher: both lanes must answer every row identically.
var batchExcludeTable = []struct {
	name     string
	patterns []string
	path     string
	root     string
	want     bool
}{
	{
		name:     "component match excludes nested source",
		patterns: []string{"node_modules"},
		path:     filepath.Join("root", "proj", "node_modules", "s.jsonl"),
		root:     "root",
		want:     true,
	},
	{
		name:     "component match excludes direct child file",
		patterns: []string{"*.lock*"},
		path:     filepath.Join("root", "proj", "pnpm.lock"),
		root:     "root",
		want:     true,
	},
	{
		name:     "component mismatch admits",
		patterns: []string{"node_modules"},
		path:     filepath.Join("root", "proj", "s.jsonl"),
		root:     "root",
		want:     false,
	},
	{
		name:     "separator pattern matches whole relative path",
		patterns: []string{"secrets/*"},
		path:     filepath.Join("root", "secrets", "s.jsonl"),
		root:     "root",
		want:     true,
	},
	{
		name:     "separator pattern does not act component-wise",
		patterns: []string{"secrets/inner"},
		path:     filepath.Join("root", "a", "secrets", "s.jsonl"),
		root:     "root",
		want:     false,
	},
	{
		name:     "path outside root admits",
		patterns: []string{"node_modules"},
		path:     filepath.Join("other", "node_modules", "s.jsonl"),
		root:     "root",
		want:     false,
	},
	{
		name:     "no patterns admits",
		patterns: nil,
		path:     filepath.Join("root", "node_modules", "s.jsonl"),
		root:     "root",
		want:     false,
	},
}

// TestBatchExcludeMatcherAgreesWithWatcherLane asserts the batch matcher and
// the watcher predicate answer the shared table identically, including the
// watcher's own normalization of the configured pattern list.
func TestBatchExcludeMatcherAgreesWithWatcherLane(t *testing.T) {
	for _, tt := range batchExcludeTable {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := newBatchExcludeMatcher(tt.patterns)
			require.NoError(t, err)
			watcherVerdict := shouldExcludeForRoot(
				normalizeExcludePatterns(tt.patterns), tt.path, tt.root,
			)
			batchVerdict := matcher.excludes(tt.path, []string{tt.root})
			assert.Equal(t, tt.want, watcherVerdict,
				"watcher lane verdict")
			assert.Equal(t, tt.want, batchVerdict,
				"batch lane verdict")
		})
	}
}

func TestBatchExcludeMatcherMultiRoot(t *testing.T) {
	matcher, err := newBatchExcludeMatcher([]string{"node_modules"})
	require.NoError(t, err)
	path := filepath.Join("rootB", "node_modules", "s.jsonl")
	assert.True(t, matcher.excludes(path, []string{"rootA", "rootB"}),
		"a source excluded under any configured root stays excluded")
	assert.False(t, matcher.excludes(path, []string{"rootA"}),
		"a source outside every root stays admitted")
}

func TestBatchExcludeMatcherRejectsInvalidPattern(t *testing.T) {
	for _, pattern := range []string{"[", "[a-", "unclosed["} {
		_, err := newBatchExcludeMatcher([]string{pattern})
		assert.Error(t, err, "pattern %q must fail to compile", pattern)
	}
	matcher, err := newBatchExcludeMatcher([]string{".git", "node_modules"})
	require.NoError(t, err)
	assert.True(t, matcher.enabled())
}

func TestBatchExcludeMatcherFailClosedExcludesEverything(t *testing.T) {
	failClosed := newFailClosedBatchExcludeMatcher(
		assert.AnError,
	)
	assert.True(t, failClosed.excludes(
		filepath.Join("root", "proj", "s.jsonl"), []string{"root"}),
		"an unusable pattern set must never admit a path")
	assert.True(t, failClosed.excludesSource(
		parser.SourceRef{DisplayPath: "anywhere"}, nil),
		"an unusable pattern set must exclude resolved sources too")
}

func TestBatchExcludeMatcherValidationMatchesConfig(t *testing.T) {
	// The config loader is the loud gate: an invalid pattern refuses startup.
	require.NoError(t, config.ValidateWatchExcludePatterns(
		[]string{".git", "node_modules", "*.lock*", "secrets/*"},
	))
	require.Error(t, config.ValidateWatchExcludePatterns([]string{"["}))
	require.Error(t, config.ValidateWatchExcludePatterns(
		[]string{".git", "broken["},
	))
}

// discoveryFixture builds one engine with per-agent roots and the shared
// exclusion patterns, then returns the engine and the root paths.
func discoveryFixture(t *testing.T, patterns []string) (*Engine, map[parser.AgentType]string) {
	t.Helper()
	base := t.TempDir()
	claudeRoot := filepath.Join(base, "claude")
	codexRoot := filepath.Join(base, "codex", "sessions")
	agYRoot := filepath.Join(base, "antigravity")
	agYCLIRoot := filepath.Join(base, "antigravity-cli")

	// Claude: <root>/<project>/<session>.jsonl. Both files carry a parseable
	// session so full SyncAll passes in the tests below ingest the admitted
	// one and stamp its freshness watermark.
	claudeKeep := testjsonl.JoinJSONL(
		testjsonl.ClaudeUserJSON("keep", "2024-01-01T10:00:00Z"),
	)
	claudeDrop := testjsonl.JoinJSONL(
		testjsonl.ClaudeUserJSON("drop", "2024-01-01T11:00:00Z"),
	)
	require.NoError(t, os.MkdirAll(
		filepath.Join(claudeRoot, "proj-keep"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(claudeRoot, "proj-keep", "session.jsonl"),
		[]byte(claudeKeep), 0o644))
	require.NoError(t, os.MkdirAll(
		filepath.Join(claudeRoot, "secret-project"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(claudeRoot, "secret-project", "session.jsonl"),
		[]byte(claudeDrop), 0o644))

	// Codex: <root>/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl (dated) and
	// <root>/rollout-<ts>-<uuid>.jsonl (flat archived).
	require.NoError(t, os.MkdirAll(
		filepath.Join(codexRoot, "2026", "10", "23"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(codexRoot, "2026", "10", "23",
			"rollout-2026-10-23T10-00-00-019eb791-cf7d-75c1-8439-9ed74c1229e1.jsonl"),
		[]byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(codexRoot,
			"rollout-2026-10-23T11-00-00-secret-019eb791-cf7d-75c1-8439-9ed74c1229f2.jsonl"),
		[]byte("{}\n"), 0o644))

	// Antigravity: <root>/conversations/<id>.db
	require.NoError(t, os.MkdirAll(
		filepath.Join(agYRoot, "conversations"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(agYRoot, "conversations", "keep-chat.db"),
		[]byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(agYRoot, "conversations", "secret-chat.db"),
		[]byte("{}"), 0o644))

	// Antigravity CLI: <root>/conversations/<id>.db
	require.NoError(t, os.MkdirAll(
		filepath.Join(agYCLIRoot, "conversations"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(agYCLIRoot, "conversations", "cli-keep.db"),
		[]byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(agYCLIRoot, "conversations", "cli-secret.db"),
		[]byte("{}"), 0o644))

	engine := NewEngine(t.Context(), openTestDB(t), EngineConfig{
		AgentDirs: map[parser.AgentType][]string{
			parser.AgentClaude:         {claudeRoot},
			parser.AgentCodex:          {codexRoot},
			parser.AgentAntigravity:    {agYRoot},
			parser.AgentAntigravityCLI: {agYCLIRoot},
		},
		ExcludePatterns: patterns,
		Machine:         "local",
	})
	t.Cleanup(engine.Close)
	return engine, map[parser.AgentType]string{
		parser.AgentClaude:         claudeRoot,
		parser.AgentCodex:          codexRoot,
		parser.AgentAntigravity:    agYRoot,
		parser.AgentAntigravityCLI: agYCLIRoot,
	}
}

func discoveredPaths(files []parser.DiscoveredFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

// TestBatchDiscoveryExcludesMatchingSources asserts the exclusion filter sits
// in front of discovery for every file-based provider lane, using the
// watcher's component semantics.
func TestBatchDiscoveryExcludesMatchingSources(t *testing.T) {
	engine, roots := discoveryFixture(t, []string{"*secret*"})

	files, failures := engine.discoverProviderSources(t.Context(), nil, nil)
	require.Zero(t, failures)
	byAgent := make(map[parser.AgentType][]string)
	for _, file := range files {
		byAgent[file.Agent] = append(byAgent[file.Agent], file.Path)
	}

	assert.Equal(t, []string{
		filepath.Join(roots[parser.AgentClaude], "proj-keep", "session.jsonl"),
	}, byAgent[parser.AgentClaude],
		"claude source in an excluded project dir must stay invisible")
	assert.Equal(t, []string{
		filepath.Join(roots[parser.AgentCodex], "2026", "10", "23",
			"rollout-2026-10-23T10-00-00-019eb791-cf7d-75c1-8439-9ed74c1229e1.jsonl"),
	}, byAgent[parser.AgentCodex],
		"codex rollout matching an exclude pattern must stay invisible")
	assert.Equal(t, []string{
		filepath.Join(roots[parser.AgentAntigravity], "conversations", "keep-chat.db"),
	}, byAgent[parser.AgentAntigravity],
		"antigravity conversation matching an exclude pattern must stay invisible")
	assert.Equal(t, []string{
		filepath.Join(roots[parser.AgentAntigravityCLI], "conversations", "cli-keep.db"),
	}, byAgent[parser.AgentAntigravityCLI],
		"antigravity-cli conversation matching an exclude pattern must stay invisible")
}

// TestBatchDiscoveryWithoutPatternsAdmitsEverything pins the default: an
// engine built without exclusion patterns admits every fixture source.
func TestBatchDiscoveryWithoutPatternsAdmitsEverything(t *testing.T) {
	engine, _ := discoveryFixture(t, nil)

	files, failures := engine.discoverProviderSources(t.Context(), nil, nil)
	require.Zero(t, failures)
	assert.Len(t, discoveredPaths(files), 8)
}

// TestBatchDiscoveryFailClosedExcludesEverything drives a full SyncAll with
// the engine-level fail-closed matcher (invalid patterns bypassing config
// validation): nothing may be ingested.
func TestBatchDiscoveryFailClosedExcludesEverything(t *testing.T) {
	engine, _ := discoveryFixture(t, nil)
	engine.batchExcludes = newFailClosedBatchExcludeMatcher(
		assert.AnError,
	)

	stats := engine.SyncAll(t.Context(), nil)
	assert.Zero(t, stats.Synced,
		"a fail-closed exclusion set must exclude every source")
	assert.Zero(t, stats.Failed)
}

// TestExcludedSourceLeavesNoWatermark asserts the watermark discipline: a
// path excluded at discovery leaves no session row and no provider_freshness
// row, so nothing marks it seen.
func TestExcludedSourceLeavesNoWatermark(t *testing.T) {
	root := t.TempDir()
	keepPath := filepath.Join(root, "proj-keep", "session.jsonl")
	excludedPath := filepath.Join(root, "secret-project", "session.jsonl")
	for _, path := range []string{keepPath, excludedPath} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	}
	require.NoError(t, os.WriteFile(keepPath, []byte(testjsonl.JoinJSONL(
		testjsonl.ClaudeUserJSON("keep", "2024-01-01T10:00:00Z"),
	)), 0o600))
	require.NoError(t, os.WriteFile(excludedPath, []byte(testjsonl.JoinJSONL(
		testjsonl.ClaudeUserJSON("secret", "2024-01-01T11:00:00Z"),
	)), 0o600))

	engine := NewEngine(t.Context(), openTestDB(t), EngineConfig{
		AgentDirs: map[parser.AgentType][]string{
			parser.AgentClaude: {root},
		},
		ExcludePatterns: []string{"*secret*"},
		Machine:         "local",
	})
	t.Cleanup(engine.Close)
	database := engine.db

	stats := engine.SyncAll(t.Context(), nil)
	require.Equal(t, 1, stats.Synced,
		"only the admitted claude source may sync")

	ids, err := database.ListSessionIDsByFilePath(
		t.Context(), excludedPath, string(parser.AgentClaude),
	)
	require.NoError(t, err)
	assert.Empty(t, ids,
		"an excluded source must not be ingested")

	_, hasHash, err := database.GetProviderStatHash(
		t.Context(), parser.AgentClaude, excludedPath,
	)
	require.NoError(t, err)
	assert.False(t, hasHash,
		"an excluded source must leave no provider_freshness row")

	_, hasHash, err = database.GetProviderStatHash(
		t.Context(), parser.AgentClaude, keepPath,
	)
	require.NoError(t, err)
	assert.True(t, hasHash,
		"the admitted source must carry a freshness watermark")
}

// TestSourceByteBudgetSkipsOversizedSource asserts parse-admission: an
// over-ceiling source is skipped with a recorded, identity-keyed failure,
// an under-ceiling source syncs normally, and a later change to the
// oversized source re-enters admission.
func TestSourceByteBudgetSkipsOversizedSource(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proj"), 0o755))
	normalPath := filepath.Join(root, "proj", "normal.jsonl")
	oversizedPath := filepath.Join(root, "proj", "oversized.jsonl")
	require.NoError(t, os.WriteFile(normalPath, []byte(testjsonl.JoinJSONL(
		testjsonl.ClaudeUserJSON("hello", "2024-01-01T10:00:00Z"),
	)), 0o600))
	big := make([]byte, 4096)
	for i := range big {
		big[i] = '\n'
	}
	require.NoError(t, os.WriteFile(oversizedPath, big, 0o600))

	engine := NewEngine(t.Context(), openTestDB(t), EngineConfig{
		AgentDirs: map[parser.AgentType][]string{
			parser.AgentClaude: {root},
		},
		MaxSourceBytes: 512,
		Machine:        "local",
	})
	t.Cleanup(engine.Close)
	database := engine.db

	stats := engine.SyncAll(t.Context(), nil)
	assert.Equal(t, 1, stats.Synced,
		"the under-ceiling source must sync")
	assert.Equal(t, 1, stats.Skipped,
		"the over-ceiling source must be skipped, not failed")
	assert.Zero(t, stats.Failed)

	ids, err := database.ListSessionIDsByFilePath(
		t.Context(), oversizedPath, string(parser.AgentClaude),
	)
	require.NoError(t, err)
	assert.Empty(t, ids, "an over-budget source must not be ingested")

	failureKey := providerAgentSkipCacheKey(oversizedPath, parser.AgentClaude)
	identity, recorded := engine.failures.Lookup(failureKey)
	require.True(t, recorded,
		"the budget skip must be recorded in the source-failure lane")
	assert.Equal(t, int64(4096), identity.Size)
	assert.NotZero(t, identity.MTimeNS)

	_, hasHash, err := database.GetProviderStatHash(
		t.Context(), parser.AgentClaude, oversizedPath,
	)
	require.NoError(t, err)
	assert.False(t, hasHash,
		"an over-budget source must leave no freshness watermark")

	// Shrinking the oversized source below the ceiling changes its stat
	// identity, so admission must retry and ingest it.
	require.NoError(t, os.WriteFile(oversizedPath, []byte(testjsonl.JoinJSONL(
		testjsonl.ClaudeUserJSON("shrunk", "2024-01-01T11:00:00Z"),
	)), 0o600))
	stats = engine.SyncAll(t.Context(), nil)
	assert.Equal(t, 1, stats.Synced,
		"a source that drops below the ceiling must be re-admitted")
	ids, err = database.ListSessionIDsByFilePath(
		t.Context(), oversizedPath, string(parser.AgentClaude),
	)
	require.NoError(t, err)
	assert.NotEmpty(t, ids)
}
