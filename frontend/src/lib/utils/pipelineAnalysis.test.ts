import { describe, expect, it } from "vite-plus/test";
import type { PipelineEvent } from "../api/client.js";
import { comparePipelineStages, summarizePipelineRun } from "./pipelineAnalysis.js";

const EVENTS: PipelineEvent[] = [
  {
    seq: 101,
    at_ms: 1000,
    stage: "finalize",
    kind: "run_started",
    run_id: "a",
    payload: { scenario_id: "cold" },
  },
  {
    seq: 102,
    at_ms: 990,
    stage: "parse",
    kind: "line_classified",
    path: "a.jsonl",
    payload: { type: "session_meta" },
  },
  {
    seq: 103,
    at_ms: 1100,
    stage: "parse",
    kind: "line_classified",
    path: "a.jsonl",
    payload: { type: "malformed" },
  },
  {
    seq: 106,
    at_ms: 1150,
    stage: "write",
    kind: "messages_written",
    path: "a.jsonl",
    payload: { inserted: 3 },
  },
  {
    seq: 107,
    at_ms: 1200,
    stage: "write",
    kind: "messages_written",
    path: "b.jsonl",
    payload: { inserted: 2 },
  },
  {
    seq: 108,
    at_ms: 1210,
    stage: "write",
    kind: "tx_committed",
    payload: { inserted: 5, failed: 0 },
  },
  { seq: 109, at_ms: 1300, stage: "finalize", kind: "run_finished" },
];

describe("pipeline run analysis", () => {
  it("counts observed facts without counting transaction totals twice", () => {
    const summary = summarizePipelineRun(EVENTS);
    expect(summary).toMatchObject({
      runId: "a",
      scenarioId: "cold",
      eventCount: 7,
      pathCount: 2,
      anomalyCount: 1,
      missingEvents: 2,
      classifiedLines: 2,
      insertedMessages: 5,
      observedSpanMs: 310,
      complete: false,
    });
    expect(summary.stages.filter((stage) => stage.events > 0)).toEqual([
      { stage: "parse", events: 2, anomalies: 1 },
      { stage: "write", events: 3, anomalies: 0 },
      { stage: "finalize", events: 2, anomalies: 0 },
    ]);
  });

  it("uses zero counts and an absent span for an empty capture", () => {
    expect(summarizePipelineRun([])).toMatchObject({
      runId: null,
      scenarioId: null,
      eventCount: 0,
      pathCount: 0,
      anomalyCount: 0,
      missingEvents: 0,
      classifiedLines: 0,
      insertedMessages: 0,
      observedSpanMs: null,
      complete: false,
    });
  });

  it("requires both bookends and no known gap for a complete capture", () => {
    const complete = [EVENTS[0]!, { ...EVENTS[6]!, seq: 102 }];
    expect(summarizePipelineRun(complete).complete).toBe(true);
    expect(summarizePipelineRun(complete.slice(1)).complete).toBe(false);
    expect(summarizePipelineRun(complete.slice(0, 1)).complete).toBe(false);
    expect(summarizePipelineRun(complete, { dropped: 3 }).complete).toBe(false);
    // Store and codec observe the same gap, so do not add their counters.
    expect(summarizePipelineRun(EVENTS, { dropped: 2 }).missingEvents).toBe(2);
  });

  it("does not equate a complete capture with a successful run", () => {
    const events = EVENTS.map((event, index) => ({ ...event, seq: index + 200 }));
    expect(summarizePipelineRun(events)).toMatchObject({ complete: true, anomalyCount: 1 });
  });

  it("retains no references to a live buffer or its payloads", () => {
    const source = [{ ...EVENTS[0]!, payload: { scenario_id: "cold" } }];
    const pinned = summarizePipelineRun(source);
    source[0]!.payload.scenario_id = "append";
    source.push({ ...source[0]!, seq: 102 });
    expect(pinned).toMatchObject({ eventCount: 1, scenarioId: "cold" });
    expect(pinned.stages.find((stage) => stage.stage === "finalize")?.events).toBe(1);
  });

  it("includes a future stage present only in the baseline", () => {
    const baseline = summarizePipelineRun([
      { ...EVENTS[0]!, stage: "future_stage" as PipelineEvent["stage"] },
    ]);
    const current = summarizePipelineRun([]);
    const row = comparePipelineStages(current, baseline).find(
      (stage) => stage.stage === "future_stage",
    );
    expect(row).toEqual({
      stage: "future_stage",
      current: { stage: "future_stage", events: 0, anomalies: 0 },
      baseline: { stage: "future_stage", events: 1, anomalies: 0 },
    });
    expect(comparePipelineStages(current, null).every((stage) => stage.baseline === null)).toBe(
      true,
    );
  });
});
