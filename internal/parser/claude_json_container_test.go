package parser

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/testjsonl"
)

const claudeContainerUUID = "019eb791-cf7d-75c1-8439-9ed74c122bb1"
const claudeContainerShadowUUID = "019eb791-cf7d-75c1-8439-9ed74c122bb2"

func claudeContainerFixture(firstMessage string) string {
	// Compact single-line array: the container holds the session's
	// records as one JSON document with no line boundaries.
	return "[" + strings.Join([]string{
		testjsonl.ClaudeUserJSON(firstMessage, tsEarly),
		testjsonl.ClaudeAssistantJSON("Done.", tsEarlyS1),
	}, ",") + "]"
}

func TestClaudeDiscoveryAdmitsWholeJSONSessionContainers(t *testing.T) {
	root := t.TempDir()
	containerOnlyProject := filepath.Join(root, "-Users-dev-code-container")
	containerPath := filepath.Join(
		containerOnlyProject, claudeContainerUUID+".json",
	)
	writeSourceFile(t, containerPath, claudeContainerFixture("container question"))

	shadowProject := filepath.Join(root, "-Users-dev-code-shadow")
	shadowJSONLPath := filepath.Join(
		shadowProject, claudeContainerShadowUUID+".jsonl",
	)
	writeSourceFile(t, shadowJSONLPath, claudeProviderFixture("jsonl question"))
	writeSourceFile(
		t, filepath.Join(shadowProject, claudeContainerShadowUUID+".json"),
		claudeContainerFixture("shadowed"),
	)

	writeSourceFile(
		t, filepath.Join(root, "-Users-dev-code-demo", "summary.json"),
		`{"notes":"not a session"}`,
	)

	provider, ok := NewProvider(AgentClaude, ProviderConfig{
		Roots:   []string{root},
		Machine: "devbox",
	})
	require.True(t, ok)

	discovered, err := provider.Discover(t.Context())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{containerPath, shadowJSONLPath},
		sourceDisplayPaths(discovered),
		"whole-JSON containers must be discovered unless a same-stem .jsonl shadows them")

	source, found, err := provider.FindSource(t.Context(), FindSourceRequest{
		RawSessionID: claudeContainerUUID,
	})
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, containerPath, source.DisplayPath)

	changed, err := provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{
		Path: containerPath, EventKind: "write", WatchRoot: root,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{containerPath}, sourceDisplayPaths(changed))

	ignored, err := provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{
		Path:      filepath.Join(root, "-Users-dev-code-demo", "summary.json"),
		EventKind: "write",
		WatchRoot: root,
	})
	require.NoError(t, err)
	assert.Empty(t, ignored,
		"non-UUID .json files in project directories must stay unclassified")
}

func TestClaudeProviderParsesWholeJSONSessionContainer(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(
		root, "-Users-dev-code-demo", claudeContainerUUID+".json",
	)
	writeSourceFile(t, sourcePath, claudeContainerFixture("container question"))

	provider, ok := NewProvider(AgentClaude, ProviderConfig{
		Roots:   []string{root},
		Machine: "devbox",
	})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)

	outcome, err := provider.Parse(t.Context(), ParseRequest{
		Source:      sources[0],
		Fingerprint: SourceFingerprint{Key: sourcePath, Hash: "abc123"},
	})
	require.NoError(t, err)
	require.Len(t, outcome.Results, 1)
	result := outcome.Results[0]

	assert.Equal(t, claudeContainerUUID, result.Result.Session.ID)
	assert.Equal(t, AgentClaude, result.Result.Session.Agent)
	assert.Equal(t, sourcePath, result.Result.Session.File.Path)
	assert.Equal(t, "container question", result.Result.Session.FirstMessage)
	assert.Len(t, result.Result.Messages, 2)
	assert.Empty(t, result.Result.Checkpoint,
		"whole-JSON containers never earn resume cursors")
}

func TestClaudeIncrementalForcesFullParseForWholeJSONContainer(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(
		root, "-Users-dev-code-demo", claudeContainerUUID+".json",
	)
	writeSourceFile(t, sourcePath, claudeContainerFixture("container question"))

	provider, ok := NewProvider(AgentClaude, ProviderConfig{
		Roots:   []string{root},
		Machine: "devbox",
	})
	require.True(t, ok)
	source, found, err := provider.FindSource(t.Context(), FindSourceRequest{
		RawSessionID: claudeContainerUUID,
	})
	require.NoError(t, err)
	require.True(t, found)

	outcome, status, err := provider.ParseIncremental(
		t.Context(), IncrementalRequest{
			Source:       source,
			Fingerprint:  SourceFingerprint{Key: sourcePath, Size: 1},
			SessionID:    claudeContainerUUID,
			Offset:       8,
			StartOrdinal: 2,
		},
	)
	require.NoError(t, err)
	assert.Equal(t, IncrementalNeedsFullParse, status,
		"whole-JSON containers must rebuild through a full parse")
	assert.True(t, outcome.ForceReplace)
}
