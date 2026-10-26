// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { tick } from "svelte";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import PipelinePage from "./PipelinePage.svelte";
import { PipelineStore, type PipelineEvent } from "../../stores/pipeline.svelte.js";

function testStore() {
  return new PipelineStore({
    listScenarios: vi.fn(async () => [
      { id: "append-lines", title: "Append lines", summary: "appends" },
    ]),
    listRuns: vi.fn(async () => []),
    listRecordings: vi.fn(async () => []),
    watch: vi.fn(() => ({ close: vi.fn() })) as never,
  });
}

const EVENTS: PipelineEvent[] = [
  { seq: 1, at_ms: 1000, stage: "watch", kind: "batch_formed" },
  {
    seq: 2,
    at_ms: 1050,
    stage: "parse",
    kind: "parse_started",
    path: "/tmp/sandbox/src/f.jsonl",
    payload: { mode: "full" },
  },
];

describe("PipelinePage", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders the header, diagram and panes without a server", async () => {
    const store = testStore();
    const { container, unmount } = render(PipelinePage, {
      props: { store },
    });

    expect(screen.getByRole("heading", { name: "Pipeline playground" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Run" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Observe" })).toBeTruthy();
    // Diagram: eight stage nodes.
    expect(container.querySelectorAll(".stage-node")).toHaveLength(8);
    // Timeline empty state.
    expect(container.textContent).toContain("No events yet");
    unmount();
  });

  it("lists loaded runs as replay chips and attaches on click", async () => {
    const store = new PipelineStore({
      listScenarios: vi.fn(async () => []),
      listRuns: vi.fn(async () => [
        {
          run_id: "run-abc",
          scenario_id: "append-lines",
          started_at: 1,
          state: "finished",
          event_count: 12,
        },
      ]),
      listRecordings: vi.fn(async () => []),
    });
    const attach = vi.spyOn(store, "attachLive");
    render(PipelinePage, { props: { store } });

    // loadRuns resolves after mount — wait for the chip.
    const chip = await screen.findByRole("button", { name: /run-abc/ });
    await fireEvent.click(chip);
    expect(attach).toHaveBeenCalledWith("run-abc");
  });

  it("restores the toggle when an observe run is still live on mount", async () => {
    // An observe window left open by an earlier visit shows up in the
    // runs list as a running run with scenario "observe" — the page
    // must render "Stop observe" so it can be turned off.
    const store = new PipelineStore({
      listScenarios: vi.fn(async () => []),
      listRuns: vi.fn(async () => [
        {
          run_id: "observe-xyz",
          scenario_id: "observe",
          started_at: 1,
          state: "running",
          event_count: 7,
        },
      ]),
      listRecordings: vi.fn(async () => []),
      watch: vi.fn(() => ({ close: vi.fn() })) as never,
    });
    render(PipelinePage, { props: { store } });

    await screen.findByRole("button", { name: "Stop observe" });
  });

  it("stops the server-side observe tap when unmounted mid-observe", async () => {
    const observeStop = vi.fn(async () => {});
    const store = new PipelineStore({
      listScenarios: vi.fn(async () => []),
      listRuns: vi.fn(async () => []),
      listRecordings: vi.fn(async () => []),
      observeStart: vi.fn(async () => ({ run_id: "observe-1" })),
      observeStop,
      watch: vi.fn(() => ({ close: vi.fn() })) as never,
    });
    const { unmount } = render(PipelinePage, { props: { store } });

    await fireEvent.click(await screen.findByRole("button", { name: "Observe" }));
    await screen.findByRole("button", { name: "Stop observe" });
    unmount();
    expect(observeStop).toHaveBeenCalledTimes(1);
  });

  it("keeps the observe toggle on when stopObserve fails", async () => {
    const store = new PipelineStore({
      listScenarios: vi.fn(async () => []),
      listRuns: vi.fn(async () => [
        {
          run_id: "observe-xyz",
          scenario_id: "observe",
          started_at: 1,
          state: "running",
          event_count: 7,
        },
      ]),
      listRecordings: vi.fn(async () => []),
      observeStop: vi.fn(async () => {
        throw new Error("boom");
      }),
      watch: vi.fn(() => ({ close: vi.fn() })) as never,
    });
    render(PipelinePage, { props: { store } });

    const stop = await screen.findByRole("button", { name: "Stop observe" });
    await fireEvent.click(stop);
    // Once the failed DELETE has landed in the error strip, the toggle
    // must still offer "Stop observe" — the server tap is still live.
    await screen.findByRole("alert");
    expect(store.error).toMatch(/boom/);
    expect(screen.getByRole("button", { name: "Stop observe" })).toBeTruthy();
  });

  it("renders the inspector panel below the timeline", () => {
    const store = testStore();
    const { container, unmount } = render(PipelinePage, {
      props: { store },
    });

    const inspector = container.querySelector(".inspector");
    expect(inspector).not.toBeNull();
    expect(inspector?.textContent).toContain("Click an event to inspect it.");
    unmount();
  });

  it("selects an event through the store when a timeline row is clicked", async () => {
    const store = testStore();
    store.events = EVENTS;
    store.cursor = EVENTS.length;
    const { container, unmount } = render(PipelinePage, {
      props: { store },
    });

    const row = container.querySelector('.timeline-row[data-idx="1"]');
    expect(row).not.toBeNull();
    await fireEvent.click(row!);
    expect(store.selected).toBe(1);

    const head = container.querySelector(".inspector-head");
    expect(head?.textContent).toContain("#2");
    expect(head?.textContent).toContain("parse_started");
    unmount();
  });

  it("shows the dropped counter only when the store reports losses", async () => {
    const store = testStore();
    const { container, unmount } = render(PipelinePage, {
      props: { store },
    });

    expect(container.textContent).not.toContain("events lost in transport");

    store.dropped = 3;
    await tick();
    expect(container.textContent).toContain("3 events lost in transport");
    unmount();
  });

  it("drives replay mode, tempo and pause-on-anomaly from the transport bar", async () => {
    const store = testStore();
    const { unmount } = render(PipelinePage, { props: { store } });

    const modes = screen.getByRole("radiogroup", { name: /replay mode/i });
    await fireEvent.click(within(modes).getByRole("radio", { name: "Time" }));
    expect(store.playMode).toBe("time");

    // The tempo control only exists in time mode.
    const tempo = await screen.findByRole("radiogroup", {
      name: /speed multiplier/i,
    });
    await fireEvent.click(within(tempo).getByRole("radio", { name: "100x" }));
    expect(store.rtSpeed).toBe(100);

    await fireEvent.click(screen.getByRole("button", { name: "Pause on anomaly" }));
    expect(store.pauseOnAnomaly).toBe(true);

    await fireEvent.click(within(modes).getByRole("radio", { name: "Events" }));
    expect(store.playMode).toBe("events");
    await tick();
    expect(screen.queryByRole("radiogroup", { name: /speed multiplier/i })).toBeNull();
    unmount();
  });

  it("hides the timeline's events-per-tick speed group in time mode", async () => {
    // In time mode pace comes from rtSpeed, so the events-per-tick
    // control is removed rather than left inert — a click reaching
    // play() would restart the interval and reset playheadMs to the
    // cursor event's at_ms, rewinding up to a tick of progress.
    const store = testStore();
    store.events = EVENTS;
    store.playMode = "time";
    const { unmount } = render(PipelinePage, { props: { store } });
    await tick();
    expect(screen.queryByRole("radiogroup", { name: /events per tick/i })).toBeNull();

    // Back in events mode the control returns and sets store.speed.
    store.playMode = "events";
    await tick();
    const speed = screen.getByRole("radiogroup", {
      name: /events per tick/i,
    });
    await fireEvent.click(within(speed).getByRole("radio", { name: "20x" }));
    expect(store.speed).toBe(20);

    unmount();
  });
});

