package parser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingLayoutSink captures the layout drift records a source set
// delivers to its injected sink.
type recordingLayoutSink struct {
	changes []CodexLayoutChange
}

func (s *recordingLayoutSink) RecordCodexLayoutChange(change CodexLayoutChange) {
	s.changes = append(s.changes, change)
}

const codexLayoutReceiptTestUUID = "019f0000-0000-7000-8000-00000000c0d1"

func codexRolloutName(uuid string) string {
	return "rollout-2026-09-15T10-00-00-" + uuid + ".jsonl"
}

// writeDatedCodexRollout writes one rollout under the standard
// year/month/day layout and returns its path.
func writeDatedCodexRollout(
	t *testing.T, root, uuid string,
) string {
	t.Helper()
	day := filepath.Join(root, "2026", "09", "15")
	require.NoError(t, os.MkdirAll(day, 0o755))
	path := filepath.Join(day, codexRolloutName(uuid))
	require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
	return path
}

// flattenCodexRollout moves one dated rollout to the root itself, simulating
// a flat↔dated rename, and removes the now-empty dated tree.
func flattenCodexRollout(t *testing.T, datedPath, root string) {
	t.Helper()
	require.NoError(t, os.Rename(
		datedPath, filepath.Join(root, filepath.Base(datedPath)),
	))
	require.NoError(t, os.RemoveAll(filepath.Join(root, "2026")))
}

func newReceiptTestSourceSet(
	root string, sink *recordingLayoutSink,
) codexSourceSet {
	sources := newCodexSourceSet(AgentCodex, []string{root})
	sources.receipts = newCodexLayoutReceipts(AgentCodex)
	sources.layoutSink = sink
	return sources
}

// TestCodexLayoutFlagsString pins the layout-set names the receipt logs and
// the anomaly samples serialize.
func TestCodexLayoutFlagsString(t *testing.T) {
	tests := []struct {
		flags codexLayoutFlags
		want  string
	}{
		{0, "none"},
		{codexFlatObserved, "flat"},
		{codexDatedObserved, "dated"},
		{codexFlatObserved | codexDatedObserved, "flat+dated"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.flags.String())
	}
}

// TestCodexLayoutReceiptObserve drives the registry directly through the
// full receipt lifecycle: first observation receipts an INFO, stable passes
// stay silent, a drift warns and returns one change record, an empty pass
// commits nothing, and dual-layout uuids warn once per process.
func TestCodexLayoutReceiptObserve(t *testing.T) {
	logBuf := captureLog(t)
	r := newCodexLayoutReceipts(AgentCodex)

	t.Run("first observation logs an INFO receipt and no change", func(t *testing.T) {
		var dated codexLayoutObservation
		dated.note(CodexLayoutDated, codexLayoutReceiptTestUUID)
		assert.Empty(t, r.observe("/root", dated))
		assertLogContains(t, logBuf,
			"codex layout receipt",
			`root "/root" uses dated`,
			"first observation",
		)
	})

	t.Run("stable layout stays silent", func(t *testing.T) {
		before := logBuf.Len()
		var dated codexLayoutObservation
		dated.note(CodexLayoutDated, codexLayoutReceiptTestUUID)
		assert.Empty(t, r.observe("/root", dated))
		assert.Equal(t, before, logBuf.Len(), "no new log lines")
	})

	t.Run("layout change warns and returns one record", func(t *testing.T) {
		var flat codexLayoutObservation
		flat.note(CodexLayoutArchivedFlat, codexLayoutReceiptTestUUID)
		changes := r.observe("/root", flat)
		require.Len(t, changes, 1)
		assert.Equal(t, CodexLayoutChange{
			Agent: AgentCodex,
			Root:  "/root",
			From:  "dated",
			To:    "flat",
		}, changes[0])
		assertLogContains(t, logBuf,
			"WARNING: codex layout change",
			`root "/root" changed dated -> flat`,
			"live-over-archived",
		)
	})

	t.Run("mixed root losing one side also counts as drift", func(t *testing.T) {
		var mixed codexLayoutObservation
		mixed.note(CodexLayoutArchivedFlat, codexLayoutReceiptTestUUID)
		mixed.note(CodexLayoutDated, "other-uuid")
		assert.Empty(t, r.observe("/mixed", mixed),
			"first observation of the root")

		var datedOnly codexLayoutObservation
		datedOnly.note(CodexLayoutDated, codexLayoutReceiptTestUUID)
		changes := r.observe("/mixed", datedOnly)
		require.Len(t, changes, 1)
		assert.Equal(t, "flat+dated", changes[0].From)
		assert.Equal(t, "dated", changes[0].To)
	})

	t.Run("empty pass commits nothing", func(t *testing.T) {
		assert.Empty(t, r.observe("/fresh", codexLayoutObservation{}),
			"an empty root says nothing about layout")
		var dated codexLayoutObservation
		dated.note(CodexLayoutDated, codexLayoutReceiptTestUUID)
		assert.Empty(t, r.observe("/fresh", dated),
			"the first real session is a first observation, not a change")
		assertLogContains(t, logBuf, `root "/fresh" uses dated`)
	})

	t.Run("dual-layout uuid warns once per root and uuid", func(t *testing.T) {
		logBuf.Reset()
		var mixed codexLayoutObservation
		mixed.note(CodexLayoutArchivedFlat, codexLayoutReceiptTestUUID)
		mixed.note(CodexLayoutDated, codexLayoutReceiptTestUUID)
		assert.Empty(t, r.observe("/dual", mixed),
			"dual-layout uuids warn, they are not drift records")
		assertLogContains(t, logBuf,
			"WARNING: codex dual-layout uuid",
			codexLayoutReceiptTestUUID,
			"live-over-archived dedup guards the stored copy",
		)

		logBuf.Reset()
		assert.Empty(t, r.observe("/dual", mixed))
		assertLogNotContains(t, logBuf, "dual-layout uuid",
			"the same root+uuid pair does not warn again")
	})
}

