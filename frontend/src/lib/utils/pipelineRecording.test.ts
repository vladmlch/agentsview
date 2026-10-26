import { describe, expect, it, vi } from "vite-plus/test";
import type { PipelineEvent } from "../api/client.js";
import {
  PIPELINE_RECORDING_MAX_BYTES,
  PIPELINE_RECORDING_MAX_EVENTS,
  countPipelineSequenceGaps,
  parsePipelineRecording,
  pipelineRecordingFilename,
  recordingHeaderFromEvents,
  readPipelineRecordingResponse,
  serializePipelineRecording,
  type PipelineRecordingHeader,
} from "./pipelineRecording.js";

const HEADER: PipelineRecordingHeader = {
  kind: "header",
  format_version: 1,
  run_id: "run-a",
  started_at_ms: 1000,
  scenario_id: "cold",
  source_file: "sessions/example.jsonl",
  source_sha256: "abc123",
};
const FIRST: PipelineEvent = {
  seq: 101,
  at_ms: 1000,
  run_id: "run-a",
  stage: "finalize",
  kind: "run_started",
  payload: { scenario_id: "cold", source_file: "sessions/example.jsonl" },
};
function recording(...events: unknown[]): string {
  return [HEADER, ...events].map((item) => JSON.stringify(item)).join("\n");
}

describe("pipeline recording codec", () => {
  it("round-trips header metadata, global sequences and nested payloads", () => {
    const events = [
      FIRST,
      {
        ...FIRST,
        seq: 104,
        at_ms: 1200,
        kind: "run_finished",
        payload: { nested: { ok: true } },
      },
    ];
    const text = serializePipelineRecording(HEADER, events);
    expect(text.endsWith("\n")).toBe(true);
    expect(parsePipelineRecording(text)).toEqual({ header: HEADER, events });
    expect(countPipelineSequenceGaps(events)).toBe(2);
  });

  it("accepts a BOM, blank physical lines, CRLF and header-only recordings", () => {
    expect(parsePipelineRecording(`\uFEFF\r\n${JSON.stringify(HEADER)}\r\n\r\n`)).toEqual({
      header: HEADER,
      events: [],
    });
  });

  it.each([
    null,
    [],
    3,
    "header",
    {},
    { ...HEADER, run_id: "" },
    { ...HEADER, started_at_ms: null },
    { ...HEADER, source_file: {} },
  ])("rejects malformed header %j without throwing a TypeError", (header) => {
    expect(() => parsePipelineRecording(JSON.stringify(header))).toThrow(/header/);
  });

  it("reports invalid JSON with its physical line number and no raw payload", () => {
    expect(() =>
      parsePipelineRecording(`${JSON.stringify(HEADER)}\n\nPRIVATE INVALID TEXT`),
    ).toThrow("pipeline recording: decode line 3: invalid JSON");
  });

  it.each([
    null,
    [],
    false,
    { ...FIRST, seq: 0 },
    { ...FIRST, seq: 1.5 },
    { ...FIRST, seq: Number.MAX_SAFE_INTEGER + 1 },
    { ...FIRST, at_ms: -1 },
    { ...FIRST, at_ms: "1000" },
    { ...FIRST, stage: "" },
    { ...FIRST, kind: " " },
    { ...FIRST, path: [] },
    { ...FIRST, payload: [] },
    { ...FIRST, payload: null },
  ])("rejects malformed event %j", (event) => {
    expect(() => parsePipelineRecording(recording(event))).toThrow(/line 2: invalid event/);
  });

  it("rejects a mixed run, but permits omitted event run IDs", () => {
    expect(() => parsePipelineRecording(recording({ ...FIRST, run_id: "other" }))).toThrow(
      /run_id/,
    );
    const { run_id: _run, ...withoutRun } = FIRST;
    expect(parsePipelineRecording(recording(withoutRun)).events).toEqual([withoutRun]);
  });

  it.each([101, 100])("rejects duplicated or decreasing sequence %s", (seq) => {
    expect(() => parsePipelineRecording(recording(FIRST, { ...FIRST, seq }))).toThrow(
      /line 3: seq/,
    );
  });

  it("preserves unknown stages/kinds and regressing clocks in sequence order", () => {
    const future = { ...FIRST, seq: 200, at_ms: 900, kind: "future_kind", stage: "future_stage" };
    const parsed = parsePipelineRecording(recording(FIRST, future));
    expect(parsed.events).toEqual([FIRST, future]);
    expect(countPipelineSequenceGaps(parsed.events)).toBe(98);
  });

  it("does not invent a missing prefix before the first global sequence", () => {
    expect(countPipelineSequenceGaps([FIRST, { ...FIRST, seq: 102 }])).toBe(0);
    expect(countPipelineSequenceGaps([])).toBe(0);
  });

  it("rejects text over the byte budget before JSON parsing", () => {
    expect(() => parsePipelineRecording(" ".repeat(PIPELINE_RECORDING_MAX_BYTES + 1))).toThrow(
      /32 MiB/,
    );
    // UTF-16 character count alone would incorrectly accept this UTF-8 input.
    expect(() =>
      parsePipelineRecording("\u00e9".repeat(PIPELINE_RECORDING_MAX_BYTES / 2 + 1)),
    ).toThrow(/32 MiB/);
  });

  it("rejects too many events even when the byte budget is not exhausted", () => {
    const events = Array.from({ length: PIPELINE_RECORDING_MAX_EVENTS + 1 }, (_, i) => ({
      seq: i + 1,
      at_ms: 1000,
      stage: "watch",
      kind: "x",
    }));
    const text = [HEADER, ...events].map((item) => JSON.stringify(item)).join("\n");
    expect(() => parsePipelineRecording(text)).toThrow(/50000 event/);
    expect(() => serializePipelineRecording(HEADER, events as PipelineEvent[])).toThrow(
      /50000 event/,
    );
  });

  it("validates serialization rather than creating an unreadable export", () => {
    expect(() => serializePipelineRecording(HEADER, [FIRST, FIRST])).toThrow(/seq/);
    expect(() => serializePipelineRecording(HEADER, [{ ...FIRST, run_id: "other" }])).toThrow(
      /run_id/,
    );
  });

  it("builds live metadata only from the actual run start", () => {
    expect(recordingHeaderFromEvents("run-a", [FIRST])).toEqual({
      kind: "header",
      format_version: 1,
      run_id: "run-a",
      started_at_ms: 1000,
      scenario_id: "cold",
      source_file: "sessions/example.jsonl",
    });
    expect(() => recordingHeaderFromEvents("run-a", [{ ...FIRST, kind: "parse_done" }])).toThrow(
      /run_started/,
    );
  });

  it("constrains download names without using source paths", () => {
    expect(pipelineRecordingFilename("run-a")).toBe("pipeline-run-a.jsonl");
    expect(pipelineRecordingFilename("../../odd/name")).toBe("pipeline-_.._odd_name.jsonl");
    expect(pipelineRecordingFilename("...")).toBe("pipeline-capture.jsonl");
    expect(pipelineRecordingFilename("a".repeat(200))).toHaveLength(115);
  });
});

