import type { PipelineEvent } from "../api/client.js";
import { m } from "../i18n/index.js";

/** The existing pipevents JSONL v1 header; preserve optional source metadata. */
export interface PipelineRecordingHeader {
  kind: "header";
  format_version: number;
  run_id: string;
  scenario_id?: string;
  started_at_ms: number;
  source_file?: string;
  source_sha256?: string;
}

export interface PipelineRecording {
  header: PipelineRecordingHeader;
  events: PipelineEvent[];
}

export const PIPELINE_RECORDING_MAX_BYTES = 32 * 1024 * 1024;
export const PIPELINE_RECORDING_MAX_EVENTS = 50_000;
const RECORDING_FORMAT_VERSION = 1;

function object(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function nonempty(value: unknown): value is string {
  return typeof value === "string" && value.trim() !== "";
}

function timestamp(value: unknown): boolean {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function optionalString(value: unknown): boolean {
  return value === undefined || typeof value === "string";
}

function validateHeader(value: unknown, line: number): asserts value is PipelineRecordingHeader {
  if (!object(value) || value.kind !== "header") {
    throw new Error(m.pipeline_recording_header_expected({ line: String(line) }));
  }
  if (value.format_version !== RECORDING_FORMAT_VERSION) {
    throw new Error(m.pipeline_recording_version_error());
  }
  if (
    !nonempty(value.run_id) ||
    !timestamp(value.started_at_ms) ||
    !optionalString(value.scenario_id) ||
    !optionalString(value.source_file) ||
    !optionalString(value.source_sha256)
  ) {
    throw new Error(m.pipeline_recording_header_fields({ line: String(line) }));
  }
}

function validateEvent(
  value: unknown,
  header: PipelineRecordingHeader,
  previousSeq: number | null,
  line: number,
): asserts value is PipelineEvent {
  if (
    !object(value) ||
    typeof value.seq !== "number" ||
    !Number.isSafeInteger(value.seq) ||
    value.seq <= 0 ||
    !timestamp(value.at_ms) ||
    !nonempty(value.stage) ||
    !nonempty(value.kind) ||
    !optionalString(value.path) ||
    !optionalString(value.run_id) ||
    (value.payload !== undefined && !object(value.payload))
  ) {
    throw new Error(m.pipeline_recording_event_fields({ line: String(line) }));
  }
  if (value.run_id !== undefined && value.run_id !== header.run_id) {
    throw new Error(m.pipeline_recording_run_id_error({ line: String(line) }));
  }
  if (previousSeq !== null && value.seq <= previousSeq) {
    throw new Error(m.pipeline_recording_sequence_error({ line: String(line) }));
  }
}

export function checkPipelineRecordingSize(bytes: number): void {
  if (bytes > PIPELINE_RECORDING_MAX_BYTES) {
    throw new Error(m.pipeline_recording_size_error());
  }
}

/** Read incrementally so an absent or dishonest Content-Length cannot make
 * a recording allocate an unbounded response Blob before its size is checked.
 * A stale source stops consuming and cancels its response body. */
export async function readPipelineRecordingResponse(
  response: Response,
  isCurrent: () => boolean = () => true,
): Promise<string | undefined> {
  const reader = response.body?.getReader();
  let finished = false;
  try {
    if (!response.ok)
      throw new Error(m.pipeline_recording_http_error({ status: String(response.status) }));
    const declaredSize = Number(response.headers.get("Content-Length"));
    if (Number.isFinite(declaredSize)) checkPipelineRecordingSize(declaredSize);
    if (reader === undefined) return "";
    const decoder = new TextDecoder();
    const chunks: string[] = [];
    let bytes = 0;
    while (isCurrent()) {
      const chunk = await reader.read();
      if (!isCurrent()) return undefined;
      if (chunk.done) {
        finished = true;
        chunks.push(decoder.decode());
        return chunks.join("");
      }
      bytes += chunk.value.byteLength;
      checkPipelineRecordingSize(bytes);
      chunks.push(decoder.decode(chunk.value, { stream: true }));
    }
    return undefined;
  } finally {
    if (reader !== undefined) {
      if (!finished) await reader.cancel().catch(() => {});
      reader.releaseLock();
    }
  }
}

/** Validate before touching the current source. Sequence gaps and unknown
 * kinds/stages are valid; timestamps need not increase when the clock moves. */
export function parsePipelineRecording(text: string): PipelineRecording {
  // The cheap UTF-16 length check avoids encoding an already oversized input.
  checkPipelineRecordingSize(text.length);
  checkPipelineRecordingSize(new TextEncoder().encode(text).byteLength);
  let header: PipelineRecordingHeader | null = null;
  const events: PipelineEvent[] = [];
  let previousSeq: number | null = null;
  let line = 0;
  for (const raw of text.replace(/^\uFEFF/, "").split("\n")) {
    line += 1;
    if (raw.trim() === "") continue;
    let decoded: unknown;
    try {
      decoded = JSON.parse(raw);
    } catch {
      // Do not include raw transcript content in error messages.
      throw new Error(m.pipeline_recording_json_error({ line: String(line) }));
    }
    if (header === null) {
      validateHeader(decoded, line);
      header = decoded;
      continue;
    }
    if (events.length >= PIPELINE_RECORDING_MAX_EVENTS) {
      throw new Error(m.pipeline_recording_event_limit());
    }
    validateEvent(decoded, header, previousSeq, line);
    previousSeq = decoded.seq;
    events.push(decoded);
  }
  if (header === null) throw new Error(m.pipeline_recording_empty_error());
  return { header, events };
}

/** Export exactly the captured buffer, preserving global seq and header
 * metadata. Limits also ensure that an exported snapshot can be opened here. */
export function serializePipelineRecording(
  header: PipelineRecordingHeader,
  events: readonly PipelineEvent[],
): string {
  validateHeader(header, 1);
  if (events.length > PIPELINE_RECORDING_MAX_EVENTS) {
    throw new Error(m.pipeline_recording_event_limit());
  }
  const encoder = new TextEncoder();
  const lines: string[] = [];
  let bytes = 0;
  function append(value: unknown): void {
    const line = JSON.stringify(value);
    if (line === undefined) {
      throw new Error(m.pipeline_recording_event_fields({ line: String(lines.length + 1) }));
    }
    bytes += encoder.encode(line).byteLength + 1;
    checkPipelineRecordingSize(bytes);
    lines.push(line);
  }
  append(header);
  let previousSeq: number | null = null;
  for (const [index, event] of events.entries()) {
    validateEvent(event, header, previousSeq, index + 2);
    previousSeq = event.seq;
    append(event);
  }
  return `${lines.join("\n")}\n`;
}

/** A live buffer needs its actual start bookend. Never label the first
 * retained event's timestamp as the start of a truncated run. */
export function recordingHeaderFromEvents(
  runId: string,
  events: readonly PipelineEvent[],
): PipelineRecordingHeader {
  const started = events.find((event) => event.kind === "run_started");
  if (started === undefined) {
    throw new Error(m.pipeline_recording_missing_start());
  }
  const payload = started.payload ?? {};
  const header: PipelineRecordingHeader = {
    kind: "header",
    format_version: RECORDING_FORMAT_VERSION,
    run_id: runId,
    started_at_ms: started.at_ms,
  };
  if (typeof payload.scenario_id === "string") header.scenario_id = payload.scenario_id;
  if (typeof payload.source_file === "string") header.source_file = payload.source_file;
  validateHeader(header, 1);
  return header;
}

export function pipelineRecordingFilename(runId: string): string {
  const safe = runId
    .replace(/[^a-zA-Z0-9._-]/g, "_")
    .replace(/^\.+/, "")
    .slice(0, 100);
  return `pipeline-${safe || "capture"}.jsonl`;
}

/** Hub sequence numbers are global. Count only known interior gaps, never
 * the unknown prefix before the first captured event. */
export function countPipelineSequenceGaps(events: readonly PipelineEvent[]): number {
  let missing = 0;
  let previous: number | null = null;
  for (const event of events) {
    if (previous !== null && event.seq > previous + 1) missing += event.seq - previous - 1;
    previous = event.seq;
  }
  return missing;
}
