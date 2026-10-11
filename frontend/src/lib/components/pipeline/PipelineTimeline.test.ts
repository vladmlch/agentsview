// @vitest-environment jsdom
import { describe, expect, it, vi } from "vite-plus/test";
import { fireEvent, render, screen } from "@testing-library/svelte";
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
    payload: { line_no: 1, type: "session_meta", raw: "{}" },
  },
];

describe("PipelineTimeline", () => {
  it("renders one row per event with seq, dt, stage, kind and path basename", () => {
    const { container } = render(PipelineTimeline, {
      props: { events: EVENTS, cursor: 3 },
    });
    const rows = container.querySelectorAll(".timeline-row");
    expect(rows).toHaveLength(3);
    expect(rows[1]?.textContent).toContain("#2");
    expect(rows[1]?.textContent).toContain("+50ms");
    expect(rows[1]?.textContent).toContain("parse");
    expect(rows[1]?.textContent).toContain("parse_started");
    expect(rows[1]?.textContent).toContain("f.jsonl");
    expect(rows[1]?.textContent).not.toContain("/tmp/sandbox");
  });

  it("seeks so the clicked event becomes the latest consumed", async () => {
    const onseek = vi.fn();
    const { container } = render(PipelineTimeline, {
      props: { events: EVENTS, cursor: 1, onseek },
    });
    const row = container.querySelector<HTMLButtonElement>('[data-idx="1"]');
    await fireEvent.click(row!);
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
    expect(rows).toHaveLength(2);
    await fireEvent.click(container.querySelector('[data-idx="2"]')!);
    expect(onseek).toHaveBeenCalledWith(3);
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

  it("disables stepping past the buffer ends", () => {
    render(PipelineTimeline, {
      props: { events: EVENTS, cursor: 0, live: false },
    });
    expect(
      screen.getByRole("button", { name: "Step back one event" }).hasAttribute("disabled"),
    ).toBe(true);
  });
});
