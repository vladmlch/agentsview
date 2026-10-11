// @vitest-environment jsdom
import { describe, expect, it, vi } from "vite-plus/test";
import { fireEvent, render, screen } from "@testing-library/svelte";
import PipelinePage from "./PipelinePage.svelte";
import { PipelineStore } from "../../stores/pipeline.svelte.js";

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
});
