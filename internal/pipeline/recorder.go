package pipeline

import (
	"bufio"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.kenn.io/agentsview/internal/pipevents"
)

// recordingFileExt is the suffix every recording file carries.
const recordingFileExt = ".jsonl"

// Recorder owns the recordings directory. It streams live runs to
// <dir>/<runID>.jsonl for the hub and serves list, read, and import
// over the stored files. An empty dir disables recording entirely:
// open fails, ListRecordings is empty, ReadRecording and
// ImportRecording return an error.
type Recorder struct {
	dir string
}

// NewRecorder creates a Recorder rooted at dir. The directory is
// created on first write, not at construction.
func NewRecorder(dir string) *Recorder { return &Recorder{dir: dir} }

// enabled reports whether the recorder has a directory to write into.
func (r *Recorder) enabled() bool { return r.dir != "" }

// path joins name onto the recordings dir. Callers must have already
// validated name with validRecordingName or built it from a run id
// that passed isSafeRunName.
func (r *Recorder) path(name string) string {
	return filepath.Join(r.dir, name)
}

// open creates <dir>/<runID>.jsonl and writes the recording header as
// line 1, flushed immediately so even a crash before the first event
// leaves a parseable file. It fails when the file already exists so a
// stale recording is never silently truncated.
func (r *Recorder) open(
	runID string, h pipevents.RecordingHeader,
) (*runRecorder, error) {
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return nil, fmt.Errorf(
			"pipeline: create recordings dir: %w", err)
	}
	path := r.path(runID + recordingFileExt)
	f, err := os.OpenFile(
		path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf(
			"pipeline: open recording %s: %w", path, err)
	}
	w := &runRecorder{f: f, w: bufio.NewWriter(f)}
	h.Kind = "header"
	h.FormatVersion = pipevents.RecordingFormatVersion
	if err := w.writeLine(h); err != nil {
		f.Close()
		return nil, fmt.Errorf(
			"pipeline: write recording header: %w", err)
	}
	return w, nil
}

// ListRecordings returns the recording file names in the directory,
// sorted. A disabled or not-yet-created directory yields an empty
// list.
func (r *Recorder) ListRecordings() ([]string, error) {
	if !r.enabled() {
		return nil, nil
	}
	entries, err := os.ReadDir(r.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pipeline: list recordings: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() ||
			!strings.HasSuffix(e.Name(), recordingFileExt) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// ReadRecording parses a stored recording by file name.
func (r *Recorder) ReadRecording(
	name string,
) (pipevents.RecordingHeader, []pipevents.Event, error) {
	var h pipevents.RecordingHeader
	if err := validRecordingName(name); err != nil {
		return h, nil, err
	}
	if !r.enabled() {
		return h, nil, errors.New("pipeline: recordings disabled")
	}
	f, err := os.Open(r.path(name))
	if err != nil {
		return h, nil, fmt.Errorf(
			"pipeline: open recording: %w", err)
	}
	defer f.Close()
	h, evs, err := pipevents.ReadRecording(f)
	if err != nil {
		return h, nil, err
	}
	return h, evs, nil
}

// ReadRecordingFile returns the raw bytes of a stored recording.
// Download endpoints use it instead of ReadRecording so the served
// bytes are the file on disk, not a re-encode of parsed events.
func (r *Recorder) ReadRecordingFile(name string) ([]byte, error) {
	if err := validRecordingName(name); err != nil {
		return nil, err
	}
	if !r.enabled() {
		return nil, errors.New("pipeline: recordings disabled")
	}
	data, err := os.ReadFile(r.path(name))
	if err != nil {
		return nil, fmt.Errorf(
			"pipeline: read recording: %w", err)
	}
	return data, nil
}

// ImportRecording stores an uploaded recording stream under name. The
// stream must parse as a valid recording under pipevents.ReadRecording
// rules; invalid input leaves no file behind. An existing file with
// the same name is replaced atomically. Callers must not target the
// file an in-flight run is writing: the rename would swap the path
// under the run recorder's open descriptor and silently detach the
// rest of the run's events. The recorder cannot see runs — HTTP
// callers guard with Hub.ActiveRun under the session mutex.
func (r *Recorder) ImportRecording(name string, rd io.Reader) error {
	if err := validRecordingName(name); err != nil {
		return err
	}
	if !r.enabled() {
		return errors.New("pipeline: recordings disabled")
	}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return fmt.Errorf(
			"pipeline: create recordings dir: %w", err)
	}
	tmp, err := os.CreateTemp(r.dir, ".import-*.jsonl")
	if err != nil {
		return fmt.Errorf("pipeline: import recording: %w", err)
	}
	tmpPath := tmp.Name()
	// Belt and suspenders: rename removes the temp on success, so a
	// stale temp only survives a crash mid-copy, which Remove cleans.
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, rd); err != nil {
		tmp.Close()
		return fmt.Errorf("pipeline: import recording: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("pipeline: import recording: %w", err)
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("pipeline: import recording: %w", err)
	}
	_, _, err = pipevents.ReadRecording(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("pipeline: import recording: %w", err)
	}
	if err := os.Rename(tmpPath, r.path(name)); err != nil {
		return fmt.Errorf("pipeline: import recording: %w", err)
	}
	return nil
}

// runRecorder streams one run's events to its recording file. Every
// line is flushed to the OS on write, so a crashed process leaves a
// recording readable up to the last accepted event. The first write
// error is sticky: later writes no-op, because the pipevents.Observer
// contract gives the hub no way to report them and a broken recording
// must never stall the pipeline.
type runRecorder struct {
	f   *os.File
	w   *bufio.Writer
	err error
}

// writeEvent appends one event as a JSONL line and flushes it.
func (w *runRecorder) writeEvent(e pipevents.Event) {
	if w.err == nil {
		w.err = w.writeLine(e)
	}
}

// writeLine serializes v as one JSONL line and flushes it,
// mirroring the encoding pipevents.WriteRecording produces.
func (w *runRecorder) writeLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return w.w.Flush()
}

// close flushes and closes the file, recording the first error seen.
func (w *runRecorder) close() {
	if err := w.w.Flush(); err != nil && w.err == nil {
		w.err = err
	}
	if err := w.f.Close(); err != nil && w.err == nil {
		w.err = err
	}
}

// validRecordingName rejects names that could escape the recordings
// directory or that are not recording files. It accepts plain
// "*.jsonl" base names only.
func validRecordingName(name string) error {
	if name == "" || name == "." || name == ".." ||
		filepath.Base(name) != name ||
		strings.ContainsAny(name, `/\`) {
		return fmt.Errorf(
			"pipeline: unsafe recording name %q", name)
	}
	if !strings.HasSuffix(name, recordingFileExt) ||
		len(name) == len(recordingFileExt) || name[0] == '.' {
		return fmt.Errorf(
			"pipeline: recording name %q must be a *%s file",
			name, recordingFileExt)
	}
	return nil
}
