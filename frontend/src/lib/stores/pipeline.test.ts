import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { EventSource } from "eventsource";
import type { WatchPipelineEventsOptions } from "../api/client.js";
import {
  eventAnomaly,
  parsePipelineRecording,
  PipelineStore,
  reduceEvents,
  type PipelineEvent,
  type PipelineStage,
} from "./pipeline.svelte.js";

let seq = 0;
function ev(
  stage: PipelineStage,
  kind: string,
  payload?: Record<string, unknown>,
  path?: string,
): PipelineEvent {
  seq += 1;
  return { seq, at_ms: 1_000 + seq, stage, kind, path, payload };
}

beforeEach(() => {
  seq = 0;
});

describe("reduceEvents", () => {
  it("returns an empty view for no events", () => {
    const s = reduceEvents([]);
    expect(s.fileLines).toEqual([]);
    expect(s.parsedEvents).toEqual([]);
    expect(s.gates).toEqual([]);
    expect(s.dbDiff).toEqual([]);
    expect(s.ftsRows).toBe(0);
    expect(s.cursor).toBeNull();
    expect(s.queue).toEqual({ pending: 0, bytes: 0 });
    expect(s.activeStage).toBeNull();
    expect(s.stageStatus.watch).toBe("idle");
    expect(s.run.finished).toBe(false);
  });

  it("collects every event into parsedEvents and tracks the last stage", () => {
    const events = [
      ev("watch", "batch_formed", { paths: 1 }),
      ev("resolve", "path_classified", { classification: "session" }),
    ];
    const s = reduceEvents(events);
    expect(s.parsedEvents).toHaveLength(2);
    expect(s.activeStage).toBe("resolve");
  });

  it("marks an earlier stage done once a later stage activates", () => {
    const s = reduceEvents([
      ev("watch", "batch_formed"),
      ev("resolve", "path_classified"),
      ev("parse", "parse_started", { mode: "full" }, "f.jsonl"),
    ]);
    expect(s.stageStatus.watch).toBe("done");
    expect(s.stageStatus.resolve).toBe("done");
    expect(s.stageStatus.parse).toBe("active");
    expect(s.stageStatus.write).toBe("idle");
  });

  it("re-activates a stage when it emits again after completing", () => {
    const s = reduceEvents([
      ev("parse", "parse_started", { mode: "full" }, "f.jsonl"),
      ev("parse", "parse_done", { messages: 1 }, "f.jsonl"),
      ev("write", "tx_started", { sessions: 1 }),
      ev("parse", "parse_started", { mode: "full" }, "g.jsonl"),
    ]);
    expect(s.stageStatus.parse).toBe("active");
    expect(s.stageStatus.write).toBe("done");
  });

  it("reconstructs file content from line_classified raw lines", () => {
    const s = reduceEvents([
      ev("parse", "parse_started", { mode: "full" }, "f.jsonl"),
      ev(
        "parse",
        "line_classified",
        { line_no: 1, offset: 40, type: "session_meta", raw: `{"type":"session_meta"}` },
        "f.jsonl",
      ),
      ev(
        "parse",
        "line_classified",
        { line_no: 2, offset: 80, type: "response_item", raw: `{"type":"response_item"}` },
        "f.jsonl",
      ),
      ev(
        "parse",
        "line_classified",
        { line_no: 3, offset: 90, type: "malformed", raw: `not json` },
        "f.jsonl",
      ),
    ]);
    expect(s.fileLines).toHaveLength(3);
    expect(s.fileLines[0]).toMatchObject({ no: 1, parsed: true, path: "f.jsonl" });
    expect(s.fileLines[2]?.parsed).toBe(false);
    // The per-path reconstruction numbers real file lines.
    expect(s.files["f.jsonl"]).toHaveLength(3);
    expect(s.files["f.jsonl"]?.[2]).toMatchObject({ no: 3, parsed: false });
  });

  it("resets a path's reconstructed content on a full re-parse", () => {
    const s = reduceEvents([
      ev("parse", "parse_started", { mode: "full" }, "f.jsonl"),
      ev("parse", "line_classified", { line_no: 1, type: "a", raw: "one" }, "f.jsonl"),
      ev("parse", "parse_started", { mode: "full" }, "f.jsonl"),
      ev("parse", "line_classified", { line_no: 1, type: "a", raw: "two" }, "f.jsonl"),
    ]);
    expect(s.fileLines).toHaveLength(2); // transcript keeps both parses
    expect(s.files["f.jsonl"]).toEqual([
      { no: 1, raw: "two", parsed: true, type: "a", truncated: false, offset: -1 },
    ]);
  });

  it("keeps reconstructed content on an incremental parse", () => {
    const s = reduceEvents([
      ev("parse", "parse_started", { mode: "full" }, "f.jsonl"),
      ev("parse", "line_classified", { line_no: 1, type: "a", raw: "one" }, "f.jsonl"),
      ev("parse", "parse_started", { mode: "incremental", offset: 40 }, "f.jsonl"),
      // line_no restarts at 1 inside an incremental pass.
      ev("parse", "line_classified", { line_no: 1, type: "b", raw: "two" }, "f.jsonl"),
    ]);
    expect(s.files["f.jsonl"]?.map((l) => l.raw)).toEqual(["one", "two"]);
    expect(s.files["f.jsonl"]?.[1]?.no).toBe(2);
  });

  it("replaces in-pass re-classified lines on the JSON-stream recovery pass", () => {
    // codex.go: a snapshot with zero valid JSONL records but ≥1
    // terminated malformed line seeks back to 0 and re-classifies the
    // same bytes through json.Decoder — physical lines first
    // (line_no 1..N, type malformed), then decoded records with
    // line_no restarting at 1. The reconstruction must not show both.
    const s = reduceEvents([
      ev("parse", "parse_started", { mode: "full", offset: 0, size: 24 }, "f.json"),
      ev(
        "parse",
        "line_classified",
        { line_no: 1, offset: 8, type: "malformed", raw: `{"a":1,` },
        "f.json",
      ),
      ev(
        "parse",
        "line_classified",
        { line_no: 2, offset: 17, type: "malformed", raw: `"b":2},` },
        "f.json",
      ),
      ev(
        "parse",
        "line_classified",
        { line_no: 3, offset: 24, type: "malformed", raw: `{"c":3}` },
        "f.json",
      ),
      // Recovery pass: same bytes, record segmentation, line_no back to 1.
      ev(
        "parse",
        "line_classified",
        { line_no: 1, offset: 15, type: "message", raw: `{"a":1,"b":2}` },
        "f.json",
      ),
      ev(
        "parse",
        "line_classified",
        { line_no: 2, offset: 24, type: "message", raw: `{"c":3}` },
        "f.json",
      ),
    ]);
    expect(s.files["f.json"]?.map((l) => l.raw)).toEqual([`{"a":1,"b":2}`, `{"c":3}`]);
    // The transcript still preserves both passes — the parser did
    // read the bytes twice.
    expect(s.fileLines).toHaveLength(5);
  });

  it("does not truncate earlier content when a new pass restarts line_no", () => {
    // A second full parse is a new pass, not an in-pass re-read.
    const s = reduceEvents([
      ev("parse", "parse_started", { mode: "full" }, "f.jsonl"),
      ev("parse", "line_classified", { line_no: 1, type: "a", raw: "one" }, "f.jsonl"),
      ev("parse", "line_classified", { line_no: 2, type: "a", raw: "two" }, "f.jsonl"),
      ev("parse", "parse_started", { mode: "incremental", offset: 40 }, "f.jsonl"),
      ev("parse", "line_classified", { line_no: 1, type: "b", raw: "three" }, "f.jsonl"),
    ]);
    expect(s.files["f.jsonl"]?.map((l) => l.raw)).toEqual(["one", "two", "three"]);
  });

  it("clears reconstructed content when the scenario deletes the file", () => {
    // scenario_step paths are sandbox-relative while files keys are
    // absolute — the clear matches on a path-boundary suffix.
    const s = reduceEvents([
      ev("parse", "parse_started", { mode: "full" }, "/tmp/sandbox/src/f.jsonl"),
      ev(
        "parse",
        "line_classified",
        { line_no: 1, type: "a", raw: "x" },
        "/tmp/sandbox/src/f.jsonl",
      ),
      ev("parse", "parse_started", { mode: "full" }, "/tmp/sandbox/src/other.jsonl"),
      ev(
        "parse",
        "line_classified",
        { line_no: 1, type: "a", raw: "y" },
        "/tmp/sandbox/src/other.jsonl",
      ),
      ev("finalize", "scenario_step", { n: 1, action: "delete" }, "f.jsonl"),
    ]);
    expect(s.files["/tmp/sandbox/src/f.jsonl"]).toBeUndefined();
    expect(s.files["/tmp/sandbox/src/other.jsonl"]).toHaveLength(1);
  });

  it("appends gate verdicts with their path", () => {
    const s = reduceEvents([
      ev(
        "gates",
        "gate_verdict",
        { gate: "checkpoint", decision: "append", reason: "clean append" },
        "f.jsonl",
      ),
    ]);
    expect(s.gates).toEqual([
      {
        gate: "checkpoint",
        decision: "append",
        reason: "clean append",
        path: "f.jsonl",
      },
    ]);
  });

  it("accumulates db writes from messages_written and tx_committed", () => {
    const s = reduceEvents([
      ev(
        "write",
        "messages_written",
        { session_id: "s1", inserted: 4, updated: 2, max_ordinal: 4 },
        "f.jsonl",
      ),
      ev("write", "tx_committed", { sessions: 1, inserted: 4, failed: 0 }),
    ]);
    expect(s.dbDiff).toEqual([
      { session_id: "s1", inserted: 4, updated: 2 },
      { session_id: undefined, inserted: 4, updated: 0, sessions: 1, failed: 0 },
    ]);
  });

  it("sums fts_rows into ftsRows", () => {
    const s = reduceEvents([
      ev("write", "fts_updated", { session_id: "s1", fts_rows: 4 }, "f.jsonl"),
      ev("write", "fts_updated", { session_id: "s2", fts_rows: 3 }, "g.jsonl"),
    ]);
    expect(s.ftsRows).toBe(7);
  });

  it("replaces the cursor on cursor_updated and keeps its path", () => {
    const s = reduceEvents([
      ev("parse", "cursor_updated", { offset: 40, pending_calls: 1, next_ordinal: 2 }, "f.jsonl"),
      ev("parse", "cursor_updated", { offset: 80, pending_calls: 0, next_ordinal: 3 }, "f.jsonl"),
    ]);
    expect(s.cursor).toMatchObject({ offset: 80, next_ordinal: 3, path: "f.jsonl" });
  });

  it("tracks pending depth and drains the queue on flush_triggered", () => {
    const s = reduceEvents([
      ev("batch", "pending_accumulated", { pending_count: 2, pending_bytes: 100 }, "f.jsonl"),
      ev("batch", "pending_accumulated", { pending_count: 3, pending_bytes: 160 }, "g.jsonl"),
      ev("batch", "flush_triggered", {
        reason: "all_sources_done",
        pending_count: 3,
        pending_bytes: 160,
      }),
    ]);
    expect(s.queue).toEqual({ pending: 0, bytes: 0 });
    expect(s.lastFlush).toEqual({
      reason: "all_sources_done",
      pending: 3,
      bytes: 160,
    });
  });

  it("records scenario steps and run bookends", () => {
    const started = ev("finalize", "run_started", { scenario_id: "append" });
    started.run_id = "run-1";
    const finished = ev("finalize", "run_finished", { synced: 1 });
    finished.run_id = "run-1";
    const s = reduceEvents([
      started,
      ev("finalize", "scenario_step", { n: 0, action: "write-file" }, "f.jsonl"),
      ev("finalize", "scenario_step", { n: 1, action: "append" }, "f.jsonl"),
      finished,
    ]);
    expect(s.steps).toEqual([
      { n: 0, action: "write-file", path: "f.jsonl", detail: undefined },
      { n: 1, action: "append", path: "f.jsonl", detail: undefined },
    ]);
    expect(s.run).toMatchObject({
      runId: "run-1",
      scenarioId: "append",
      finished: true,
    });
    // run_finished closes every stage for the diagram.
    for (const status of Object.values(s.stageStatus)) {
      expect(status).not.toBe("active");
    }
  });

  it("tolerates unknown kinds and missing payloads", () => {
    const s = reduceEvents([
      ev("embed", "embed_wat"),
      { seq: 99, at_ms: 1, stage: "parse", kind: "line_classified" },
    ]);
    expect(s.parsedEvents).toHaveLength(2);
    expect(s.activeStage).toBe("parse");
    expect(s.fileLines).toHaveLength(1);
    expect(s.fileLines[0]).toMatchObject({ raw: "", parsed: true });
  });

  it("builds a journey per file in first-seen path order", () => {
    const s = reduceEvents([
      ev("resolve", "path_classified", { classification: "session" }, "a.jsonl"),
      ev("parse", "parse_started", { mode: "full", offset: 0, size: 90 }, "a.jsonl"),
      ev("resolve", "path_classified", { classification: "session" }, "b.jsonl"),
      ev(
        "parse",
        "line_classified",
        { line_no: 1, offset: 40, type: "meta", raw: "{}" },
        "a.jsonl",
      ),
      ev("parse", "parse_started", { mode: "full", offset: 0, size: 20 }, "b.jsonl"),
    ]);
    expect(s.journeys.map((j) => j.path)).toEqual(["a.jsonl", "b.jsonl"]);
    const a = s.journeys[0];
    expect(a?.steps.map((st) => st.kind)).toEqual([
      "path_classified",
      "parse_started",
      "line_classified",
    ]);
    // idx is the event's buffer index — interleaved events skip ahead.
    expect(a?.steps.map((st) => st.idx)).toEqual([0, 1, 3]);
    expect(a?.steps[0]?.summary).toBe("classified: session");
    expect(a?.steps[1]?.summary).toBe("parse full offset=0 size=90");
    expect(a?.steps[2]?.summary).toBe("1 line classified");
    expect(s.journeys[1]?.steps.map((st) => st.kind)).toEqual(["path_classified", "parse_started"]);
  });

  it("skips events without a non-empty path", () => {
    const s = reduceEvents([
      ev("watch", "batch_formed", { paths: 2 }),
      ev("write", "tx_started", { sessions: 2, mode: "batch" }),
      ev("write", "tx_committed", { sessions: 2, inserted: 5 }, ""),
    ]);
    expect(s.journeys).toEqual([]);
  });

  it("folds consecutive line/message runs into one step", () => {
    const s = reduceEvents([
      ev("parse", "parse_started", { mode: "full", offset: 0, size: 90 }, "f.jsonl"),
      ev("parse", "line_classified", { line_no: 1, offset: 10, type: "a", raw: "1" }, "f.jsonl"),
      ev(
        "parse",
        "line_classified",
        { line_no: 2, offset: 20, type: "malformed", raw: "x" },
        "f.jsonl",
      ),
      ev("parse", "line_classified", { line_no: 3, offset: 30, type: "b", raw: "3" }, "f.jsonl"),
      ev("parse", "message_emitted", { ordinal: 1 }, "f.jsonl"),
      ev("parse", "message_emitted", { ordinal: 2 }, "f.jsonl"),
      ev("parse", "cursor_updated", { offset: 30, next_ordinal: 3 }, "f.jsonl"),
      ev("parse", "parse_done", { messages: 2, consumed_bytes: 90 }, "f.jsonl"),
    ]);
    const j = s.journeys[0];
    expect(j?.steps.map((st) => st.kind)).toEqual([
      "parse_started",
      "line_classified",
      "message_emitted",
      "cursor_updated",
      "parse_done",
    ]);
    const lines = j?.steps[1];
    // idx stays on the run's first event so seek/select land at its start.
    expect(lines?.idx).toBe(1);
    expect(lines?.folded).toBe(3);
    expect(lines?.summary).toBe("3 lines classified, 1 malformed");
    expect(lines?.anomaly).toBe("malformed line");
    expect(j?.steps[2]?.folded).toBe(2);
    expect(j?.steps[2]?.summary).toBe("2 messages emitted");
    // A lone cursor_updated keeps its detailed summary.
    expect(j?.steps[3]?.folded).toBe(1);
    expect(j?.steps[3]?.summary).toBe("cursor @30 ord=3");
    // Parser-level parse_done carries no mode — the summary omits it.
    expect(j?.steps[4]?.summary).toBe("done: 2 msgs, 90 B");
    // Only the malformed line counted as an anomaly.
    expect(j?.anomalies).toBe(1);
  });

  it("summarizes a cursor_updated run as a count", () => {
    const s = reduceEvents([
      ev("parse", "cursor_updated", { offset: 10, next_ordinal: 1 }, "f.jsonl"),
      ev("parse", "cursor_updated", { offset: 20, next_ordinal: 2 }, "f.jsonl"),
      ev("parse", "cursor_updated", { offset: 30, next_ordinal: 3 }, "f.jsonl"),
    ]);
    const st = s.journeys[0]?.steps[0];
    expect(st?.folded).toBe(3);
    expect(st?.summary).toBe("3 cursor updates");
  });

  it("continues one journey across repeated parse passes", () => {
    const s = reduceEvents([
      ev("parse", "parse_started", { mode: "full", offset: 0, size: 20 }, "f.jsonl"),
      ev("parse", "line_classified", { line_no: 1, offset: 20, type: "a", raw: "1" }, "f.jsonl"),
      ev("parse", "parse_done", { messages: 1, consumed_bytes: 20 }, "f.jsonl"),
      ev("parse", "parse_started", { mode: "incremental", offset: 20, size: 40 }, "f.jsonl"),
      ev("parse", "line_classified", { line_no: 1, offset: 40, type: "b", raw: "2" }, "f.jsonl"),
      ev("parse", "parse_done", { mode: "incremental", messages: 1, consumed: 40 }, "f.jsonl"),
    ]);
    expect(s.journeys).toHaveLength(1);
    const j = s.journeys[0];
    expect(j?.steps.map((st) => st.kind)).toEqual([
      "parse_started",
      "line_classified",
      "parse_done",
      "parse_started",
      "line_classified",
      "parse_done",
    ]);
    expect(j?.steps[3]?.summary).toBe("parse incremental offset=20 size=40");
    // Engine-level parse_done: `consumed`/`messages` still extract.
    expect(j?.steps[5]?.summary).toBe("done incremental: 1 msgs, 40 B");
    expect(j?.byteMap).toEqual({ size: 40, readEnd: 40, checkpoint: null, incStart: 20 });
  });

  it("tracks the write outcome and byte map per path", () => {
    const s = reduceEvents([
      ev("parse", "parse_started", { mode: "full", offset: 0, size: 90 }, "f.jsonl"),
      ev("parse", "line_classified", { line_no: 1, offset: 40, type: "a", raw: "1" }, "f.jsonl"),
      ev("parse", "line_classified", { line_no: 2, offset: 80, type: "b", raw: "2" }, "f.jsonl"),
      ev("parse", "parse_done", { messages: 2, consumed_bytes: 80 }, "f.jsonl"),
      ev("write", "session_written", { session_id: "s1" }, "f.jsonl"),
      ev("write", "messages_written", { session_id: "s1", inserted: 2, max_ordinal: 2 }, "f.jsonl"),
      ev(
        "write",
        "checkpoint_written",
        { session_id: "s1", checkpoint_version: 7, offset: 80 },
        "f.jsonl",
      ),
      ev("write", "messages_written", { session_id: "s1", inserted: 1, max_ordinal: 3 }, "f.jsonl"),
    ]);
    const j = s.journeys[0];
    expect(j?.byteMap).toEqual({ size: 90, readEnd: 80, checkpoint: 80, incStart: null });
    // inserted sums every messages_written for the path.
    expect(j?.outcome).toEqual({ sessionId: "s1", inserted: 3, checkpointVersion: 7 });
    expect(j?.steps.map((st) => st.summary)).toContain("session s1");
    expect(j?.steps.map((st) => st.summary)).toContain("2 msgs (max ord 2)");
    expect(j?.steps.map((st) => st.summary)).toContain("checkpoint v7 @80");
  });

  it("falls back to parse_done's consumed count when offsets are untracked", () => {
    // The generic incremental tail reader reports offset -1 — readEnd
    // then comes from parse_done instead of line offsets.
    const s = reduceEvents([
      ev("parse", "parse_started", { mode: "incremental", offset: 10 }, "f.jsonl"),
      ev("parse", "line_classified", { line_no: 1, offset: -1, type: "a", raw: "1" }, "f.jsonl"),
      ev("parse", "parse_done", { mode: "incremental", messages: 1, consumed: 55 }, "f.jsonl"),
    ]);
    expect(s.journeys[0]?.byteMap).toEqual({
      size: 0,
      readEnd: 55,
      checkpoint: null,
      incStart: 10,
    });
  });

  it("reads engine-level parse_started size from source_bytes", () => {
    const s = reduceEvents([
      ev("parse", "parse_started", { mode: "collect", source_bytes: 500 }, "f.jsonl"),
      ev("parse", "parse_done", { mode: "collect", results: 1, source_errors: 0 }, "f.jsonl"),
    ]);
    expect(s.journeys[0]?.byteMap.size).toBe(500);
    expect(s.journeys[0]?.steps[1]?.summary).toBe("done collect: 1 msgs, 0 B");
  });

  it("marks anomalous journey steps with the eventAnomaly reason", () => {
    const s = reduceEvents([
      ev(
        "gates",
        "gate_verdict",
        {
          gate: "codex_checkpoint",
          decision: "invalid",
          reason: "checkpoint proof invalid",
        },
        "f.jsonl",
      ),
      ev("parse", "parse_started", { mode: "full" }, "f.jsonl"),
    ]);
    const j = s.journeys[0];
    expect(j?.anomalies).toBe(1);
    expect(j?.steps[0]?.anomaly).toBe('gate "codex_checkpoint": invalid');
    expect(j?.steps[0]?.summary).toBe("codex_checkpoint → invalid (checkpoint proof invalid)");
    expect(j?.steps[1]?.anomaly).toBeUndefined();
  });

  it("labels kinds without a detail summary by kind", () => {
    const s = reduceEvents([
      ev("finalize", "scenario_step", { n: 1, action: "delete" }, "f.jsonl"),
    ]);
    expect(s.journeys[0]?.steps[0]?.summary).toBe("scenario_step");
  });
});

