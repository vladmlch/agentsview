package pipeline

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.kenn.io/agentsview/internal/pipevents"
)

const (
	// defaultIdleQuiet is the quiet window a WaitIdle step applies
	// when the scenario passes a non-positive duration.
	defaultIdleQuiet = 500 * time.Millisecond

	// idleDeliveryFloor is the minimum time a WaitIdle step waits
	// before it may report idle. On macOS the FSEvents stream
	// coalesces delivery at darwinFSEventsLatency (500 ms) before
	// the watcher's batch delay and dispatch floor add more, so the
	// step's own filesystem effects legitimately surface ~600 ms+
	// after the write. A quiet window that only counts hub silence
	// would expire while that work is still in flight; the floor
	// holds the window open until delivery had a fair chance.
	// 1.5 s covers the stream latency, the batch delay, dispatch,
	// and the sync pass with slack.
	idleDeliveryFloor = 1500 * time.Millisecond
)

// Scenario is a scripted ingest run against the sandbox: an
// identifier, the presentation text the playground UI shows, and the
// ordered steps the runner executes. Fixture names the scenario's
// primary source artifact and is recorded as the run's source_file in
// the recording header.
//
// Registry scenarios built through define always end on an idle wait:
// the watcher reports post-write events on FSEvents latency, so a run
// that ended on its last file action would close before the ingest it
// caused reached the recording.
type Scenario struct {
	ID      string
	Title   string
	Summary string
	Fixture string
	Steps   []Step
}

// Step is one scripted action in a scenario. It runs against the
// sandbox's RunContext and returns an error to abort the run.
type Step func(ctx context.Context, rc *RunContext) error

// RunContext exposes the sandbox surface a step may touch: the codex
// source tree (SrcDir), the metadata dir beside it (MetaDir, which
// holds session_index.jsonl), and the event hub. The runner stamps
// each helper call as a scenario_step event.
type RunContext struct {
	SrcDir  string
	MetaDir string
	Hub     *Hub

	ctx context.Context
	n   int // index of the Step func currently running
}

// emitStep records the step's action in the active run. It fires
// before the filesystem effect so the recording orders the scripted
// cause ahead of the watch events it produces.
func (rc *RunContext) emitStep(action, path, detail string) {
	payload := map[string]any{"n": rc.n, "action": action}
	if detail != "" {
		payload["detail"] = detail
	}
	rc.Hub.ObserveEvent(pipevents.Event{
		Stage:   pipevents.StageFinalize,
		Kind:    pipevents.KindScenarioStep,
		Path:    path,
		Payload: payload,
	})
}

