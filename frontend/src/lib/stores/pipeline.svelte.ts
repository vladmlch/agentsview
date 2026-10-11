import { watchPipelineEvents, type WatchPipelineEventsOptions } from "../api/client.js";
import type { PipelineEvent, PipelineStage } from "../api/client.js";
import {
  PipelineService,
  type PipelineRecordingImported,
  type PipelineRunInfo,
  type PipelineRunStarted,
  type PipelineScenarioOut,
} from "../api/generated/index.js";
import type { EventSource } from "eventsource";

export type { PipelineEvent, PipelineStage } from "../api/client.js";

/** Stage order matching pipevents.Stage* constants — used to infer
 * per-stage done state for the diagram: a stage counts as finished
 * once a different stage emits (boundary kinds close their stage
 * explicitly). */
export const PIPELINE_STAGES: PipelineStage[] = [
  "watch",
  "resolve",
  "gates",
  "parse",
  "batch",
  "write",
  "finalize",
  "embed",
];

export type PipelineStageStatus = "idle" | "active" | "done";

/** One transcript line shown on the File tab. `no` is the line's
 * number within its parse pass for `fileLines` entries, and its
 * position in the reconstructed file for `files` entries. `parsed`
 * is false only for `type === "malformed"` (non-JSON lines). */
export interface PipelineFileLine {
  no: number;
  raw: string;
  parsed: boolean;
  type: string;
  truncated: boolean;
}

export interface PipelineGateVerdict {
  gate: string;
  decision: string;
  reason: string;
  path?: string;
}

export interface PipelineDbWrite {
  session_id?: string;
  inserted: number;
  updated: number;
  /** Batch tx_committed totals — absent on per-session commits. */
  sessions?: number;
  failed?: number;
}

export interface PipelineScenarioStep {
  n: number;
  action: string;
  path?: string;
  detail?: string;
}

export interface PipelineRunState {
  runId: string | null;
  scenarioId: string | null;
  sourceFile: string | null;
  finished: boolean;
  /** run_finished payload (scenario stats) once the run closes. */
  stats: Record<string, unknown> | null;
}

/** The per-tab state Task 9 renders. Rebuilt from events[0..cursor)
 * on every cursor move, so scrubbing replays the run's history. */
export interface PipelineViewState {
  /** Append-only transcript of every classified line (parse ordinal
   * `no` restarts each pass), each tagged with its file `path`. */
  fileLines: (PipelineFileLine & { path: string })[];
  /** Reconstructed current content per path: reset when a parse
   * starts with mode "full", appended to by line_classified. Engine-
   * level parse_started modes ("collect"/"staged") do not reset —
   * the parser-level "full"/"incremental" event follows them. */
  files: Record<string, PipelineFileLine[]>;
  /** Every consumed event — the Events tab log. */
  parsedEvents: PipelineEvent[];
  gates: PipelineGateVerdict[];
  /** Write ops as they committed (DB tab): one entry per
   * messages_written and tx_committed. */
  dbDiff: PipelineDbWrite[];
  /** Sum of fts_updated.fts_rows. */
  ftsRows: number;
  /** Latest cursor_updated payload plus the file path it came from. */
  cursor: Record<string, unknown> | null;
  /** Current pending-write depth (pending_accumulated), drained to
   * zero by flush_triggered. */
  queue: { pending: number; bytes: number };
  /** Last flush_triggered: what was drained and why. */
  lastFlush: { reason: string; pending: number; bytes: number } | null;
  steps: PipelineScenarioStep[];
  /** Stage of the last consumed event — the diagram's highlight. */
  activeStage: PipelineStage | null;
  /** Per-stage lifecycle for the diagram. A stage is "active" while
   * it produced the most recent event, "done" once another stage
   * emits or its closing kind arrives, "idle" until first use. */
  stageStatus: Record<PipelineStage, PipelineStageStatus>;
  run: PipelineRunState;
}

/** Line 1 of a .jsonl run recording (pipevents.RecordingHeader). */
export interface PipelineRecordingHeader {
  kind: "header";
  format_version: number;
  run_id: string;
  scenario_id?: string;
  started_at_ms: number;
  source_file?: string;
  source_sha256?: string;
}

