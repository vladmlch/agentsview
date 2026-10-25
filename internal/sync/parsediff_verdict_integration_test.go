package sync_test

// Integration tests for the claude parse-diff per-source recoverability
// verdicts (audit av-21). The verdict is report-only parse state: a
// clean claude source is healthy, malformed-line drops make it
// readable_with_coercions, and a source the current binary cannot parse
// is unreadable. Non-claude entries never carry a verdict.

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/sync"
	"go.kenn.io/agentsview/internal/testjsonl"
)

// TestParseDiffClaudeVerdictHealthy pins the clean-source verdict and
// the lane scope. A verdict is parse state, not drift: the claude
// source parsed with zero dropped lines, so its entry reports
// VerdictHealthy even though the stored row was drifted to make the
// session listed (a listed entry is the only way a verdict reaches the
// report — plain identical rows are counted, not listed). The codex
// sibling is drifted the same way so it is listed too, and it carries
// no verdict at all: the verdict is scoped to the claude lane.
func TestParseDiffClaudeVerdictHealthy(t *testing.T) {
	env := setupFocusedTestEnv(t, parser.AgentClaude, parser.AgentCodex)

	env.writeClaudeSession(t, "test-proj", "verdict-clean.jsonl",
		parseDiffClaudeContent("verdict prompt", "verdict reply"))
	env.writeCodexSession(
		t, filepath.Join("2024", "01", "15"),
		"rollout-20240115-verdict-codex.jsonl",
		parseDiffCodexContent("verdict-codex"),
	)
	runSyncAndAssert(t, env.engine, sync.SyncStats{
		TotalSessions: 2, Synced: 2,
	})

	// Drift both stored rows so both sessions are listed; the sources
	// on disk stay clean, so the claude parse state is still healthy.
	mutateDB(t, env,
		"UPDATE sessions SET first_message = ? WHERE id = ?",
		"drifted claude first message", "verdict-clean")
	mutateDB(t, env,
		"UPDATE sessions SET first_message = ? WHERE id = ?",
		"drifted codex first message", "codex:verdict-codex")

	report := runParseDiff(t, env, sync.ParseDiffOptions{})
	assert.Equal(t, sync.ParseDiffTotals{Examined: 2, Changed: 2},
		report.Totals)

	claude := findSessionDiff(report, "verdict-clean")
	require.NotNil(t, claude, "drifted claude session must be listed")
	require.Equal(t, sync.DiffChanged, claude.Class)
	assert.Equal(t, sync.VerdictHealthy, claude.Verdict,
		"a clean claude source must report a healthy verdict even when its stored row changed")

	codex := findSessionDiff(report, "codex:verdict-codex")
	require.NotNil(t, codex, "drifted codex session must be listed")
	assert.Empty(t, codex.Verdict,
		"a codex entry must carry no verdict")
}

// TestParseDiffClaudeVerdictReadableWithCoercions pins the coercion
// verdict: malformed (invalid-JSON) lines are dropped while the
// surviving content stays indexed, so the source is readable with
// coercions.
func TestParseDiffClaudeVerdictReadableWithCoercions(t *testing.T) {
	env := setupSingleAgentTestEnv(t, parser.AgentClaude)

	content := testjsonl.NewSessionBuilder().
		AddClaudeUser(tsEarly, "coerced prompt").
		AddRaw("}{ not json at all").
		AddClaudeAssistant(tsEarlyS5, "coerced reply").
		AddRaw("{\"truncated\": ").
		String()
	env.writeClaudeSession(t, "test-proj", "verdict-coerced.jsonl", content)
	runSyncAndAssert(t, env.engine, sync.SyncStats{
		TotalSessions: 1, Synced: 1,
		Anomalies: sync.AnomalyStats{
			MalformedLinesTotal:   2,
			MalformedLinesByAgent: map[string]int{"claude": 2},
		},
	})

	// Drift the stored drop count so the session is listed: the drift is
	// an informational incremental-history diff for claude, so the
	// session stays DiffIdentical (InformationalOnly) — the most common
	// real shape for a coerced source. The verdict itself rides on the
	// re-parse's own drop count, not on the drift.
	mutateDB(t, env,
		"UPDATE sessions SET parser_malformed_lines ="+
			" parser_malformed_lines + 1 WHERE id = ?",
		"verdict-coerced")

	report := runParseDiff(t, env, sync.ParseDiffOptions{
		Agents: []parser.AgentType{parser.AgentClaude},
	})
	entry := findSessionDiff(report, "verdict-coerced")
	require.NotNil(t, entry, "coerced session must be listed")
	require.Equal(t, sync.DiffIdentical, entry.Class)
	assert.Equal(t, sync.VerdictReadableWithCoercions, entry.Verdict,
		"dropped malformed lines with indexed content must report the coercion verdict")
}

// TestParseDiffClaudeVerdictUnreadableOnParseError pins the unreadable
// verdict: a claude whole-JSON container whose document cannot be
// decoded fails the parse, and the parse-error entry reports
// VerdictUnreadable.
func TestParseDiffClaudeVerdictUnreadableOnParseError(t *testing.T) {
	env := setupSingleAgentTestEnv(t, parser.AgentClaude)

	env.writeSession(t, env.claudeDir, filepath.Join(
		"test-proj", "019f4c2e-7ac1-7cc1-9f21-000000000abc.json",
	), `[{"broken": `)

	report := runParseDiff(t, env, sync.ParseDiffOptions{
		Agents: []parser.AgentType{parser.AgentClaude},
	})
	require.Equal(t, 1, report.Totals.ParseErrors,
		"the undecodable container must report a parse error")
	require.Len(t, report.Sessions, 1, "one parse-error entry")
	entry := report.Sessions[0]
	assert.Equal(t, sync.DiffParseError, entry.Class)
	assert.True(t, filepath.IsAbs(entry.FilePath) || entry.FilePath != "",
		"parse-error entry should attribute its source path")
	assert.Contains(t, entry.FilePath, "019f4c2e-7ac1-7cc1-9f21-000000000abc.json")
	assert.Equal(t, sync.VerdictUnreadable, entry.Verdict,
		"a source the binary failed to parse must report the unreadable verdict")
}
