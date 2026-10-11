// @vitest-environment jsdom
import { describe, expect, it, vi } from "vite-plus/test";
import { fireEvent, render, screen } from "@testing-library/svelte";
import PipelineStatePanel from "./PipelineStatePanel.svelte";
import { reduceEvents, type PipelineEvent } from "../../stores/pipeline.svelte.js";

const A = "/tmp/sandbox/a.jsonl";
const B = "/tmp/sandbox/b.jsonl";

// Two files through the pipeline: A gets classified, gate-passed, parsed
// full, checkpointed, written, then re-parsed incrementally; B only gets
// classified and loses dedup. Gaps at seq 6→7 (+510ms), 11→12 (+300ms)
// and 13→14 (+300ms) stand out from the +10ms baseline.
const EVENTS: PipelineEvent[] = [
  { seq: 1, at_ms: 1000, stage: "watch", kind: "batch_formed" },
  {
    seq: 2,
    at_ms: 1010,
    stage: "resolve",
    kind: "path_classified",
    path: A,
    payload: { classification: "session" },
  },
  {
    seq: 3,
    at_ms: 1020,
    stage: "gates",
    kind: "gate_verdict",
    path: A,
    payload: { gate: "size", decision: "pass", reason: "ok" },
  },
  {
    seq: 4,
    at_ms: 1030,
    stage: "parse",
    kind: "parse_started",
    path: A,
    payload: { mode: "full", size: 1000 },
  },
  {
    seq: 5,
    at_ms: 1040,
    stage: "parse",
    kind: "line_classified",
    path: A,
    payload: { line_no: 1, type: "user", raw: "{}", offset: 400 },
  },
  {
    seq: 6,
    at_ms: 1050,
    stage: "parse",
    kind: "line_classified",
    path: A,
    payload: { line_no: 2, type: "assistant", raw: "{}", offset: 800 },
  },
  {
    seq: 7,
    at_ms: 1560,
    stage: "parse",
    kind: "line_classified",
    path: A,
    payload: { line_no: 3, type: "malformed", raw: "bad", offset: 1000 },
  },
  {
    seq: 8,
    at_ms: 1570,
    stage: "parse",
    kind: "parse_done",
    path: A,
    payload: { mode: "full", messages: 2, consumed_bytes: 1000 },
  },
  {
    seq: 9,
    at_ms: 1580,
    stage: "write",
    kind: "checkpoint_written",
    path: A,
    payload: { offset: 500, checkpoint_version: 3 },
  },
  {
    seq: 10,
    at_ms: 1590,
    stage: "write",
    kind: "session_written",
    path: A,
    payload: { session_id: "sess-1" },
  },
  {
    seq: 11,
    at_ms: 1600,
    stage: "write",
    kind: "messages_written",
    path: A,
    payload: { inserted: 2, max_ordinal: 1 },
  },
  {
    seq: 12,
    at_ms: 1900,
    stage: "resolve",
    kind: "path_classified",
    path: B,
    payload: { classification: "attach" },
  },
  {
    seq: 13,
    at_ms: 1910,
    stage: "batch",
    kind: "dedup_loser",
    path: B,
    payload: { winner: A, reason: "same session" },
  },
  {
    seq: 14,
    at_ms: 2210,
    stage: "parse",
    kind: "parse_started",
    path: A,
    payload: { mode: "incremental", offset: 1000, size: 2000 },
  },
  {
    seq: 15,
    at_ms: 2220,
    stage: "parse",
    kind: "line_classified",
    path: A,
    payload: { line_no: 4, type: "user", raw: "{}", offset: 1600 },
  },
  {
    seq: 16,
    at_ms: 2230,
    stage: "parse",
    kind: "parse_done",
    path: A,
    payload: { mode: "incremental", messages: 1, consumed_bytes: 600 },
  },
];

const VIEW = reduceEvents(EVENTS);

async function openTab(name: string) {
  await fireEvent.click(screen.getByRole("radio", { name }));
}