const RECORDING_FORMAT_VERSION = 1;

/** Parse a downloaded/uploaded .jsonl recording the same way
 * pipevents.ReadRecording does: first non-empty line must be the
 * header, the rest are pipevents.Event JSON. */
export function parsePipelineRecording(text: string): {
  header: PipelineRecordingHeader;
  events: PipelineEvent[];
} {
  let header: PipelineRecordingHeader | null = null;
  const events: PipelineEvent[] = [];
  let lineNo = 0;
  for (const raw of text.split("\n")) {
    lineNo += 1;
    if (raw.trim() === "") continue;
    let decoded: unknown;
    try {
      decoded = JSON.parse(raw);
    } catch (cause) {
      throw new Error(
        `pipeline recording: decode line ${lineNo}: ${cause instanceof Error ? cause.message : String(cause)}`,
      );
    }
    if (header === null) {
      const h = decoded as PipelineRecordingHeader;
      if (h.kind !== "header") {
        throw new Error(
          `pipeline recording: line ${lineNo} kind ${JSON.stringify(h.kind)}, want "header"`,
        );
      }
      if (h.format_version !== RECORDING_FORMAT_VERSION) {
        throw new Error(`pipeline recording: unsupported format_version ${h.format_version}`);
      }
      header = h;
      continue;
    }
    events.push(decoded as PipelineEvent);
  }
  if (header === null) throw new Error("pipeline recording: empty recording");
  return { header, events };
}

function num(v: unknown): number {
  return typeof v === "number" && Number.isFinite(v) ? v : 0;
}

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
}

function optStr(v: unknown): string | undefined {
  const s = str(v);
  return s === "" ? undefined : s;
}

/** Kinds that close their stage for the diagram. Stages without a
 * closing kind (watch, resolve, gates) report done once a different
 * stage emits — see applyStageStatus. run_finished closes all. */
const STAGE_CLOSING_KINDS: Record<string, PipelineStage | "all"> = {
  parse_done: "parse",
  flush_triggered: "batch",
  tx_committed: "write",
  embed_done: "embed",
  embed_skipped: "embed",
  sync_pass_done: "finalize",
  run_finished: "all",
};

function emptyViewState(): PipelineViewState {
  return {
    fileLines: [],
    files: {},
    parsedEvents: [],
    gates: [],
    dbDiff: [],
    ftsRows: 0,
    cursor: null,
    queue: { pending: 0, bytes: 0 },
    lastFlush: null,
    steps: [],
    activeStage: null,
    stageStatus: {
      watch: "idle",
      resolve: "idle",
      gates: "idle",
      parse: "idle",
      batch: "idle",
      write: "idle",
      finalize: "idle",
      embed: "idle",
    },
    run: { runId: null, scenarioId: null, sourceFile: null, finished: false, stats: null },
  };
}

function applyStageStatus(state: PipelineViewState, ev: PipelineEvent): void {
  if (PIPELINE_STAGES.includes(ev.stage)) {
    // Exactly one stage is active at a time: whichever emitted most
    // recently. Any other "active" stage reverts to done — it already
    // produced its events and the pipeline moved on. Re-emission
    // re-activates a done stage (next file through the same stage).
    for (const st of PIPELINE_STAGES) {
      if (st !== ev.stage && state.stageStatus[st] === "active") {
        state.stageStatus[st] = "done";
      }
    }
    state.stageStatus[ev.stage] = "active";
  }
  const closing = STAGE_CLOSING_KINDS[ev.kind];
  if (closing === "all") {
    for (const st of PIPELINE_STAGES) {
      if (state.stageStatus[st] !== "idle") state.stageStatus[st] = "done";
    }
  } else if (closing !== undefined) {
    state.stageStatus[closing] = "done";
  }
  state.activeStage = ev.stage;
}

