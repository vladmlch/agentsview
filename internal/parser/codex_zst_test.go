package parser

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/testjsonl"
)

const codexLegacyJSONUUID = "019eb791-cf7d-75c1-8439-9ed74c122aa1"
const codexCompressedZstUUID = "019eb791-cf7d-75c1-8439-9ed74c122aa2"
const codexLiveJSONLUUID = "019eb791-cf7d-75c1-8439-9ed74c122aa3"

func TestCodexSessionFilenameAdmitsReleasedExtensions(t *testing.T) {
	const uuid = "019eb791-cf7d-75c1-8439-9ed74c122aa3"
	tests := []struct {
		name     string
		filename string
		admit    bool
		wantStem string
	}{
		{
			name:     "live jsonl rollout",
			filename: "rollout-2026-06-11T12-44-06-" + uuid + ".jsonl",
			admit:    true,
			wantStem: "rollout-2026-06-11T12-44-06-" + uuid,
		},
		{
			name:     "legacy whole-json rollout",
			filename: "rollout-2026-06-12T09-00-00-" + uuid + ".json",
			admit:    true,
			wantStem: "rollout-2026-06-12T09-00-00-" + uuid,
		},
		{
			name:     "compressed rollout",
			filename: "rollout-2026-06-13T09-00-00-" + uuid + ".jsonl.zst",
			admit:    true,
			wantStem: "rollout-2026-06-13T09-00-00-" + uuid,
		},
		{
			name:     "plain json is not a rollout",
			filename: "notes.json",
			admit:    false,
		},
		{
			name:     "plain jsonl is not a rollout",
			filename: "scratch.jsonl",
			admit:    false,
		},
		{
			name:     "compressed non-rollout is not a rollout",
			filename: "dump.jsonl.zst",
			admit:    false,
		},
		{
			name:     "rollout with unknown extension",
			filename: "rollout-2026-06-11T12-44-06-" + uuid + ".zip",
			admit:    false,
		},
		{
			name:     "history sidecar is not a rollout",
			filename: "history.jsonl",
			admit:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.admit, isCodexSessionFilename(tt.filename))
			if !tt.admit {
				assert.Empty(t, CodexSessionUUIDFromFilename(tt.filename))
				return
			}
			assert.Equal(t, tt.wantStem, codexRolloutStem(tt.filename))
			assert.Equal(t, uuid, CodexSessionUUIDFromFilename(tt.filename))
		})
	}
}

func TestCodexPathInfoAcceptsReleasedExtensions(t *testing.T) {
	const uuid = "019eb791-cf7d-75c1-8439-9ed74c122aa4"
	root := t.TempDir()
	tests := []struct {
		name       string
		rel        string
		wantLayout CodexLayout
	}{
		{
			name:       "dated jsonl",
			rel:        filepath.Join("2026", "06", "11", "rollout-2026-06-11T12-44-06-"+uuid+".jsonl"),
			wantLayout: CodexLayoutDated,
		},
		{
			name:       "flat legacy json",
			rel:        "rollout-2026-06-12T09-00-00-" + uuid + ".json",
			wantLayout: CodexLayoutArchivedFlat,
		},
		{
			name:       "dated compressed",
			rel:        filepath.Join("2026", "06", "13", "rollout-2026-06-13T09-00-00-"+uuid+".jsonl.zst"),
			wantLayout: CodexLayoutDated,
		},
		{
			name:       "non-rollout json rejected",
			rel:        filepath.Join("2026", "06", "11", "notes.json"),
			wantLayout: CodexLayoutUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			layout, gotUUID, ok := CodexSessionPathInfo(root, filepath.Join(root, tt.rel))
			if tt.wantLayout == CodexLayoutUnknown {
				assert.False(t, ok)
				return
			}
			require.True(t, ok)
			assert.Equal(t, tt.wantLayout, layout)
			assert.Equal(t, uuid, gotUUID)
		})
	}
}

func codexRolloutJSONLContent(uuid string) string {
	return testjsonl.JoinJSONL(
		testjsonl.CodexSessionMetaJSON(
			uuid, "/workspace/project-a", "codex_cli_rs", tsEarly,
		),
		testjsonl.CodexMsgJSON("user", "run the migration", tsEarlyS1),
		testjsonl.CodexMsgJSON("assistant", "migration applied", tsEarlyS5),
	)
}

