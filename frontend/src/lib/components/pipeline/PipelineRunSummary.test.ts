// @vitest-environment jsdom
import { describe, expect, it } from "vite-plus/test";
import { fireEvent, render, screen, within } from "@testing-library/svelte";
import type { PipelineEvent } from "../../api/client.js";
import PipelineRunSummary from "./PipelineRunSummary.svelte";

const A: PipelineEvent[] = [
  {
    seq: 101,
    at_ms: 1000,
    stage: "finalize",
    kind: "run_started",
    payload: { scenario_id: "cold" },
  },
  { seq: 102, at_ms: 1100, stage: "finalize", kind: "run_finished" },
];
const B: PipelineEvent[] = [
  {
    seq: 201,
    at_ms: 2000,
    stage: "finalize",
    kind: "run_started",
    payload: { scenario_id: "append" },
  },
  {
    seq: 202,
    at_ms: 2050,
    stage: "parse",
    kind: "line_classified",
    payload: { type: "malformed" },
  },
  { seq: 203, at_ms: 2200, stage: "finalize", kind: "run_finished" },
];

function metricCells(name: string): string[] {
  const table = screen.getByRole("table", { name: "Metric" });
  const heading = within(table).getByRole("rowheader", { name });
  return within(heading.closest("tr")!)
    .getAllByRole("cell")
    .map((cell) => cell.textContent ?? "");
}

describe("PipelineRunSummary", () => {
  it("pins A, compares B, and clears the baseline without altering B", async () => {
    const { rerender } = render(PipelineRunSummary, {
      props: { events: A, runId: "a", header: null },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Pin as baseline" }));
    await rerender({ events: B, runId: "b", header: null });
    expect(metricCells("Events")).toEqual(["3", "2", "+1"]);
    expect(metricCells("Anomalies")).toEqual(["1", "0", "+1"]);
    expect(metricCells("Observed span (ms)")).toEqual(["200", "100", "+100"]);
    expect(screen.getByText(/Different scenarios: current append, baseline cold/)).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Clear baseline" }));
    expect(metricCells("Events")).toEqual(["3"]);
    expect(screen.queryByText(/Different scenarios:/)).toBeNull();
  });

  it("keeps a pinned snapshot fixed as more events arrive", async () => {
    const { rerender } = render(PipelineRunSummary, {
      props: { events: A, runId: "a", header: null },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Pin as baseline" }));
    await rerender({ events: [...A, { ...A[1]!, seq: 103 }], runId: "a", header: null });
    expect(metricCells("Events")).toEqual(["3", "2", "+1"]);
  });

  it("warns about an incomplete baseline even when the current capture is complete", async () => {
    const { rerender } = render(PipelineRunSummary, {
      props: { events: A.slice(0, 1), runId: "a", header: null },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Pin as baseline" }));
    await rerender({ events: B, runId: "b", header: null });
    expect(screen.getByText(/Partial capture: a run bookend is missing/)).toBeTruthy();
  });

  it("does not offer an empty capture as a baseline", () => {
    render(PipelineRunSummary, { props: { events: [], runId: null, header: null } });
    expect(
      (screen.getByRole("button", { name: "Pin as baseline" }) as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(screen.queryByText(/Partial capture:/)).toBeNull();
  });
});
