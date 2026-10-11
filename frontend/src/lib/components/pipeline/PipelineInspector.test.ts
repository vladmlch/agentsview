// @vitest-environment jsdom
import { describe, expect, it, vi } from "vite-plus/test";
import { fireEvent, render, screen } from "@testing-library/svelte";
import PipelineInspector from "./PipelineInspector.svelte";
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
  {
    seq: 6,
    at_ms: 1150,
    stage: "parse",
    kind: "parse_started",
    path: "/tmp/sandbox/src/h.jsonl",
    payload: { mode: "incremental" },
  },
];

const EMPTY_TEXT = "Click an event to inspect it. Double-click seeks the cursor.";

describe("PipelineInspector", () => {
  it("shows the empty state when nothing is selected", () => {
    const { container } = render(PipelineInspector, {
      props: { events: EVENTS, selected: null },
    });
    expect(container.querySelector(".inspector")).not.toBeNull();
    expect(container.textContent).toContain(EMPTY_TEXT);
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("shows the empty state for a selection outside the buffer", () => {
    const { container } = render(PipelineInspector, {
      props: { events: EVENTS, selected: 99 },
    });
    expect(container.textContent).toContain(EMPTY_TEXT);
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("renders seq, stage, kind, path basename, time and delta", () => {
    const { container } = render(PipelineInspector, {
      props: { events: EVENTS, selected: 2 },
    });
    const head = container.querySelector(".inspector-head");
    expect(head?.textContent).toContain("#3");
    expect(head?.textContent).toContain("parse");
    expect(head?.textContent).toContain("line_classified");
    expect(head?.textContent).toContain("f.jsonl");
    expect(head?.textContent).not.toContain("/tmp/sandbox");
    expect(head?.textContent).toContain(new Date(1070).toLocaleTimeString());
    // Delta to the previous buffer event: 1070 - 1050.
    expect(head?.textContent).toContain("+20ms");
  });

  it("reports +0ms for the first buffer event", () => {
    const { container } = render(PipelineInspector, {
      props: { events: EVENTS, selected: 0 },
    });
    expect(container.querySelector(".inspector-time")?.textContent).toContain("+0ms");
  });

  it("renders the pretty-printed payload, or 'no payload' without one", async () => {
    const { container, rerender } = render(PipelineInspector, {
      props: { events: EVENTS, selected: 1 },
    });
    const payload = container.querySelector(".inspector-payload");
    expect(payload?.textContent).toBe(JSON.stringify(EVENTS[1]!.payload, null, 2));
    expect(payload?.textContent).toContain('"mode": "full"');

    await rerender({ events: EVENTS, selected: 0 });
    const empty = container.querySelector(".inspector-payload");
    expect(empty?.textContent).toBe("no payload");
  });

  it("shows the anomaly reason only for anomalous events", async () => {
    const { container, rerender } = render(PipelineInspector, {
      props: { events: EVENTS, selected: 2 },
    });
    const anomaly = container.querySelector(".inspector-anomaly");
    expect(anomaly?.textContent).toContain("malformed line");

    await rerender({ events: EVENTS, selected: 1 });
    expect(container.querySelector(".inspector-anomaly")).toBeNull();
  });

  it("navigates to the previous and next event in the same file", async () => {
    const onselect = vi.fn();
    const onseek = vi.fn();
    render(PipelineInspector, {
      props: { events: EVENTS, selected: 2, onselect, onseek },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Previous event in file" }));
    expect(onselect).toHaveBeenCalledWith(1);
    await fireEvent.click(screen.getByRole("button", { name: "Next event in file" }));
    expect(onselect).toHaveBeenCalledWith(3);
    // In-file navigation only moves the selection — it never seeks.
    expect(onseek).not.toHaveBeenCalled();
  });

  it("navigates to the previous and next event of the same kind", async () => {
    const onselect = vi.fn();
    const onseek = vi.fn();
    render(PipelineInspector, {
      props: { events: EVENTS, selected: 1, onselect, onseek },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Next event of kind" }));
    expect(onselect).toHaveBeenCalledWith(5);
    expect(onseek).not.toHaveBeenCalled();
  });

  it("disables navigation at the buffer bounds and without a path", async () => {
    const { rerender } = render(PipelineInspector, {
      props: { events: EVENTS, selected: 1 },
    });
    // parse_started at idx 1: no earlier same-kind or same-path event.
    expect(
      screen.getByRole("button", { name: "Previous event of kind" }).hasAttribute("disabled"),
    ).toBe(true);
    expect(
      screen.getByRole("button", { name: "Previous event in file" }).hasAttribute("disabled"),
    ).toBe(true);

    await rerender({ events: EVENTS, selected: 5 });
    // parse_started at idx 5 is the last event and h.jsonl is unique.
    expect(
      screen.getByRole("button", { name: "Next event of kind" }).hasAttribute("disabled"),
    ).toBe(true);
    expect(
      screen.getByRole("button", { name: "Next event in file" }).hasAttribute("disabled"),
    ).toBe(true);
    expect(
      screen.getByRole("button", { name: "Previous event in file" }).hasAttribute("disabled"),
    ).toBe(true);

    await rerender({ events: EVENTS, selected: 0 });
    // batch_formed carries no path — it has no file neighbours at all.
    expect(
      screen.getByRole("button", { name: "Previous event in file" }).hasAttribute("disabled"),
    ).toBe(true);
    expect(
      screen.getByRole("button", { name: "Next event in file" }).hasAttribute("disabled"),
    ).toBe(true);
  });

  it("seeks to just after the event via 'Seek here'", async () => {
    const onseek = vi.fn();
    const onselect = vi.fn();
    render(PipelineInspector, {
      props: { events: EVENTS, selected: 2, onseek, onselect },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Seek here" }));
    expect(onseek).toHaveBeenCalledWith(3);
    expect(onselect).not.toHaveBeenCalled();
  });

  it("clears the selection via the close button", async () => {
    const onselect = vi.fn();
    render(PipelineInspector, {
      props: { events: EVENTS, selected: 2, onselect },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Close inspector" }));
    expect(onselect).toHaveBeenCalledWith(null);
  });

  it("copies the event JSON without a clipboard API", async () => {
    // jsdom may not define navigator.clipboard — the click must not throw.
    render(PipelineInspector, {
      props: { events: EVENTS, selected: 2 },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Copy event JSON" }));
  });

  it("writes the event JSON to the clipboard when available", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    try {
      render(PipelineInspector, {
        props: { events: EVENTS, selected: 2 },
      });
      await fireEvent.click(screen.getByRole("button", { name: "Copy event JSON" }));
      expect(writeText).toHaveBeenCalledWith(JSON.stringify(EVENTS[2], null, 2));
    } finally {
      delete (navigator as { clipboard?: unknown }).clipboard;
    }
  });
});