describe("bounded recording response reads", () => {
  it("decodes a UTF-8 character split across response chunks", async () => {
    const bytes = new TextEncoder().encode("a\u00e9b");
    const response = new Response(
      new ReadableStream({
        start(controller) {
          controller.enqueue(bytes.slice(0, 2));
          controller.enqueue(bytes.slice(2));
          controller.close();
        },
      }),
    );
    expect(await readPipelineRecordingResponse(response)).toBe("a\u00e9b");
  });

  it("enforces actual streamed bytes despite an understated Content-Length", async () => {
    const cancel = vi.fn();
    const response = new Response(
      new ReadableStream({
        start(controller) {
          controller.enqueue(new Uint8Array(PIPELINE_RECORDING_MAX_BYTES + 1));
        },
        cancel,
      }),
      { headers: { "Content-Length": "1" } },
    );
    await expect(readPipelineRecordingResponse(response)).rejects.toThrow(/32 MiB/);
    expect(cancel).toHaveBeenCalledTimes(1);
  });

  it("cancels a stale response instead of reading it into the new source", async () => {
    const cancel = vi.fn();
    const response = new Response(new ReadableStream({ cancel }));
    expect(await readPipelineRecordingResponse(response, () => false)).toBeUndefined();
    expect(cancel).toHaveBeenCalledTimes(1);
  });
});
