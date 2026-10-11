package pipevents

import (
	"bufio"
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
)

// RecordingFormatVersion is the JSONL recording format version
// written by WriteRecording and accepted by ReadRecording.
const RecordingFormatVersion = 1

// maxRecordingLineSize bounds one JSONL line in a recording file.
// It matches the parser's session-line cap.
const maxRecordingLineSize = 64 * 1024 * 1024

// RecordingHeader is line 1 of a recording file.
type RecordingHeader struct {
	Kind          string `json:"kind"` // always "header"
	FormatVersion int    `json:"format_version"`
	RunID         string `json:"run_id"`
	ScenarioID    string `json:"scenario_id,omitempty"`
	StartedAtMS   int64  `json:"started_at_ms"`
	SourceFile    string `json:"source_file,omitempty"`
	SourceSHA256  string `json:"source_sha256,omitempty"`
}

// WriteRecording serializes header + events as JSONL to w.
func WriteRecording(
	w io.Writer, h RecordingHeader, events []Event,
) error {
	h.Kind = "header"
	if err := writeLine(w, h); err != nil {
		return err
	}
	for i := range events {
		if err := writeLine(w, events[i]); err != nil {
			return err
		}
	}
	return nil
}

// ReadRecording parses a recording file back.
func ReadRecording(
	r io.Reader,
) (RecordingHeader, []Event, error) {
	var h RecordingHeader
	var events []Event
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), maxRecordingLineSize)
	gotHeader := false
	line := 0
	for s.Scan() {
		line++
		raw := s.Bytes()
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		if !gotHeader {
			gotHeader = true
			if err := json.Unmarshal(raw, &h); err != nil {
				return h, nil, fmt.Errorf(
					"pipevents: decode header: %w", err,
				)
			}
			if h.Kind != "header" {
				return h, nil, fmt.Errorf(
					"pipevents: line 1 kind %q, want \"header\"",
					h.Kind,
				)
			}
			if h.FormatVersion != RecordingFormatVersion {
				return h, nil, fmt.Errorf(
					"pipevents: unsupported format_version %d",
					h.FormatVersion,
				)
			}
			continue
		}
		var e Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return h, nil, fmt.Errorf(
				"pipevents: decode event at line %d: %w", line, err,
			)
		}
		events = append(events, e)
	}
	if err := s.Err(); err != nil {
		return h, nil, fmt.Errorf("pipevents: read: %w", err)
	}
	if !gotHeader {
		return h, nil, errors.New("pipevents: empty recording")
	}
	return h, events, nil
}

func writeLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
