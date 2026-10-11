package pipeline

import (
	"context"
	"time"

	"go.kenn.io/agentsview/internal/parser"
)

// Session UUIDs and rollout destinations shared by the built-in
// scenarios and their tests. Every fixture's session_meta id matches
// the UUID embedded in its destination filename — discovery extracts
// the session id from the name (rollout-*-<uuid>.jsonl), so the two
// must agree.
const (
	uuidHappy     = "a0a0a0a0-1111-4222-8333-444444444444"
	uuidAppend    = "b1b1b1b1-2222-4333-8444-555555555555"
	uuidMalformed = "c2c2c2c2-3333-4444-8555-666666666666"
	uuidCold      = "d3d3d3d3-4444-4555-8666-777777777777"

	// rel* are the dated codex layouts under SrcDir each fixture
	// lands at: <year>/<month>/<day>/rollout-<ts>-<uuid>.jsonl.
	relHappy = "2024/01/01/" +
		"rollout-2024-01-01T10-00-00-" + uuidHappy + ".jsonl"
	relAppend = "2024/01/02/" +
		"rollout-2024-01-02T09-30-00-" + uuidAppend + ".jsonl"
	relMalformed = "2024/01/03/" +
		"rollout-2024-01-03T11-15-00-" + uuidMalformed + ".jsonl"
	relCold = "2024/01/04/" +
		"rollout-2024-01-04T14-45-00-" + uuidCold + ".jsonl"

	// indexTitle is the thread name the session-index.jsonl fixture
	// assigns to uuidHappy in the title-change scenario.
	indexTitle = "Renamed: happy-path thread"
)

// Step builders keep the registry declarative: one short call per
// scripted action, all emitting their scenario_step marker through
// the matching RunContext helper.

func stepCopyFixture(name, rel string) Step {
	return func(ctx context.Context, rc *RunContext) error {
		return rc.CopyFixture(name, rel)
	}
}

func stepAppendFixture(name, rel string) Step {
	return func(ctx context.Context, rc *RunContext) error {
		return rc.AppendFixture(name, rel)
	}
}

func stepCopyMetaFixture(name, metaName string) Step {
	return func(ctx context.Context, rc *RunContext) error {
		return rc.CopyMetaFixture(name, metaName)
	}
}

func stepTouch(rel string) Step {
	return func(ctx context.Context, rc *RunContext) error {
		return rc.Touch(rel)
	}
}

func stepRemove(rel string) Step {
	return func(ctx context.Context, rc *RunContext) error {
		return rc.RemoveFile(rel)
	}
}

func stepWaitIdle(quiet time.Duration) Step {
	return func(ctx context.Context, rc *RunContext) error {
		return rc.WaitIdle(quiet)
	}
}

// define builds one registry entry and appends the trailing idle
// wait every scenario needs: post-EndRun events are dropped, and the
// watcher's delivery latency means a run that ended on its last file
// action would lose the ingest that action caused. Callers therefore
// never write the final WaitIdle themselves.
func define(id, title, summary, fixture string, steps ...Step) Scenario {
	return Scenario{
		ID:      id,
		Title:   title,
		Summary: summary,
		Fixture: fixture,
		Steps:   append(steps, stepWaitIdle(0)),
	}
}

// Scenarios returns the built-in scripted ingest runs, in registry
// order. Each teaches one pipeline behavior: a gate verdict, a parse
// mode, or a write-stage trace the playground can point at.
func Scenarios() []Scenario {
	return []Scenario{
		define(
			"happy-path",
			"Cold ingest",
			"A complete rollout is dropped into sessions/ and travels "+
				"watch, gates, a full parse, and the archive write.",
			"rollout-happy.jsonl",
			stepCopyFixture("rollout-happy.jsonl", relHappy),
		),
		define(
			"live-append",
			"Live append",
			"A rollout grows mid-run: the checkpoint gate proves a "+
				"clean append and only the new tail parses "+
				"incrementally.",
			"rollout-append-a.jsonl",
			stepCopyFixture("rollout-append-a.jsonl", relAppend),
			stepWaitIdle(0),
			stepAppendFixture("rollout-append-b.jsonl", relAppend),
		),
		define(
			"reingest-unchanged",
			"Re-ingest unchanged",
			"The same bytes get a fresh mtime; a touch cannot be told "+
				"from a same-size rewrite by stat, so every gate "+
				"declines and the file re-parses to re-verify "+
				"content.",
			"rollout-happy.jsonl",
			stepCopyFixture("rollout-happy.jsonl", relHappy),
			stepWaitIdle(0),
			stepTouch(relHappy),
		),
		define(
			"malformed-line",
			"Malformed line",
			"A rollout carrying a non-JSON line: the parse classifies "+
				"the bad line and still commits the session.",
			"rollout-malformed.jsonl",
			stepCopyFixture("rollout-malformed.jsonl", relMalformed),
		),
		define(
			"delete-file",
			"Delete file",
			"A synced rollout disappears; the watcher path proves the "+
				"source gone and tombstones its session.",
			"rollout-happy.jsonl",
			stepCopyFixture("rollout-happy.jsonl", relHappy),
			stepWaitIdle(0),
			stepRemove(relHappy),
		),
		define(
			"truncate-rewrite",
			"Truncate rewrite",
			"A longer rollout is replaced by a shorter rewrite: the "+
				"checkpoint proof fails and the session re-parses "+
				"in full.",
			"rollout-append-full.jsonl",
			stepCopyFixture("rollout-append-full.jsonl", relAppend),
			stepWaitIdle(0),
			stepCopyFixture("rollout-truncated.jsonl", relAppend),
		),
		define(
			"title-change",
			"Title change",
			"session_index.jsonl appears beside sessions/ with a new "+
				"thread name; the sidecar event breaks the freshness "+
				"digest and the session re-reads with its new title.",
			"rollout-happy.jsonl",
			stepCopyFixture("rollout-happy.jsonl", relHappy),
			stepWaitIdle(0),
			stepCopyMetaFixture(
				"session-index.jsonl",
				parser.CodexSessionIndexFilename),
		),
		define(
			"cold-archive",
			"Cold archive",
			"Three rollouts land in one drop: batching groups them "+
				"and the flush writes every session.",
			"rollout-happy.jsonl",
			stepCopyFixture("rollout-happy.jsonl", relHappy),
			stepCopyFixture("rollout-append-full.jsonl", relAppend),
			stepCopyFixture("rollout-extra.jsonl", relCold),
		),
	}
}
