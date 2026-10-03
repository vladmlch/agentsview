package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJunieProviderFindSourceSubagent(t *testing.T) {
	root := t.TempDir()
	sessionID := "session-260101-120000-abcd"
	sessionDir := filepath.Join(root, sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(junieSubagentFixture()), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	source, found, err := provider.FindSource(t.Context(), FindSourceRequest{
		RawSessionID: sessionID + "--agent-1",
	})
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, path, source.DisplayPath)
}

func TestJunieCapabilitiesDeclareSubagents(t *testing.T) {
	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{t.TempDir()}})
	require.True(t, ok)
	caps := provider.Capabilities()
	assert.Equal(t, CapabilitySupported, caps.Source.MultiSessionSource)
	assert.Equal(t, CapabilitySupported, caps.Source.ExcludedSessions)
	assert.Equal(t, CapabilitySupported, caps.Content.Subagents)
	assert.Equal(t, CapabilitySupported, caps.Content.Relationships)
	assert.Equal(t, CapabilitySupported, caps.Content.ToolCalls)
	assert.Equal(t, CapabilitySupported, caps.Content.Thinking)
}

func TestJunieFingerprintFoldsCompanions(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-260101-120000-abcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Hi","timestampMs":1704067200000}`+"\n"), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	fp1, err := provider.Fingerprint(t.Context(), sources[0])
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.json"),
		[]byte(`{"taskName":"t","lifecycle":"COMPLETED"}`), 0o600))
	fp2, err := provider.Fingerprint(t.Context(), sources[0])
	require.NoError(t, err)
	assert.NotEqual(t, fp1.Hash, fp2.Hash)
}

func TestJunieCompanionChangedPathMapsToTranscript(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-260101-120000-abcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	path := filepath.Join(sessionDir, "events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Hi","timestampMs":1704067200000}`+"\n"), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.SourcesForChangedPath(t.Context(), ChangedPathRequest{
		Path: filepath.Join(sessionDir, "summary.json"),
	})
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, path, sources[0].DisplayPath)
}

func TestJunieWatchPlanIncludesSidecars(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-260101-120000-abcd")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "events.jsonl"), []byte(
		`{"kind":"UserPromptEvent","requestId":"r1","prompt":"Hi","timestampMs":1704067200000}`+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.json"), []byte(`{}`), 0o600))

	provider, ok := NewProvider(AgentJunie, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	plan, err := provider.WatchPlan(t.Context())
	require.NoError(t, err)
	require.Len(t, plan.Roots, 1)
	assert.Contains(t, plan.Roots[0].IncludeGlobs, "summary.json")
	assert.Contains(t, plan.Roots[0].IncludeGlobs, "state.json")
}

func TestJunieRegistryEntry(t *testing.T) {
	t.Parallel()

	def, ok := AgentByType(AgentJunie)
	require.True(t, ok)
	assert.Equal(t, "Junie", def.DisplayName)
	assert.Equal(t, "JUNIE_DIR", def.EnvVar)
	assert.Equal(t, "JUNIE_HOME", def.DefaultRootEnvVar)
	assert.Equal(t, "junie_dirs", def.ConfigKey)
	assert.Equal(t, []string{".junie/sessions"}, def.DefaultDirs)
	assert.Equal(t, "junie:", def.IDPrefix)
	assert.True(t, def.FileBased)
	assert.True(t, def.HomesSupported)
	assert.True(t, def.Usage.NoPerMessageTokenData)

	_, ok = NewProvider(AgentJunie, ProviderConfig{})
	assert.True(t, ok)
}
