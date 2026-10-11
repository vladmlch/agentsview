import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { EventSource } from "eventsource";
import type { WatchPipelineEventsOptions } from "../api/client.js";
import {
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
});