function localCapture(runId = "local-a"): string {
  return [
    { kind: "header", format_version: 1, run_id: runId, started_at_ms: 1000 },
    { seq: 11, at_ms: 1000, stage: "finalize", kind: "run_started" },
    { seq: 12, at_ms: 1200, stage: "finalize", kind: "run_finished" },
  ]
    .map((item) => JSON.stringify(item))
    .join("\n");
}

function recordingFile(text: string): File {
  const file = new File([text], "capture.jsonl", { type: "application/x-ndjson" });
  // jsdom versions without Blob.text still exercise the actual File input.
  Object.defineProperty(file, "text", { value: async () => text });
  return file;
}

describe("PipelinePage local recordings", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("opens a local file without calling the upload or download API", async () => {
    const upload = vi.fn(async () => ({ name: "uploaded.jsonl" }));
    const download = vi.fn(async () => new Response(localCapture()));
    const store = new PipelineStore({
      listScenarios: async () => [],
      listRuns: async () => [],
      listRecordings: async () => [],
      importRecordingApi: upload,
      downloadRecording: download,
    });
    const { unmount } = render(PipelinePage, { props: { store } });
    const input = screen.getByLabelText("Local pipeline recording") as HTMLInputElement;
    await fireEvent.change(input, { target: { files: [recordingFile(localCapture())] } });
    await waitFor(() => expect(store.runId).toBe("local-a"));
    expect(store.events).toHaveLength(2);
    expect(store.cursor).toBe(2);
    expect(upload).not.toHaveBeenCalled();
    expect(download).not.toHaveBeenCalled();
    expect(input.value).toBe("");
    unmount();
  });

  it("retains the current recording when local validation fails", async () => {
    const store = testStore();
    await store.playRecording(recordingFile(localCapture("good")));
    const { unmount } = render(PipelinePage, { props: { store } });
    await tick();
    await fireEvent.change(screen.getByLabelText("Local pipeline recording"), {
      target: { files: [recordingFile("null\n")] },
    });
    await screen.findByRole("alert");
    expect(store.runId).toBe("good");
    expect(store.events).toHaveLength(2);
    unmount();
  });

  it("shows progress while a local read is pending", async () => {
    let finish!: (text: string) => void;
    const pending = new Promise<string>((resolve) => {
      finish = resolve;
    });
    const file = new File(["data"], "slow.jsonl");
    Object.defineProperty(file, "text", { value: () => pending });
    const store = testStore();
    const { unmount } = render(PipelinePage, { props: { store } });
    await fireEvent.change(screen.getByLabelText("Local pipeline recording"), {
      target: { files: [file] },
    });
    expect(screen.getByText("Reading recording\u2026")).toBeTruthy();
    finish(localCapture());
    await waitFor(() => expect(store.recordingLoading).toBe(false));
    expect(screen.queryByText("Reading recording\u2026")).toBeNull();
    unmount();
  });

  it("exports every captured event, keeps replay position, and releases the object URL", async () => {
    const store = testStore();
    await store.playRecording(recordingFile(localCapture()));
    store.seek(1);
    const exported = vi.spyOn(store, "exportRecording");
    const createObjectURL = vi.fn(() => "blob:recording");
    const revokeObjectURL = vi.fn();
    class DownloadURL extends URL {
      static override createObjectURL = createObjectURL;
      static override revokeObjectURL = revokeObjectURL;
    }
    vi.stubGlobal("URL", DownloadURL);
    let downloadName = "";
    const click = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(function (this: HTMLAnchorElement) {
        downloadName = this.download;
      });
    const { unmount } = render(PipelinePage, { props: { store } });
    vi.useFakeTimers();
    await fireEvent.click(screen.getByRole("button", { name: "Export capture" }));
    expect(exported).toHaveBeenCalledTimes(1);
    const snapshot = exported.mock.results[0]!.value as { text: string };
    expect(snapshot.text.trim().split("\n")).toHaveLength(3);
    expect(downloadName).toBe("pipeline-local-a.jsonl");
    expect(store.cursor).toBe(1);
    expect(click).toHaveBeenCalledTimes(1);
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    expect(document.querySelector('a[download="pipeline-local-a.jsonl"]')).toBeNull();
    vi.advanceTimersByTime(1000);
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:recording");
    unmount();
  });

  it("does not let replay seeking alter the pinned comparison totals", async () => {
    const store = testStore();
    await store.playRecording(recordingFile(localCapture()));
    const { unmount } = render(PipelinePage, { props: { store } });
    await fireEvent.click(screen.getByRole("button", { name: "Pin as baseline" }));
    store.seek(0);
    await tick();
    const table = screen.getByRole("table", { name: "Metric" });
    const header = within(table).getByRole("rowheader", { name: "Events" });
    expect(
      within(header.closest("tr")!)
        .getAllByRole("cell")
        .map((cell) => cell.textContent),
    ).toEqual(["2", "2", "0"]);
    unmount();
  });

  it("keeps the existing Import action explicitly server-side", async () => {
    const upload = vi.fn(async () => ({ name: "uploaded.jsonl" }));
    const download = vi.fn(async () => new Response(localCapture("uploaded")));
    const store = new PipelineStore({
      listScenarios: async () => [],
      listRuns: async () => [],
      listRecordings: async () => [],
      importRecordingApi: upload,
      downloadRecording: download,
    });
    const { container, unmount } = render(PipelinePage, { props: { store } });
    const input = container.querySelector('input[type="file"]:not([aria-label])')!;
    const file = recordingFile(localCapture());
    await fireEvent.change(input, { target: { files: [file] } });
    await waitFor(() => expect(store.runId).toBe("uploaded"));
    expect(upload).toHaveBeenCalledWith(file);
    expect(download).toHaveBeenCalledWith("uploaded.jsonl");
    unmount();
  });
});
