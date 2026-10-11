package pipevents

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordingRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	h := RecordingHeader{
		Kind: "header", FormatVersion: RecordingFormatVersion,
		RunID: "r1", ScenarioID: "happy-path", StartedAtMS: 123,
	}
	evs := []Event{
		{Seq: 1, AtMS: 100, Stage: StageWatch, Kind: "fs_event",
			Path: "/x/rollout-1.jsonl", Payload: map[string]any{"op": "create"}},
		{Seq: 2, AtMS: 105, Stage: StageGates, Kind: "gate_verdict",
			Payload: map[string]any{"gate": "skip_cache", "decision": "pass"}},
	}
	require.NoError(t, WriteRecording(&buf, h, evs))
	gotH, gotEvs, err := ReadRecording(&buf)
	require.NoError(t, err)
	assert.Equal(t, h.RunID, gotH.RunID)
	require.Len(t, gotEvs, 2)
	assert.Equal(t, StageGates, gotEvs[1].Stage)
	assert.Equal(t, "pass", gotEvs[1].Payload["decision"])
}

func TestReadRecordingRejectsBadVersion(t *testing.T) {
	buf := bytes.NewBufferString(
		`{"kind":"header","format_version":99}` + "\n")
	_, _, err := ReadRecording(buf)
	assert.Error(t, err)
}