// WriteFile writes content to rel under SrcDir, creating parent
// directories — a rollout file drop or a rewrite.
func (rc *RunContext) WriteFile(rel, content string) error {
	rc.emitStep("write-file", rel, "")
	path, err := sandboxPath(rc.SrcDir, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("pipeline: step mkdir %s: %w", rel, err)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// AppendFile appends content to rel under SrcDir — the live-append
// ingest path.
func (rc *RunContext) AppendFile(rel, content string) error {
	rc.emitStep("append", rel, "")
	path, err := sandboxPath(rc.SrcDir, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("pipeline: step mkdir %s: %w", rel, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// RemoveFile deletes rel under SrcDir — the tombstone ingest path.
func (rc *RunContext) RemoveFile(rel string) error {
	rc.emitStep("delete", rel, "")
	path, err := sandboxPath(rc.SrcDir, rel)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// Touch bumps the atime and mtime of rel under SrcDir without
// changing its bytes — the re-ingest-unchanged path: the file's stat
// identity moves while the content stays provably identical.
func (rc *RunContext) Touch(rel string) error {
	rc.emitStep("touch", rel, "")
	path, err := sandboxPath(rc.SrcDir, rel)
	if err != nil {
		return err
	}
	now := time.Now()
	return os.Chtimes(path, now, now)
}

// WriteMeta writes content to name under MetaDir — sidecar files such
// as session_index.jsonl that live beside sessions/, not inside it.
// Sandbox-owned files are denied: the archive (sandbox.db and its
// journals), the staged-parse scratch tree, and the sessions tree
// itself (which scenarios must reach through the SrcDir helpers so
// the event attribution stays honest).
func (rc *RunContext) WriteMeta(name, content string) error {
	rc.emitStep("write-meta", name, "")
	path, err := metaPath(rc.MetaDir, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("pipeline: step mkdir %s: %w", name, err)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// fixtureFS carries the rollout and sidecar artifacts scenarios copy
// into the sandbox. Files live under testdata/ so the go tool keeps
// them out of the compiled package's file list.
//
//go:embed testdata
var fixtureFS embed.FS

// fixtureBytes reads an embedded fixture by bare file name. Any name
// carrying a separator or traversal is rejected so a scenario can
// never read outside testdata/.
func fixtureBytes(name string) ([]byte, error) {
	if name == "" || name != filepath.Base(name) ||
		strings.Contains(name, "..") {
		return nil, fmt.Errorf(
			"pipeline: unsafe fixture name %q", name)
	}
	data, err := fixtureFS.ReadFile("testdata/" + name)
	if err != nil {
		return nil, fmt.Errorf(
			"pipeline: read fixture %s: %w", name, err)
	}
	return data, nil
}

// CopyFixture writes the embedded fixture name to rel under SrcDir —
// a file drop shaped like a codex rollout appearing on disk.
func (rc *RunContext) CopyFixture(name, rel string) error {
	rc.emitStep("copy-fixture", rel, name)
	content, err := fixtureBytes(name)
	if err != nil {
		return err
	}
	path, err := sandboxPath(rc.SrcDir, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("pipeline: step mkdir %s: %w", rel, err)
	}
	return os.WriteFile(path, content, 0o644)
}

// AppendFixture appends the embedded fixture name to rel under
// SrcDir — a rollout growth chunk, exercising the append gate and
// incremental parse path.
func (rc *RunContext) AppendFixture(name, rel string) error {
	rc.emitStep("append-fixture", rel, name)
	content, err := fixtureBytes(name)
	if err != nil {
		return err
	}
	path, err := sandboxPath(rc.SrcDir, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("pipeline: step mkdir %s: %w", rel, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// CopyMetaFixture writes the embedded fixture name to metaName under
// MetaDir — a sidecar drop such as an updated session_index.jsonl.
func (rc *RunContext) CopyMetaFixture(name, metaName string) error {
	rc.emitStep("copy-meta-fixture", metaName, name)
	content, err := fixtureBytes(name)
	if err != nil {
		return err
	}
	path, err := metaPath(rc.MetaDir, metaName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf(
			"pipeline: step mkdir %s: %w", metaName, err)
	}
	return os.WriteFile(path, content, 0o644)
}

// WaitIdle blocks until the hub observes no new events for quiet, or
// the run context ends — but never sooner than idleDeliveryFloor,
// which gives the filesystem watcher a fair window to deliver the
// step's own effects. A non-positive quiet uses defaultIdleQuiet.
// The subscription opens before the window starts, so work queued in
// the watcher but not yet dispatched still resets the timer.
func (rc *RunContext) WaitIdle(quiet time.Duration) error {
	if quiet <= 0 {
		quiet = defaultIdleQuiet
	}
	rc.emitStep("wait-idle", "", quiet.String())
	ctx := rc.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// Subscribing at the max seq skips the buffered backlog: only
	// events that arrive after this call count against the window.
	ch, unsub := rc.Hub.Subscribe(math.MaxUint64)
	defer unsub()
	notBefore := time.Now().Add(max(quiet, idleDeliveryFloor))
	timer := time.NewTimer(quiet)
	defer timer.Stop()
	reset := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(quiet)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			reset()
		case <-timer.C:
			// A pending event counts as activity even when the
			// timer branch won the select draw.
			select {
			case _, ok := <-ch:
				if !ok {
					return nil
				}
				reset()
				continue
			default:
			}
			if wait := time.Until(notBefore); wait > 0 {
				timer.Reset(wait)
				continue
			}
			return nil
		}
	}
}

// sandboxPath resolves rel under base, rejecting absolute paths and
// parent traversal outside the sandbox.
func sandboxPath(base, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("pipeline: empty scenario path")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf(
			"pipeline: absolute scenario path %q not allowed", rel)
	}
	p := filepath.Join(base, rel)
	if p != base && !strings.HasPrefix(
		p, base+string(filepath.Separator)) {
		return "", fmt.Errorf(
			"pipeline: scenario path %q escapes the sandbox", rel)
	}
	return p, nil
}

// metaPath resolves a sidecar name under MetaDir. On top of the
// sandbox containment rules it rejects the files the sandbox owns
// itself: the SQLite archive and its journals, the staged-parse
// scratch tree, and everything inside sessions/ — a scenario writing
// rollout content through the meta path would bypass SrcDir's
// attribution and confuse the recording.
func metaPath(metaDir, name string) (string, error) {
	p, err := sandboxPath(metaDir, name)
	if err != nil {
		return "", err
	}
	slash := filepath.ToSlash(filepath.Clean(name))
	first, _, _ := strings.Cut(slash, "/")
	if first == sandboxSrcName || first == "scratch" ||
		strings.HasPrefix(filepath.Base(slash), sandboxDBName) {
		return "", fmt.Errorf(
			"pipeline: meta path %q collides with sandbox internals",
			name)
	}
	return p, nil
}
