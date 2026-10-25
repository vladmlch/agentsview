package sync

// ABOUTME: unit tests for the claude parse-diff per-source recoverability
// ABOUTME: verdict derivation (audit av-21): table-driven healthy /
// ABOUTME: readable-with-coercions / unreadable mapping and the claude-lane
// ABOUTME: scoping gate.

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.kenn.io/agentsview/internal/parser"
)

// TestClaudeSourceVerdict pins the derivation rules: a failed parse is
// unreadable, malformed-line drops with indexed content are coercions,
// and a clean parse is healthy.
func TestClaudeSourceVerdict(t *testing.T) {
	tests := []struct {
		name           string
		parseFailed    bool
		malformedLines int
		want           SourceVerdict
	}{
		{
			name: "clean parse is healthy",
			want: VerdictHealthy,
		},
		{
			name:           "one dropped line is a coercion",
			malformedLines: 1,
			want:           VerdictReadableWithCoercions,
		},
		{
			name:           "many dropped lines are still one coercion class",
			malformedLines: 7,
			want:           VerdictReadableWithCoercions,
		},
		{
			name:        "failed parse is unreadable",
			parseFailed: true,
			want:        VerdictUnreadable,
		},
		{
			name:           "failure wins over drops (no content was derived)",
			parseFailed:    true,
			malformedLines: 3,
			want:           VerdictUnreadable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := claudeSourceVerdict(tt.parseFailed, tt.malformedLines)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestParseDiffSourceVerdictScopesToClaude pins the lane scope: only
// claude entries carry a verdict; every other agent's entries stay
// empty so the report surface is unchanged outside the audited lane.
func TestParseDiffSourceVerdictScopesToClaude(t *testing.T) {
	assert.Equal(t, VerdictHealthy,
		parseDiffSourceVerdict(parser.AgentClaude, false, 0))
	assert.Equal(t, VerdictReadableWithCoercions,
		parseDiffSourceVerdict(parser.AgentClaude, false, 2))
	assert.Equal(t, VerdictUnreadable,
		parseDiffSourceVerdict(parser.AgentClaude, true, 0))

	for _, agent := range []parser.AgentType{
		parser.AgentCodex, parser.AgentGemini, parser.AgentCursor,
		parser.AgentOpenCode, parser.AgentKiro,
	} {
		assert.Empty(t, parseDiffSourceVerdict(agent, false, 0),
			"agent %q must carry no verdict", agent)
		assert.Empty(t, parseDiffSourceVerdict(agent, true, 4),
			"agent %q must carry no verdict even on failure", agent)
	}
}

// TestSourceVerdictJSONValues pins the serialized values: they are part
// of the parse-diff CLI's machine-readable surface.
func TestSourceVerdictJSONValues(t *testing.T) {
	assert.Equal(t, "healthy", string(VerdictHealthy))
	assert.Equal(t, "readable_with_coercions",
		string(VerdictReadableWithCoercions))
	assert.Equal(t, "unreadable", string(VerdictUnreadable))
}