describe("PipelineStatePanel journey tab", () => {
  it("shows the empty state when no file journeys exist", async () => {
    render(PipelineStatePanel, { props: { view: reduceEvents([]) } });
    await openTab("Journey");
    expect(screen.getByText("No file journeys yet")).toBeTruthy();
    expect(
      screen.getByText("Events with a file path appear here once the pipeline touches a file."),
    ).toBeTruthy();
  });

  it("renders path chips and the first journey's steps with summaries", async () => {
    const { container } = render(PipelineStatePanel, {
      props: { view: VIEW },
    });
    await openTab("Journey");

    const chips = container.querySelectorAll(".path-strip .path-chip");
    expect(chips).toHaveLength(2);
    expect(chips[0]?.textContent).toBe("a.jsonl");
    expect(chips[1]?.textContent).toBe("b.jsonl");
    expect(chips[0]?.classList.contains("is-active")).toBe(true);

    const card = container.querySelector(".journey-card");
    expect(card?.querySelector(".journey-name")?.textContent).toBe("a.jsonl");
    // Classification comes from the first path_classified step.
    expect(card?.querySelector(".journey-class")?.textContent).toBe("classified: session");

    const steps = card?.querySelectorAll(".j-steps .j-step") ?? [];
    expect(steps).toHaveLength(11);
    const text = card?.textContent ?? "";
    expect(text).toContain("size → pass (ok)");
    expect(text).toContain("parse full offset=0 size=1000");
    expect(text).toContain("3 lines classified, 1 malformed");
    expect(text).toContain("checkpoint v3 @500");
    expect(text).toContain("parse incremental offset=1000 size=2000");
    // Folded run of three line_classified events carries the ×N suffix.
    expect(card?.querySelector(".j-folded")?.textContent).toBe("×3");
    // The malformed line flags the folded step as anomalous.
    expect(card?.querySelector(".j-step.anomaly .j-anomaly")?.textContent).toBe("malformed line");
  });

  it("draws the byte map fill and position markers", async () => {
    const { container } = render(PipelineStatePanel, {
      props: { view: VIEW },
    });
    await openTab("Journey");

    // readEnd 1600 of size 2000 → 80% fill.
    const fill = container.querySelector<HTMLElement>(".byte-fill");
    expect(fill?.style.width).toBe("80%");
    // checkpoint @500 → 25%, incremental resume @1000 → 50%.
    const cp = container.querySelector<HTMLElement>(".bm-checkpoint");
    expect(cp?.style.left).toBe("25%");
    expect(cp?.getAttribute("title")).toBe("checkpoint @ 500");
    const inc = container.querySelector<HTMLElement>(".bm-inc");
    expect(inc?.style.left).toBe("50%");
    expect(inc?.getAttribute("title")).toContain("1000");
  });

  it("hides the byte map for a journey with no tracked size", async () => {
    const { container } = render(PipelineStatePanel, {
      props: { view: VIEW },
    });
    await openTab("Journey");
    await fireEvent.click(screen.getByRole("button", { name: "b.jsonl" }));

    expect(container.querySelector(".byte-map")).toBeNull();
    expect(container.querySelector(".journey-class")?.textContent).toBe("classified: attach");
  });

  it("reports the write outcome and anomaly count in the footer", async () => {
    const { container } = render(PipelineStatePanel, {
      props: { view: VIEW },
    });
    await openTab("Journey");

    const foot = container.querySelector(".journey-foot");
    expect(foot?.textContent).toContain("sess-1");
    expect(foot?.textContent).toContain("2");
    expect(foot?.textContent).toContain("inserted");
    expect(foot?.textContent).toContain("checkpoint v3");
    expect(foot?.querySelector(".j-anomalies")?.textContent).toContain("1 anomaly");
  });

  it("switches the card when a path chip is clicked", async () => {
    const { container } = render(PipelineStatePanel, {
      props: { view: VIEW },
    });
    await openTab("Journey");
    await fireEvent.click(screen.getByRole("button", { name: "b.jsonl" }));

    expect(container.querySelector(".journey-name")?.textContent).toBe("b.jsonl");
    const text = container.querySelector(".journey-card")?.textContent ?? "";
    expect(text).toContain("dedup lost: same session");
    expect(text).toContain("dedup lost to a.jsonl");
    expect(text).toContain("1 anomaly");
  });

  it("seeks and selects the step's first event on seq click", async () => {
    const onselect = vi.fn();
    const onseek = vi.fn();
    render(PipelineStatePanel, {
      props: { view: VIEW, onselect, onseek },
    });
    await openTab("Journey");

    // The folded line_classified step points at buffer index 4 (seq 5).
    await fireEvent.click(screen.getByRole("button", { name: "#5" }));
    expect(onseek).toHaveBeenCalledWith(5);
    expect(onselect).toHaveBeenCalledWith(4);
  });
});

describe("PipelineStatePanel largest gaps", () => {
  it("lists the top-5 gaps between consumed events", async () => {
    const { container } = render(PipelineStatePanel, {
      props: { view: VIEW },
    });
    await openTab("Events");

    expect(screen.getByText("Largest gaps")).toBeTruthy();
    const rows = container.querySelectorAll(".gap-list .gap-row");
    expect(rows).toHaveLength(5);
    expect(rows[0]?.textContent).toBe("#6→#7 +510ms line_classified");
    expect(rows[1]?.textContent).toBe("#11→#12 +300ms path_classified");
    expect(rows[2]?.textContent).toBe("#13→#14 +300ms parse_started");
  });

  it("seeks and selects the later event of the gap", async () => {
    const onselect = vi.fn();
    const onseek = vi.fn();
    render(PipelineStatePanel, {
      props: { view: VIEW, onselect, onseek },
    });
    await openTab("Events");

    await fireEvent.click(screen.getByRole("button", { name: /#6→#7/ }));
    expect(onseek).toHaveBeenCalledWith(7);
    expect(onselect).toHaveBeenCalledWith(6);
  });

  it("renders gaps and seq numbers without buttons without callbacks", async () => {
    const { container } = render(PipelineStatePanel, {
      props: { view: VIEW },
    });
    await openTab("Events");
    expect(container.querySelectorAll("button.gap-row")).toHaveLength(0);
    expect(container.querySelectorAll("span.gap-row")).toHaveLength(5);

    await openTab("Journey");
    expect(container.querySelectorAll("button.j-seq")).toHaveLength(0);
    expect(container.querySelectorAll("span.j-seq").length).toBeGreaterThan(0);
  });
});
