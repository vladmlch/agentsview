// @vitest-environment jsdom
import { describe, expect, it, vi } from "vite-plus/test";
import { fireEvent, render, screen, within } from "@testing-library/svelte";
import PipelineTimeline from "./PipelineTimeline.svelte";
import type { PipelineEvent } from "../../stores/pipeline.svelte.js";

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
  {
    seq: 3,
    at_ms: 1070,
    stage: "parse",
    kind: "line_classified",
    path: "/tmp/sandbox/src/f.jsonl",
    payload: { line_no: 1, type: "malformed", raw: "not json" },
  },
  {
    seq: 4,
    at_ms: 1090,
    stage: "parse",
    kind: "parse_done",
    path: "/tmp/sandbox/src/f.jsonl",
    payload: { mode: "full", messages: 0 },
  },
  {
    seq: 5,
    at_ms: 1120,
    stage: "batch",
    kind: "dedup_loser",
    path: "/tmp/sandbox/src/g.jsonl",
    payload: { winner: "/tmp/sandbox/src/f.jsonl" },
  },
];

async function openFilters(container: HTMLElement) {
  await fireEvent.click(screen.getByRole("button", { name: "Toggle event filters" }));
  const panel = container.querySelector<HTMLElement>(".timeline-filters");
  expect(panel).not.toBeNull();
  return within(panel!);
}