describe("eventAnomaly", () => {
  it("flags adverse gate verdicts", () => {
    // "invalid" is really emitted — the codex_checkpoint gate reports
    // it when the checkpoint proof fails (truncate-rewrite). The rest
    // are forward-compat vocabulary no producer emits today.
    for (const decision of ["reject", "drop", "rescan", "invalid"]) {
      expect(eventAnomaly(ev("gates", "gate_verdict", { gate: "g", decision }))).toBe(
        `gate "g": ${decision}`,
      );
    }
  });

  it("passes routine gate verdicts", () => {
    // The emitted routine vocabulary: "pass"/"skip" from gateVerdict
    // call sites, "unchanged"/"append"/"missing" from the
    // codex_checkpoint gate.
    for (const decision of ["pass", "skip", "unchanged", "append", "missing"]) {
      expect(eventAnomaly(ev("gates", "gate_verdict", { gate: "g", decision }))).toBeNull();
    }
  });

  it("flags the dedup loser with the winner's basename", () => {
    expect(
      eventAnomaly(
        ev("resolve", "dedup_loser", {
          winner: "/home/u/logs/winner.jsonl",
          loser: "/tmp/l.jsonl",
        }),
      ),
    ).toBe("dedup lost to winner.jsonl");
  });

  it("flags incremental_fallback with its reason", () => {
    expect(
      eventAnomaly(ev("parse", "incremental_fallback", { reason: "line_no regression" })),
    ).toBe("line_no regression");
  });

  it("flags parse_done and embed_done failures via payload.error", () => {
    expect(eventAnomaly(ev("parse", "parse_done", { messages: 0, error: "boom" }))).toBe("boom");
    expect(eventAnomaly(ev("embed", "embed_done", { error: "ollama down" }))).toBe("ollama down");
    expect(eventAnomaly(ev("parse", "parse_done", { messages: 3 }))).toBeNull();
    expect(eventAnomaly(ev("embed", "embed_done", { store: "pg" }))).toBeNull();
  });

  it("flags tx_committed with failed sessions", () => {
    expect(eventAnomaly(ev("write", "tx_committed", { sessions: 3, inserted: 9, failed: 2 }))).toBe(
      "2 session(s) failed",
    );
    expect(
      eventAnomaly(ev("write", "tx_committed", { sessions: 3, inserted: 9, failed: 0 })),
    ).toBeNull();
    expect(eventAnomaly(ev("write", "tx_committed", { inserted: 9 }))).toBeNull();
  });

  it("flags malformed lines only", () => {
    expect(eventAnomaly(ev("parse", "line_classified", { type: "malformed", raw: "x" }))).toBe(
      "malformed line",
    );
    expect(eventAnomaly(ev("parse", "line_classified", { type: "message", raw: "{}" }))).toBeNull();
  });

  it("flags a batch_formed that lost watcher events", () => {
    expect(eventAnomaly(ev("watch", "batch_formed", { lost_events: true }))).toBe(
      "watcher lost events",
    );
    expect(eventAnomaly(ev("watch", "batch_formed", { lost_events: false }))).toBeNull();
    expect(eventAnomaly(ev("watch", "batch_formed", { paths: 3 }))).toBeNull();
  });

  it("returns null for routine events", () => {
    expect(eventAnomaly(ev("parse", "parse_started", { mode: "full" }))).toBeNull();
    expect(eventAnomaly(ev("parse", "message_emitted", { ordinal: 1 }))).toBeNull();
    expect(eventAnomaly(ev("write", "messages_written", { inserted: 4 }))).toBeNull();
    expect(eventAnomaly(ev("resolve", "dedup_winner", { reason: "newer" }))).toBeNull();
  });
});

