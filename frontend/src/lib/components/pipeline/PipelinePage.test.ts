// @vitest-environment jsdom
import { describe, expect, it, vi } from "vite-plus/test";
import { tick } from "svelte";
import { fireEvent, render, screen, within } from "@testing-library/svelte";
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
});