// codexRolloutContainerContent wraps the JSONL records as one valid JSON
// array: a whole-JSON rollout holds the session's records as array
// elements, so they join with commas rather than newlines.
func codexRolloutContainerContent(uuid string) string {
	return "[" + strings.Join(strings.Split(
		strings.TrimRight(codexRolloutJSONLContent(uuid), "\n"), "\n",
	), ",") + "]"
}

func writeCodexRolloutFile(t *testing.T, path, content string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func compressZstForTest(t *testing.T, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	encoder, err := zstd.NewWriter(&buf)
	require.NoError(t, err)
	_, err = encoder.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, encoder.Close())
	return buf.Bytes()
}

func writeCodexCompressedRollout(t *testing.T, path, content string) string {
	t.Helper()
	compressed := compressZstForTest(t, content)
	return writeCodexRolloutFile(t, path, string(compressed))
}

func TestCodexDiscoveryAdmitsLegacyAndCompressedRollouts(t *testing.T) {
	root := t.TempDir()
	// Each rollout carries a distinct session UUID: discovery collapses
	// same-UUID rollouts to one canonical source regardless of extension.
	jsonlPath := writeCodexRolloutFile(
		t,
		filepath.Join(
			root, "2026", "06", "11",
			"rollout-2026-06-11T12-44-06-"+codexLiveJSONLUUID+".jsonl",
		),
		codexRolloutJSONLContent(codexLiveJSONLUUID),
	)
	legacyPath := writeCodexRolloutFile(
		t,
		filepath.Join(
			root,
			"rollout-2026-06-12T09-00-00-"+codexLegacyJSONUUID+".json",
		),
		codexRolloutContainerContent(codexLegacyJSONUUID),
	)
	compressedPath := writeCodexCompressedRollout(
		t,
		filepath.Join(
			root, "2026", "06", "13",
			"rollout-2026-06-13T09-00-00-"+codexCompressedZstUUID+".jsonl.zst",
		),
		codexRolloutJSONLContent(codexCompressedZstUUID),
	)

	// Non-rollout neighbors in the same day directories must stay ignored.
	writeCodexRolloutFile(
		t, filepath.Join(root, "2026", "06", "11", "notes.json"), "{}",
	)
	writeCodexRolloutFile(
		t, filepath.Join(root, "2026", "06", "11", "scratch.jsonl"), "{}\n",
	)
	writeCodexRolloutFile(
		t, filepath.Join(root, "2026", "06", "11", "dump.jsonl.zst"), "junk",
	)

	provider, ok := NewProvider(AgentCodex, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)

	discovered, err := provider.Discover(t.Context())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{jsonlPath, legacyPath, compressedPath},
		sourceDisplayPaths(discovered),
		"discovery must admit all three rollout extensions and only those")

	streamer, ok := provider.(StreamingDiscoverer)
	require.True(t, ok)
	var streamed []SourceRef
	require.NoError(t, streamer.DiscoverEach(t.Context(), func(source SourceRef) error {
		streamed = append(streamed, source)
		return nil
	}))
	assert.ElementsMatch(t, []string{jsonlPath, legacyPath, compressedPath},
		sourceDisplayPaths(streamed),
		"streaming discovery must match batch discovery admission")

	for _, path := range []string{legacyPath, compressedPath} {
		changed, err := provider.SourcesForChangedPath(
			t.Context(), ChangedPathRequest{Path: path, EventKind: "write"},
		)
		require.NoError(t, err)
		assert.Equal(t, []string{path}, sourceDisplayPaths(changed),
			"watch classification must admit %s", filepath.Base(path))
	}

	ignored, err := provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{
		Path:      filepath.Join(root, "2026", "06", "11", "notes.json"),
		EventKind: "write",
	})
	require.NoError(t, err)
	assert.Empty(t, ignored, "non-rollout .json must stay unclassified")

	source, found, err := provider.FindSource(t.Context(), FindSourceRequest{
		FullSessionID: "codex:" + codexLegacyJSONUUID,
	})
	require.NoError(t, err)
	require.True(t, found, "legacy .json rollouts must be findable by UUID")
	assert.Equal(t, legacyPath, source.DisplayPath)
}