describe("parsePipelineRecording", () => {
  it("parses a header line plus JSONL events", () => {
    const text = [
      JSON.stringify({
        kind: "header",
        format_version: 1,
        run_id: "run-9",
        scenario_id: "append",
        started_at_ms: 5,
      }),
      JSON.stringify({ seq: 1, at_ms: 6, stage: "watch", kind: "batch_formed" }),
      "",
      JSON.stringify({ seq: 2, at_ms: 7, stage: "parse", kind: "parse_done" }),
    ].join("\n");
    const { header, events } = parsePipelineRecording(text);
    expect(header.run_id).toBe("run-9");
    expect(events).toHaveLength(2);
    expect(events[1]?.kind).toBe("parse_done");
  });

  it("rejects a recording without a header", () => {
    expect(() =>
      parsePipelineRecording(JSON.stringify({ seq: 1, at_ms: 1, stage: "watch", kind: "x" })),
    ).toThrow(/header/);
  });

  it("rejects an unsupported format version", () => {
    expect(() =>
      parsePipelineRecording(JSON.stringify({ kind: "header", format_version: 99, run_id: "r" })),
    ).toThrow(/format_version/);
  });
});

function fakeWatch() {
  const handlers: ((e: PipelineEvent) => void)[] = [];
  const close = vi.fn();
  return {
    close,
    emit(e: PipelineEvent) {
      for (const h of handlers) h(e);
    },
    watch: (_runId: string, onEvent: (e: PipelineEvent) => void) => {
      handlers.push(onEvent);
      return { close } as unknown as EventSource;
    },
  };
}