// TestCodexLayoutReceiptDiscoverDetectsLayoutChange walks a real dated root
// twice through the non-streaming discovery lane, flattens the archive, and
// walks it again: the sink must see exactly one drift record and the log
// must carry the INFO receipt then the change warning.
func TestCodexLayoutReceiptDiscoverDetectsLayoutChange(t *testing.T) {
	root := t.TempDir()
	datedPath := writeDatedCodexRollout(t, root, codexLayoutReceiptTestUUID)

	logBuf := captureLog(t)
	sink := &recordingLayoutSink{}
	sources := newReceiptTestSourceSet(root, sink)

	_, err := sources.Discover(context.Background())
	require.NoError(t, err)
	assertLogContains(t, logBuf, "codex layout receipt", "first observation")
	assert.Empty(t, sink.changes)

	// Stable second pass: silent.
	_, err = sources.Discover(context.Background())
	require.NoError(t, err)
	assert.Empty(t, sink.changes)

	flattenCodexRollout(t, datedPath, root)

	_, err = sources.Discover(context.Background())
	require.NoError(t, err)
	require.Len(t, sink.changes, 1)
	assert.Equal(t, CodexLayoutChange{
		Agent: AgentCodex,
		Root:  root,
		From:  "dated",
		To:    "flat",
	}, sink.changes[0])
	assertLogContains(t, logBuf, "WARNING: codex layout change")
}

// TestCodexLayoutReceiptDiscoverEachDetectsLayoutChange covers the streaming
// discovery lane (reconciliation and raw-capture traversal): the same
// receipt lifecycle must hold when the root is walked source by source.
func TestCodexLayoutReceiptDiscoverEachDetectsLayoutChange(t *testing.T) {
	root := t.TempDir()
	datedPath := writeDatedCodexRollout(t, root, codexLayoutReceiptTestUUID)

	logBuf := captureLog(t)
	sink := &recordingLayoutSink{}
	sources := newReceiptTestSourceSet(root, sink)

	var seen []SourceRef
	require.NoError(t, sources.DiscoverEach(
		context.Background(),
		func(source SourceRef) error {
			seen = append(seen, source)
			return nil
		},
	))
	require.Len(t, seen, 1)
	assertLogContains(t, logBuf, "codex layout receipt", "first observation")
	assert.Empty(t, sink.changes)

	flattenCodexRollout(t, datedPath, root)

	seen = nil
	require.NoError(t, sources.DiscoverEach(
		context.Background(),
		func(source SourceRef) error {
			seen = append(seen, source)
			return nil
		},
	))
	require.Len(t, seen, 1)
	require.Len(t, sink.changes, 1)
	assert.Equal(t, "dated", sink.changes[0].From)
	assert.Equal(t, "flat", sink.changes[0].To)
	assertLogContains(t, logBuf, "WARNING: codex layout change")
}

// TestCodexLayoutReceiptDualUUIDViaDiscovery plants one session UUID in both
// layouts under one root — the actual fork-history hazard shape — and pins
// that discovery warns and dedups the warning across passes while discovery
// itself still resolves the uuid to one live-over-archived source.
func TestCodexLayoutReceiptDualUUIDViaDiscovery(t *testing.T) {
	root := t.TempDir()
	datedPath := writeDatedCodexRollout(t, root, codexLayoutReceiptTestUUID)
	flatPath := filepath.Join(root, codexRolloutName(codexLayoutReceiptTestUUID))
	require.NoError(t, os.WriteFile(flatPath, []byte("{}\n"), 0o600))

	logBuf := captureLog(t)
	sink := &recordingLayoutSink{}
	sources := newReceiptTestSourceSet(root, sink)

	sourcesDiscovered, err := sources.Discover(context.Background())
	require.NoError(t, err)
	require.Len(t, sourcesDiscovered, 1,
		"the same uuid in both layouts resolves to one source")
	assert.Equal(t, datedPath, sourcesDiscovered[0].DisplayPath,
		"discovery prefers the live dated copy")
	assertLogContains(t, logBuf, "WARNING: codex dual-layout uuid")
	assert.Empty(t, sink.changes,
		"a stable mixed root is not layout drift")

	// The second pass re-observes the same mixed layout set: no new warning.
	warnCount := strings.Count(logBuf.String(), "WARNING: codex dual-layout uuid")
	require.Equal(t, 1, warnCount)
	_, err = sources.Discover(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1,
		strings.Count(logBuf.String(), "WARNING: codex dual-layout uuid"),
		"the dual-layout warning dedups per root and uuid")
}