/** Pure reduction of an event slice into per-tab view state — the
 * replay engine's testable core. Unknown kinds are safe: they land
 * in parsedEvents and update activeStage only. Events are consumed
 * in array order; callers pass events[0..cursor). */
export function reduceEvents(evs: PipelineEvent[]): PipelineViewState {
  const state = emptyViewState();
  for (const ev of evs) {
    state.parsedEvents.push(ev);
    applyStageStatus(state, ev);
    const p = ev.payload ?? {};
    switch (ev.kind) {
      case "line_classified": {
        const type = str(p.type);
        const line: PipelineFileLine = {
          no: num(p.line_no),
          raw: str(p.raw),
          // "malformed" marks lines that are not valid JSON.
          parsed: type !== "malformed",
          type,
          truncated: p.truncated === true,
        };
        state.fileLines.push({ ...line, path: ev.path ?? "" });
        if (ev.path !== undefined) {
          const lines = (state.files[ev.path] ??= []);
          lines.push({ ...line, no: lines.length + 1 });
        }
        break;
      }
      case "parse_started": {
        // Only the parser-level "full" mode rewrites the file; the
        // engine-level "collect"/"staged" markers precede it and the
        // "incremental" mode appends.
        if (ev.path !== undefined && p.mode === "full") {
          state.files[ev.path] = [];
        }
        break;
      }
      case "gate_verdict":
        state.gates.push({
          gate: str(p.gate),
          decision: str(p.decision),
          reason: str(p.reason),
          path: ev.path,
        });
        break;
      case "messages_written":
        state.dbDiff.push({
          session_id: optStr(p.session_id),
          inserted: num(p.inserted),
          updated: num(p.updated),
        });
        break;
      case "tx_committed":
        state.dbDiff.push({
          session_id: optStr(p.session_id),
          inserted: num(p.inserted),
          updated: 0,
          sessions: p.sessions === undefined ? undefined : num(p.sessions),
          failed: p.failed === undefined ? undefined : num(p.failed),
        });
        break;
      case "fts_updated":
        state.ftsRows += num(p.fts_rows);
        break;
      case "cursor_updated":
        state.cursor = { ...p, path: ev.path };
        break;
      case "pending_accumulated":
        state.queue = {
          pending: num(p.pending_count),
          bytes: num(p.pending_bytes),
        };
        break;
      case "flush_triggered":
        state.lastFlush = {
          reason: str(p.reason),
          pending: num(p.pending_count),
          bytes: num(p.pending_bytes),
        };
        state.queue = { pending: 0, bytes: 0 };
        break;
      case "scenario_step":
        state.steps.push({
          n: num(p.n),
          action: str(p.action),
          path: ev.path,
          detail: optStr(p.detail),
        });
        break;
      case "run_started":
        state.run = {
          runId: ev.run_id ?? null,
          scenarioId: optStr(p.scenario_id) ?? null,
          sourceFile: optStr(p.source_file) ?? null,
          finished: false,
          stats: null,
        };
        break;
      case "run_finished":
        state.run.finished = true;
        state.run.stats = ev.payload ?? null;
        if (ev.run_id !== undefined) state.run.runId = ev.run_id;
        break;
    }
  }
  return state;
}

/** Injectable seams so tests can drive the store without a server.
 * Every entry defaults to the generated API client / SSE watcher. */
export interface PipelineStoreDeps {
  watch?: (
    runId: string,
    onEvent: (e: PipelineEvent) => void,
    opts?: WatchPipelineEventsOptions,
  ) => EventSource;
  listScenarios?: () => Promise<PipelineScenarioOut[]>;
  startRunApi?: (scenarioId: string) => Promise<PipelineRunStarted>;
  listRuns?: () => Promise<PipelineRunInfo[]>;
  listRecordings?: () => Promise<string[]>;
  downloadRecording?: (name: string) => Promise<Response>;
  importRecordingApi?: (file: Blob) => Promise<PipelineRecordingImported>;
  observeStart?: (runId: string) => Promise<PipelineRunStarted>;
  observeStop?: () => Promise<void>;
  resetSandboxApi?: () => Promise<void>;
}