describe("PipelineStore", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("follows the tail while live", () => {
    const w = fakeWatch();
    const store = new PipelineStore({ watch: w.watch });
    store.attachLive("run-1");
    w.emit(ev("watch", "batch_formed"));
    w.emit(ev("parse", "parse_started", { mode: "full" }, "f.jsonl"));
    expect(store.events).toHaveLength(2);
    expect(store.live).toBe(true);
    expect(store.cursor).toBe(2);
    expect(store.viewState.activeStage).toBe("parse");
  });

  it("deduplicates replayed events by seq", () => {
    const w = fakeWatch();
    const store = new PipelineStore({ watch: w.watch });
    store.attachLive("run-1");
    const e = ev("watch", "batch_formed");
    w.emit(e);
    w.emit({ ...e }); // reconnect replay
    w.emit(ev("resolve", "path_classified"));
    w.emit({ ...e }); // still below lastSeq
    expect(store.events).toHaveLength(2);
  });

  it("seek scrubs to a frozen position and disables live", () => {
    const w = fakeWatch();
    const store = new PipelineStore({ watch: w.watch });
    store.attachLive("run-1");
    w.emit(ev("watch", "batch_formed"));
    w.emit(ev("parse", "parse_started", { mode: "full" }, "f.jsonl"));
    w.emit(ev("parse", "line_classified", { line_no: 1, type: "a", raw: "x" }, "f.jsonl"));
    store.seek(1);
    expect(store.live).toBe(false);
    expect(store.viewState.activeStage).toBe("watch");
    // New events still arrive but the cursor stays put.
    w.emit(ev("write", "tx_committed", { inserted: 1 }));
    expect(store.cursor).toBe(1);
    expect(store.events).toHaveLength(4);
  });

  it("seeking back to the end re-enables live", () => {
    const w = fakeWatch();
    const store = new PipelineStore({ watch: w.watch });
    store.attachLive("run-1");
    w.emit(ev("watch", "batch_formed"));
    store.seek(0);
    expect(store.live).toBe(false);
    store.seek(store.events.length);
    expect(store.live).toBe(true);
    w.emit(ev("write", "tx_started", { sessions: 0 }));
    expect(store.cursor).toBe(2);
  });

  it("stepBy moves the cursor relative to its position", () => {
    const w = fakeWatch();
    const store = new PipelineStore({ watch: w.watch });
    store.attachLive("run-1");
    for (let i = 0; i < 5; i++) w.emit(ev("watch", "batch_formed"));
    store.stepBy(-2);
    expect(store.cursor).toBe(3);
    expect(store.live).toBe(false);
    store.stepBy(2);
    expect(store.cursor).toBe(5);
    expect(store.live).toBe(true);
    // Clamp at both ends.
    store.stepBy(-99);
    expect(store.cursor).toBe(0);
  });

  it("play advances the cursor and stops live-following at the end", () => {
    vi.useFakeTimers();
    const w = fakeWatch();
    const store = new PipelineStore({ watch: w.watch });
    store.attachLive("run-1");
    for (let i = 0; i < 3; i++) w.emit(ev("watch", "batch_formed"));
    store.seek(0);
    store.play(2); // 2 events per tick
    expect(store.playing).toBe(true);
    vi.advanceTimersByTime(100);
    expect(store.cursor).toBe(2);
    vi.advanceTimersByTime(100);
    expect(store.cursor).toBe(3);
    expect(store.playing).toBe(false);
    expect(store.live).toBe(true);
  });

  it("pause freezes playback", () => {
    vi.useFakeTimers();
    const w = fakeWatch();
    const store = new PipelineStore({ watch: w.watch });
    store.attachLive("run-1");
    for (let i = 0; i < 3; i++) w.emit(ev("watch", "batch_formed"));
    store.seek(0);
    store.play(1);
    vi.advanceTimersByTime(100);
    store.pause();
    vi.advanceTimersByTime(500);
    expect(store.cursor).toBe(1);
  });

  it("surfaces a stream that never opens as store.error", () => {
    let captured: WatchPipelineEventsOptions | undefined;
    const store = new PipelineStore({
      watch: (_runId, _onEvent, opts) => {
        captured = opts;
        return { close: vi.fn() } as unknown as EventSource;
      },
    });
    store.attachLive("run-x");
    captured?.onPermanentFailure?.();
    expect(store.error).toMatch(/run-x/);
  });

  it("lets a caller-supplied onPermanentFailure win over the default", () => {
    let captured: WatchPipelineEventsOptions | undefined;
    const custom = vi.fn();
    const store = new PipelineStore({
      watch: (_runId, _onEvent, opts) => {
        captured = opts;
        return { close: vi.fn() } as unknown as EventSource;
      },
    });
    store.attachLive("run-x", { onPermanentFailure: custom });
    captured?.onPermanentFailure?.();
    expect(custom).toHaveBeenCalledTimes(1);
    expect(store.error).toBeNull();
  });

  it("playRecording loads events frozen at the end, not live", async () => {
    const recording = [
      JSON.stringify({
        kind: "header",
        format_version: 1,
        run_id: "run-9",
        scenario_id: "s",
        started_at_ms: 5,
      }),
      JSON.stringify({ seq: 4, at_ms: 6, stage: "watch", kind: "batch_formed" }),
      JSON.stringify({
        seq: 9,
        at_ms: 7,
        stage: "finalize",
        kind: "run_finished",
        run_id: "run-9",
      }),
    ].join("\n");
    const store = new PipelineStore({
      downloadRecording: vi.fn(async () => new Response(recording)),
    });
    await store.playRecording("run-9.jsonl");
    expect(store.events).toHaveLength(2);
    expect(store.live).toBe(false);
    expect(store.cursor).toBe(2);
    expect(store.viewState.run.finished).toBe(true);
    store.seek(0);
    expect(store.viewState.parsedEvents).toHaveLength(0);
  });

  it("counts events lost to seq gaps as dropped", () => {
    const w = fakeWatch();
    const store = new PipelineStore({ watch: w.watch });
    store.attachLive("run-1");
    w.emit(ev("watch", "batch_formed")); // seq 1
    w.emit(ev("watch", "batch_formed")); // seq 2
    const skipped = ev("watch", "batch_formed");
    skipped.seq = 7; // seqs 3–6 never arrived
    w.emit(skipped);
    expect(store.dropped).toBe(4);
    const next = ev("watch", "batch_formed");
    next.seq = 8; // consecutive — no further loss
    w.emit(next);
    expect(store.dropped).toBe(4);
    expect(store.events).toHaveLength(4);
    // Reattaching to another run resets the counter with the buffer.
    store.attachLive("run-2");
    expect(store.dropped).toBe(0);
  });

  it("does not count a gap from the stream's first frame", () => {
    // afterSeq reconnects resume mid-stream — the first frame can
    // legitimately start above 1 and is not a loss.
    const w = fakeWatch();
    const store = new PipelineStore({ watch: w.watch });
    store.attachLive("run-1");
    const e = ev("watch", "batch_formed");
    e.seq = 9;
    w.emit(e);
    expect(store.dropped).toBe(0);
    expect(store.events).toHaveLength(1);
  });

  it("selects an event without moving the cursor", () => {
    const w = fakeWatch();
    const store = new PipelineStore({ watch: w.watch });
    store.attachLive("run-1");
    for (let i = 0; i < 3; i++) w.emit(ev("watch", "batch_formed"));
    store.seek(1);
    store.select(0);
    expect(store.selected).toBe(0);
    expect(store.cursor).toBe(1);
    expect(store.live).toBe(false);
    store.select(null);
    expect(store.selected).toBeNull();
    // resetBuffer clears the selection along with the buffer.
    store.select(2);
    store.attachLive("run-2");
    expect(store.selected).toBeNull();
  });

  it("pauses playback when pauseOnAnomaly consumes an anomaly", () => {
    vi.useFakeTimers();
    const w = fakeWatch();
    const store = new PipelineStore({ watch: w.watch });
    store.attachLive("run-1");
    w.emit(ev("watch", "batch_formed"));
    w.emit(ev("gates", "gate_verdict", { gate: "g", decision: "reject" }, "f.jsonl"));
    w.emit(ev("watch", "batch_formed"));
    store.seek(0);
    store.pauseOnAnomaly = true;
    store.play(1);
    vi.advanceTimersByTime(100); // consumes event 0 — routine
    expect(store.playing).toBe(true);
    vi.advanceTimersByTime(100); // consumes the anomalous gate_verdict
    expect(store.cursor).toBe(2);
    expect(store.playing).toBe(false);
    expect(store.live).toBe(false);
    vi.advanceTimersByTime(500); // stays paused
    expect(store.cursor).toBe(2);
  });

  it("keeps playing past routine events when pauseOnAnomaly is set", () => {
    vi.useFakeTimers();
    const w = fakeWatch();
    const store = new PipelineStore({ watch: w.watch });
    store.attachLive("run-1");
    for (let i = 0; i < 3; i++) w.emit(ev("watch", "batch_formed"));
    store.seek(0);
    store.pauseOnAnomaly = true;
    store.play(2);
    vi.advanceTimersByTime(200);
    expect(store.cursor).toBe(3);
    expect(store.playing).toBe(false);
    expect(store.live).toBe(true);
  });

  it("follows at_ms spacing in time playMode", () => {
    vi.useFakeTimers();
    const w = fakeWatch();
    const store = new PipelineStore({ watch: w.watch });
    store.playMode = "time";
    store.rtSpeed = 5; // 500ms of event time per 100ms tick
    store.attachLive("run-1");
    for (const t of [1000, 1300, 1600, 2400]) {
      const e = ev("watch", "batch_formed");
      e.at_ms = t;
      w.emit(e);
    }
    store.seek(0);
    store.play();
    // The playhead starts on the event under the cursor (at_ms 1000).
    vi.advanceTimersByTime(100); // playhead 1500 → 1000, 1300 consumed
    expect(store.cursor).toBe(2);
    vi.advanceTimersByTime(100); // playhead 2000 → 1600 consumed
    expect(store.cursor).toBe(3);
    vi.advanceTimersByTime(100); // playhead 2500 → tail reached
    expect(store.cursor).toBe(4);
    expect(store.playing).toBe(false);
    expect(store.live).toBe(true);
  });

  it("re-bases the time playhead on seek so the next tick does not undo it", () => {
    vi.useFakeTimers();
    const w = fakeWatch();
    const store = new PipelineStore({ watch: w.watch });
    store.playMode = "time";
    store.rtSpeed = 10; // 1000ms of event time per 100ms tick
    store.attachLive("run-1");
    for (const t of [1000, 2000, 3000, 4000, 5000]) {
      const e = ev("watch", "batch_formed");
      e.at_ms = t;
      w.emit(e);
    }
    store.seek(0);
    store.play();
    vi.advanceTimersByTime(200); // playhead 3000 → cursor 3
    expect(store.cursor).toBe(3);
    // Scrub back mid-play. Without a playhead rebase the next tick
    // recomputes from the stale watermark (3000+1000) and snaps the
    // cursor right back past the seek target.
    store.seek(1);
    vi.advanceTimersByTime(100); // rebased playhead 2000+1000 → cursor 3
    expect(store.cursor).toBe(3);
    expect(store.playing).toBe(true);
    vi.advanceTimersByTime(100); // playhead 4000 → cursor 4
    expect(store.cursor).toBe(4);
    vi.advanceTimersByTime(100); // playhead 5000 → tail
    expect(store.cursor).toBe(5);
    expect(store.playing).toBe(false);
    expect(store.live).toBe(true);
  });

  it("is a no-op when time playMode starts at the tail", () => {
    vi.useFakeTimers();
    const w = fakeWatch();
    const store = new PipelineStore({ watch: w.watch });
    store.playMode = "time";
    store.attachLive("run-1");
    w.emit(ev("watch", "batch_formed"));
    // cursor already follows the tail (live).
    store.play();
    expect(store.playing).toBe(false);
    expect(store.live).toBe(true);
    expect(store.cursor).toBe(1);
  });

  it("pauses on anomalies in time playMode too", () => {
    vi.useFakeTimers();
    const w = fakeWatch();
    const store = new PipelineStore({ watch: w.watch });
    store.playMode = "time";
    store.rtSpeed = 10; // 1000ms of event time per tick
    store.attachLive("run-1");
    const e1 = ev("watch", "batch_formed");
    e1.at_ms = 1000;
    w.emit(e1);
    const e2 = ev("gates", "gate_verdict", { gate: "g", decision: "drop" }, "f.jsonl");
    e2.at_ms = 1500;
    w.emit(e2);
    const e3 = ev("watch", "batch_formed");
    e3.at_ms = 2000;
    w.emit(e3);
    store.seek(0);
    store.pauseOnAnomaly = true;
    store.play();
    vi.advanceTimersByTime(100); // playhead 2000 → all three consumed, e2 anomalous
    expect(store.cursor).toBe(3);
    expect(store.playing).toBe(false);
    expect(store.live).toBe(false);
  });
});
