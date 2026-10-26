import type { PipelineEvent } from "../api/client.js";
import { eventAnomaly, PIPELINE_STAGES } from "../stores/pipeline.svelte.js";
import { countPipelineSequenceGaps, type PipelineRecordingHeader } from "./pipelineRecording.js";

export interface PipelineStageCounts {
  stage: string;
  events: number;
  anomalies: number;
}

export interface PipelineRunSummary {
  runId: string | null;
  scenarioId: string | null;
  eventCount: number;
  pathCount: number;
  anomalyCount: number;
  missingEvents: number;
  classifiedLines: number;
  insertedMessages: number;
  observedSpanMs: number | null;
  complete: boolean;
  stages: PipelineStageCounts[];
}

interface Metadata {
  runId?: string | null;
  header?: PipelineRecordingHeader | null;
  dropped?: number;
}

/** Full captured buffer, independent of replay position. The result retains
 * neither events nor payloads, so it can serve as an in-memory baseline. */
export function summarizePipelineRun(
  events: readonly PipelineEvent[],
  metadata: Metadata = {},
): PipelineRunSummary {
  const stages = new Map<string, PipelineStageCounts>(
    PIPELINE_STAGES.map((stage) => [stage, { stage, events: 0, anomalies: 0 }]),
  );
  const paths = new Set<string>();
  let anomalies = 0;
  let lines = 0;
  let inserted = 0;
  let first = Number.POSITIVE_INFINITY;
  let last = Number.NEGATIVE_INFINITY;
  let started = false;
  let finished = false;
  let scenarioId = metadata.header?.scenario_id || null;
  for (const event of events) {
    let stage = stages.get(event.stage);
    if (stage === undefined) {
      stage = { stage: event.stage, events: 0, anomalies: 0 };
      stages.set(event.stage, stage);
    }
    stage.events += 1;
    if (eventAnomaly(event) !== null) {
      anomalies += 1;
      stage.anomalies += 1;
    }
    if (event.path) paths.add(event.path);
    first = Math.min(first, event.at_ms);
    last = Math.max(last, event.at_ms);
    if (event.kind === "run_started") {
      started = true;
      const value = event.payload?.scenario_id;
      if (typeof value === "string" && value !== "") scenarioId = value;
    }
    if (event.kind === "run_finished") finished = true;
    if (event.kind === "line_classified") lines += 1;
    if (event.kind === "messages_written") {
      const value = event.payload?.inserted;
      if (typeof value === "number" && Number.isFinite(value) && value >= 0) inserted += value;
    }
  }
  const missingEvents = Math.max(countPipelineSequenceGaps(events), metadata.dropped ?? 0);
  return {
    runId: metadata.runId ?? metadata.header?.run_id ?? events[0]?.run_id ?? null,
    scenarioId,
    eventCount: events.length,
    pathCount: paths.size,
    anomalyCount: anomalies,
    missingEvents,
    classifiedLines: lines,
    insertedMessages: inserted,
    observedSpanMs: events.length === 0 ? null : last - first,
    // A header alone does not prove that the run_started event was captured.
    complete: started && finished && missingEvents === 0,
    stages: [...stages.values()],
  };
}

export interface PipelineStageComparison {
  stage: string;
  current: PipelineStageCounts;
  baseline: PipelineStageCounts | null;
}

/** Include stages seen only in the baseline, including unknown future ones. */
export function comparePipelineStages(
  current: PipelineRunSummary,
  baseline: PipelineRunSummary | null,
): PipelineStageComparison[] {
  const currentStages = new Map(current.stages.map((stage) => [stage.stage, stage]));
  const baselineStages = new Map(baseline?.stages.map((stage) => [stage.stage, stage]) ?? []);
  const names = new Set([...currentStages.keys(), ...baselineStages.keys()]);
  return [...names].map((stage) => ({
    stage,
    current: currentStages.get(stage) ?? { stage, events: 0, anomalies: 0 },
    baseline:
      baseline === null ? null : (baselineStages.get(stage) ?? { stage, events: 0, anomalies: 0 }),
  }));
}
