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