/** Milliseconds between playback ticks. */
export const PIPELINE_PLAY_TICK_MS = 100;

export class PipelineStore {
  /** Full event buffer for the attached run/loaded recording —
   * scrubbing never discards it; `cursor` selects the visible prefix. */
  events = $state<PipelineEvent[]>([]);
  /** Replay position: viewState covers events[0..cursor). */
  cursor = $state(0);
  /** While true, cursor follows the tail as events arrive. */
  live = $state(true);
  runs = $state<PipelineRunInfo[]>([]);
  scenarios = $state<PipelineScenarioOut[]>([]);
  recordings = $state<string[]>([]);
  /** Run the buffer belongs to (live or recording). */
  runId = $state<string | null>(null);
  /** Header of the loaded recording, when the buffer came from a file. */
  header = $state<PipelineRecordingHeader | null>(null);
  playing = $state(false);
  /** Events advanced per playback tick. */
  speed = $state(1);
  error = $state<string | null>(null);

  viewState = $derived.by(() => reduceEvents(this.events.slice(0, this.cursor)));

  private es: EventSource | null = null;
  private playTimer: ReturnType<typeof setInterval> | null = null;
  /** Highest seq appended — SSE reconnect replays the run log, so
   * already-seen seqs are dropped instead of duplicated. */
  private lastSeq = 0;
  private deps: Required<PipelineStoreDeps>;

  constructor(deps: PipelineStoreDeps = {}) {
    this.deps = {
      watch: deps.watch ?? ((id, cb, o) => watchPipelineEvents(id, cb, o)),
      listScenarios: deps.listScenarios ?? (() => PipelineService.getApiV1PipelineScenarios()),
      startRunApi:
        deps.startRunApi ??
        ((scenario_id) => PipelineService.postApiV1PipelineRuns({ scenario_id })),
      listRuns: deps.listRuns ?? (() => PipelineService.getApiV1PipelineRuns()),
      listRecordings: deps.listRecordings ?? (() => PipelineService.getApiV1PipelineRecordings()),
      downloadRecording:
        deps.downloadRecording ??
        ((name) => PipelineService.getApiV1PipelineRecordingsByName({ name })),
      importRecordingApi:
        deps.importRecordingApi ??
        ((file) => PipelineService.postApiV1PipelineRecordings({ file })),
      observeStart:
        deps.observeStart ?? ((run_id) => PipelineService.postApiV1PipelineObserve({ run_id })),
      observeStop: deps.observeStop ?? (() => PipelineService.deleteApiV1PipelineObserve()),
      resetSandboxApi:
        deps.resetSandboxApi ?? (() => PipelineService.postApiV1PipelineSandboxReset()),
    };
  }

  /** Append one streamed event. Dedups on seq so an EventSource
   * reconnect (which replays the run log) cannot duplicate rows. */
  private pushEvent(e: PipelineEvent): void {
    if (e.seq <= this.lastSeq) return;
    this.lastSeq = e.seq;
    this.events.push(e);
    if (this.live) this.cursor = this.events.length;
  }

  private resetBuffer(): void {
    this.events = [];
    this.cursor = 0;
    this.lastSeq = 0;
    this.header = null;
  }

  /** Scrub to a position. Seeking away from the tail freezes live
   * following; seeking to the end re-enables it. */
  seek(i: number): void {
    this.cursor = Math.max(0, Math.min(this.events.length, Math.trunc(i)));
    this.live = this.cursor >= this.events.length;
  }

  stepBy(d: number): void {
    this.seek(this.cursor + d);
  }

  /** Jump back to the tail and resume live following. */
  seekToEnd(): void {
    this.seek(this.events.length);
  }

  /** Advance `speed` events per PIPELINE_PLAY_TICK_MS until the tail
   * is reached, then resume live following. */
  play(speed = this.speed): void {
    this.speed = Math.max(1, Math.round(speed));
    this.pauseTimer();
    this.playing = true;
    this.live = false;
    this.playTimer = setInterval(() => {
      const next = Math.min(this.events.length, this.cursor + this.speed);
      this.cursor = next;
      if (next >= this.events.length) {
        this.pauseTimer();
        this.live = true;
      }
    }, PIPELINE_PLAY_TICK_MS);
  }