describe("PipelineTimeline", () => {
  it("renders one row per event with seq, dt, stage, kind and path basename", () => {
    const { container } = render(PipelineTimeline, {
      props: { events: EVENTS, cursor: 5 },
    });
    const rows = container.querySelectorAll(".timeline-row");
    expect(rows).toHaveLength(5);
    expect(rows[1]?.textContent).toContain("#2");
    expect(rows[1]?.textContent).toContain("+50ms");
    expect(rows[1]?.textContent).toContain("parse");
    expect(rows[1]?.textContent).toContain("parse_started");
    expect(rows[1]?.textContent).toContain("f.jsonl");
    expect(rows[1]?.textContent).not.toContain("/tmp/sandbox");
  });

  it("selects a row on single click without seeking", async () => {
    const onselect = vi.fn();
    const onseek = vi.fn();
    const { container } = render(PipelineTimeline, {
      props: { events: EVENTS, cursor: 1, selected: 1, onselect, onseek },
    });
    const row = container.querySelector<HTMLButtonElement>('[data-idx="1"]');
    await fireEvent.click(row!);
    expect(onselect).toHaveBeenCalledWith(1);
    expect(onseek).not.toHaveBeenCalled();
    expect(row?.classList.contains("is-selected")).toBe(true);
    expect(row?.getAttribute("title")).toContain("click inspects, double-click seeks");
  });

  it("seeks so the double-clicked event becomes the latest consumed", async () => {
    const onseek = vi.fn();
    const { container } = render(PipelineTimeline, {
      props: { events: EVENTS, cursor: 1, onseek },
    });
    const row = container.querySelector<HTMLButtonElement>('[data-idx="1"]');
    await fireEvent.dblClick(row!);
    expect(onseek).toHaveBeenCalledWith(2);
  });

  it("splits consumed from pending rows at the cursor", () => {
    const { container } = render(PipelineTimeline, {
      props: { events: EVENTS, cursor: 1, live: false },
    });
    expect(container.querySelector('[data-idx="0"]')?.classList.contains("consumed")).toBe(true);
    expect(container.querySelector('[data-idx="1"]')?.classList.contains("pending")).toBe(true);
    // The cursor marker lands right before the first pending row.
    const marker = container.querySelector(".cursor-line");
    expect(marker?.nextElementSibling?.getAttribute("data-idx")).toBe("1");
  });

  it("filters rows by stage while keeping real seek indexes", async () => {
    const onseek = vi.fn();
    const { container } = render(PipelineTimeline, {
      props: { events: EVENTS, cursor: 0, filter: "parse" as const, onseek },
    });
    const rows = container.querySelectorAll(".timeline-row");
    expect(rows).toHaveLength(3);
    await fireEvent.dblClick(container.querySelector('[data-idx="3"]')!);
    expect(onseek).toHaveBeenCalledWith(4);
  });

  it("wires transport controls to their callbacks", async () => {
    const onstep = vi.fn();
    const onplaypause = vi.fn();
    const onspeed = vi.fn();
    render(PipelineTimeline, {
      props: {
        events: EVENTS,
        cursor: 1,
        live: false,
        onstep,
        onplaypause,
        onspeed,
      },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Step back one event" }));
    expect(onstep).toHaveBeenCalledWith(-1);
    await fireEvent.click(screen.getByRole("button", { name: "Step forward one event" }));
    expect(onstep).toHaveBeenCalledWith(1);
    await fireEvent.click(screen.getByRole("button", { name: /Play/ }));
    expect(onplaypause).toHaveBeenCalled();
    await fireEvent.click(screen.getByRole("radio", { name: "5x" }));
    expect(onspeed).toHaveBeenCalledWith(5);
  });

  it("hides the events-per-tick speed group under wall-clock pacing", () => {
    const { unmount } = render(PipelineTimeline, {
      props: { events: EVENTS, cursor: 1, live: false, pacing: "time" },
    });
    expect(screen.queryByRole("radiogroup", { name: /events per tick/i })).toBeNull();
    unmount();

    // Explicit events pacing — and the default above — keep it visible.
    render(PipelineTimeline, {
      props: { events: EVENTS, cursor: 1, live: false, pacing: "events" },
    });
    expect(screen.getByRole("radiogroup", { name: /events per tick/i })).toBeTruthy();
  });

  it("disables stepping past the buffer ends", () => {
    render(PipelineTimeline, {
      props: { events: EVENTS, cursor: 0, live: false },
    });
    expect(
      screen.getByRole("button", { name: "Step back one event" }).hasAttribute("disabled"),
    ).toBe(true);
  });

  it("narrows the list with the kindFilter set", () => {
    const { container } = render(PipelineTimeline, {
      props: {
        events: EVENTS,
        cursor: 0,
        live: false,
        kindFilter: new Set(["line_classified", "dedup_loser"]),
      },
    });
    expect(container.querySelectorAll(".timeline-row")).toHaveLength(2);
    expect(container.querySelector('[data-idx="2"]')).not.toBeNull();
    expect(container.querySelector('[data-idx="4"]')).not.toBeNull();
  });

  it("toggles kinds through chips grouped by stage", async () => {
    const onkindchange = vi.fn();
    const { container } = render(PipelineTimeline, {
      props: { events: EVENTS, cursor: 0, live: false, onkindchange },
    });
    const panel = await openFilters(container);
    // Groups follow PIPELINE_STAGES order, kinds in first-seen order.
    const stages = [...container.querySelectorAll(".kind-stage")].map((el) => el.textContent);
    expect(stages).toEqual(["watch", "parse", "batch"]);
    await fireEvent.click(panel.getByRole("button", { name: "line_classified" }));
    expect(onkindchange).toHaveBeenCalledWith(new Set(["line_classified"]));
  });

  it("removes a kind that is already in the filter", async () => {
    const onkindchange = vi.fn();
    const { container } = render(PipelineTimeline, {
      props: {
        events: EVENTS,
        cursor: 0,
        live: false,
        kindFilter: new Set(["line_classified", "dedup_loser"]),
        onkindchange,
      },
    });
    const panel = await openFilters(container);
    await fireEvent.click(panel.getByRole("button", { name: "line_classified" }));
    expect(onkindchange).toHaveBeenCalledWith(new Set(["dedup_loser"]));
  });

  it("narrows the list with a case-insensitive query over kind, path and payload", () => {
    const { container } = render(PipelineTimeline, {
      props: { events: EVENTS, cursor: 0, live: false, query: "G.JSONL" },
    });
    const rows = container.querySelectorAll(".timeline-row");
    expect(rows).toHaveLength(1);
    expect(rows[0]?.getAttribute("data-idx")).toBe("4");
  });

  it("reports search input through onquerychange", async () => {
    const onquerychange = vi.fn();
    const { container } = render(PipelineTimeline, {
      props: { events: EVENTS, cursor: 0, live: false, onquerychange },
    });
    const panel = await openFilters(container);
    const input = panel.getByRole("searchbox", { name: "Filter events by text" });
    await fireEvent.input(input, { target: { value: "dedup" } });
    expect(onquerychange).toHaveBeenCalledWith("dedup");
  });

  it("narrows the list to anomalies and toggles through the chip", async () => {
    const onanomalieschange = vi.fn();
    const { container } = render(PipelineTimeline, {
      props: {
        events: EVENTS,
        cursor: 0,
        live: false,
        anomaliesOnly: true,
        onanomalieschange,
      },
    });
    const rows = container.querySelectorAll(".timeline-row");
    expect(rows).toHaveLength(2);
    expect(container.querySelector('[data-idx="2"]')).not.toBeNull();
    expect(container.querySelector('[data-idx="4"]')).not.toBeNull();
    // With only anomalies listed, the buffer-wide counter is hidden.
    expect(container.querySelector(".timeline-anomalies")).toBeNull();

    const panel = await openFilters(container);
    await fireEvent.click(panel.getByRole("button", { name: "anomalies" }));
    expect(onanomalieschange).toHaveBeenCalledWith(false);
  });

  it("marks anomalous rows with the anomaly class and reason in the title", () => {
    const { container } = render(PipelineTimeline, {
      props: { events: EVENTS, cursor: 5 },
    });
    const anomalyRow = container.querySelector('[data-idx="2"]');
    expect(anomalyRow?.classList.contains("anomaly")).toBe(true);
    expect(anomalyRow?.getAttribute("title")).toContain("malformed line");
    const routineRow = container.querySelector('[data-idx="0"]');
    expect(routineRow?.classList.contains("anomaly")).toBe(false);
    expect(routineRow?.getAttribute("title")).not.toContain("anomaly:");
  });

  it("jumps to the previous and next matching row around the cursor", async () => {
    const onseek = vi.fn();
    const onselect = vi.fn();
    const { container } = render(PipelineTimeline, {
      props: {
        events: EVENTS,
        cursor: 2,
        live: false,
        kindFilter: new Set(["parse_started", "dedup_loser"]),
        onseek,
        onselect,
      },
    });
    const panel = await openFilters(container);
    // Matches: idx 1 (parse_started) and idx 4 (dedup_loser); cursor 2 sits
    // between them.
    await fireEvent.click(panel.getByRole("button", { name: "Next matching event" }));
    expect(onseek).toHaveBeenCalledWith(5);
    expect(onselect).toHaveBeenCalledWith(4);
    await fireEvent.click(panel.getByRole("button", { name: "Previous matching event" }));
    expect(onseek).toHaveBeenCalledWith(2);
    expect(onselect).toHaveBeenCalledWith(1);
  });

  it("skips the selected row when finding the previous match", async () => {
    const onseek = vi.fn();
    const onselect = vi.fn();
    const { container } = render(PipelineTimeline, {
      props: {
        events: EVENTS,
        cursor: 3,
        live: false,
        selected: 2,
        onseek,
        onselect,
      },
    });
    const panel = await openFilters(container);
    // idx 2 is selected and sits right below the cursor — "previous" must
    // walk past it to idx 1, not land on the row itself.
    await fireEvent.click(panel.getByRole("button", { name: "Previous matching event" }));
    expect(onseek).toHaveBeenCalledWith(2);
    expect(onselect).toHaveBeenCalledWith(1);
  });

  it("shows the anomaly count and the dropped-events counter", () => {
    const { container } = render(PipelineTimeline, {
      props: { events: EVENTS, cursor: 5, dropped: 3 },
    });
    expect(container.querySelector(".timeline-anomalies")?.textContent).toContain("2");
    expect(container.textContent).toContain("3 events lost in transport");
  });
});