func TestCodexParsesCompressedRolloutEndToEnd(t *testing.T) {
	root := t.TempDir()
	content := codexRolloutJSONLContent(codexCompressedZstUUID)
	compressed := compressZstForTest(t, content)
	path := writeCodexRolloutFile(
		t,
		filepath.Join(
			root, "2026", "06", "13",
			"rollout-2026-06-13T09-00-00-"+codexCompressedZstUUID+".jsonl.zst",
		),
		string(compressed),
	)

	provider, ok := NewProvider(AgentCodex, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	source := requireCodexProviderSource(t, provider, codexCompressedZstUUID)
	fingerprint, err := provider.Fingerprint(t.Context(), source)
	require.NoError(t, err)

	outcome, err := provider.Parse(t.Context(), ParseRequest{
		Source: source, Fingerprint: fingerprint,
	})
	require.NoError(t, err)
	require.Len(t, outcome.Results, 1)
	result := outcome.Results[0].Result

	assert.Equal(t, "codex:"+codexCompressedZstUUID, result.Session.ID)
	assert.Len(t, result.Messages, 2)
	assert.Equal(t, "run the migration", result.Session.FirstMessage)
	assert.Equal(
		t, int64(len(compressed)), result.Session.File.Size,
		"a compressed rollout must report the on-disk source size, not the decompressed copy's",
	)
	assert.Equal(t, path, result.Session.File.Path)
	assert.Empty(t, result.Checkpoint,
		"compressed rollouts are full-parse-only and never earn checkpoints")
}

func TestCodexParsesLegacyWholeJSONRollout(t *testing.T) {
	root := t.TempDir()
	// Compact single-line array: the container carries no line-aligned
	// record boundaries at all.
	content := codexRolloutContainerContent(codexLegacyJSONUUID)
	path := writeCodexRolloutFile(
		t,
		filepath.Join(
			root,
			"rollout-2026-06-12T09-00-00-"+codexLegacyJSONUUID+".json",
		),
		content,
	)

	provider, ok := NewProvider(AgentCodex, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	source := requireCodexProviderSource(t, provider, codexLegacyJSONUUID)
	fingerprint, err := provider.Fingerprint(t.Context(), source)
	require.NoError(t, err)

	outcome, err := provider.Parse(t.Context(), ParseRequest{
		Source: source, Fingerprint: fingerprint,
	})
	require.NoError(t, err)
	require.Len(t, outcome.Results, 1)
	result := outcome.Results[0].Result

	assert.Equal(t, "codex:"+codexLegacyJSONUUID, result.Session.ID)
	assert.Len(t, result.Messages, 2)
	assert.Equal(t, "run the migration", result.Session.FirstMessage)
	assert.Equal(t, int64(len(content)), result.Session.File.Size)
	assert.Equal(t, path, result.Session.File.Path)
	assert.Empty(t, result.Checkpoint,
		"whole-JSON rollouts are full-parse-only and never earn checkpoints")
}

func TestCodexIncrementalForcesFullParseForNonResumableTranscripts(t *testing.T) {
	tests := []struct {
		name string
		uuid string
	}{
		{name: "compressed rollout", uuid: codexCompressedZstUUID},
		{name: "legacy whole-json rollout", uuid: codexLegacyJSONUUID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			content := codexRolloutJSONLContent(tt.uuid)
			var path string
			switch tt.name {
			case "compressed rollout":
				path = writeCodexCompressedRollout(t, filepath.Join(
					root, "2026", "06", "13",
					"rollout-2026-06-13T09-00-00-"+tt.uuid+".jsonl.zst",
				), content)
			default:
				path = writeCodexRolloutFile(t, filepath.Join(
					root,
					"rollout-2026-06-12T09-00-00-"+tt.uuid+".json",
				), codexRolloutContainerContent(tt.uuid))
			}

			provider, ok := NewProvider(
				AgentCodex, ProviderConfig{Roots: []string{root}},
			)
			require.True(t, ok)
			source := requireCodexProviderSource(t, provider, tt.uuid)

			outcome, status, err := provider.ParseIncremental(
				t.Context(), IncrementalRequest{
					Source:       source,
					Fingerprint:  SourceFingerprint{Key: path, Size: 1},
					SessionID:    "codex:" + tt.uuid,
					Offset:       8,
					StartOrdinal: 2,
					Seed:         []byte("stale-checkpoint"),
				},
			)
			require.NoError(t, err)
			assert.Equal(t, IncrementalNeedsFullParse, status,
				"non-resumable transcripts must rebuild through a full parse")
			assert.True(t, outcome.ForceReplace)
		})
	}
}