  pause(): void {
    this.pauseTimer();
  }

  private pauseTimer(): void {
    if (this.playTimer !== null) {
      clearInterval(this.playTimer);
      this.playTimer = null;
    }
    this.playing = false;
  }

  /** Open the SSE stream for a run: fresh buffer, live tail-follow.
   * Works for finished runs too — the server replays the recorded
   * log and closes after run_finished. */
  attachLive(runId: string, opts: WatchPipelineEventsOptions = {}): void {
    this.detach();
    this.resetBuffer();
    this.runId = runId;
    this.live = true;
    this.es = this.deps.watch(runId, (e) => this.pushEvent(e), opts);
  }

  /** Close the stream and stop playback. Call from onDestroy. */
  detach(): void {
    this.pauseTimer();
    this.es?.close();
    this.es = null;
  }

  async loadScenarios(): Promise<void> {
    const out = await this.capture(this.deps.listScenarios());
    if (out !== undefined) this.scenarios = out;
  }

  async loadRuns(): Promise<void> {
    const out = await this.capture(this.deps.listRuns());
    if (out !== undefined) this.runs = out;
  }

  async loadRecordings(): Promise<void> {
    const out = await this.capture(this.deps.listRecordings());
    if (out !== undefined) this.recordings = out;
  }

  /** Start a scenario run, refresh the run list, then attach live. */
  async startRun(scenarioId: string): Promise<string | undefined> {
    const started = await this.capture(this.deps.startRunApi(scenarioId));
    if (started === undefined) return undefined;
    void this.loadRuns();
    this.attachLive(started.run_id);
    return started.run_id;
  }

  /** Open an observe window on the daemon's real ingest engine. */
  async startObserve(): Promise<string | undefined> {
    const started = await this.capture(
      this.deps.observeStart(`observe-${Date.now().toString(36)}`),
    );
    if (started === undefined) return undefined;
    void this.loadRuns();
    this.attachLive(started.run_id);
    return started.run_id;
  }

  /** Detach the observe taps server-side. The stream closes itself
   * once the run_finished bookend arrives. */
  async stopObserve(): Promise<void> {
    await this.capture(this.deps.observeStop());
  }

  async resetSandbox(): Promise<void> {
    await this.capture(this.deps.resetSandboxApi());
  }

  /** Upload a .jsonl recording for later replay; refreshes the list. */
  async importRecording(file: File): Promise<string | undefined> {
    const res = await this.capture(this.deps.importRecordingApi(file));
    if (res === undefined) return undefined;
    void this.loadRecordings();
    return res.name;
  }

  /** Load a recording for scrubbed replay — either a server-side
   * recording name (downloaded via the recordings API) or a local
   * File. The buffer lands frozen at the end with live off, so the
   * user scrubs backwards or hits play to watch the run again. */
  async playRecording(source: File | string): Promise<void> {
    const text =
      typeof source === "string"
        ? await (await this.capture(this.deps.downloadRecording(source)))?.text()
        : await source.text();
    if (text === undefined) return;
    let parsed: { header: PipelineRecordingHeader; events: PipelineEvent[] };
    try {
      parsed = parsePipelineRecording(text);
    } catch (cause) {
      this.error = cause instanceof Error ? cause.message : String(cause);
      return;
    }
    this.detach();
    this.resetBuffer();
    this.header = parsed.header;
    this.runId = parsed.header.run_id;
    this.events = parsed.events;
    this.lastSeq = parsed.events.reduce((m, e) => Math.max(m, e.seq), 0);
    this.live = false;
    this.cursor = this.events.length;
  }

  private async capture<T>(p: Promise<T>): Promise<T | undefined> {
    try {
      const out = await p;
      this.error = null;
      return out;
    } catch (cause) {
      this.error = cause instanceof Error ? cause.message : String(cause);
      return undefined;
    }
  }
}

export const pipelineStore = new PipelineStore();
