// @vitest-environment jsdom
import { describe, expect, it, vi } from "vite-plus/test";
import { fireEvent, render, screen } from "@testing-library/svelte";
import PipelineDiagram from "./PipelineDiagram.svelte";
import type { PipelineStage, PipelineStageStatus } from "../../stores/pipeline.svelte.js";

function status(
  overrides: Partial<Record<PipelineStage, PipelineStageStatus>> = {},
): Record<PipelineStage, PipelineStageStatus> {
  return {
    watch: "idle",
    resolve: "idle",
    gates: "idle",
    parse: "idle",
    batch: "idle",
    write: "idle",
    finalize: "idle",
    embed: "idle",
    ...overrides,
  };
}

describe("PipelineDiagram", () => {
  it("renders all eight pipeline stages, embed as the satellite", () => {
    const { container } = render(PipelineDiagram, {
      stageStatus: status(),
    });
    const nodes = container.querySelectorAll<HTMLButtonElement>(".stage-node");
    expect(nodes).toHaveLength(8);
    expect(Array.from(nodes).map((n) => n.dataset.stage)).toEqual([
      "watch",
      "resolve",
      "gates",
      "parse",
      "batch",
      "write",
      "finalize",
      "embed",
    ]);
  });

  it("marks the active and done stages from stageStatus", () => {
    const { container } = render(PipelineDiagram, {
      stageStatus: status({ watch: "done", parse: "active" }),
    });
    expect(container.querySelector('[data-stage="watch"]')?.classList.contains("is-done")).toBe(
      true,
    );
    expect(container.querySelector('[data-stage="parse"]')?.classList.contains("is-active")).toBe(
      true,
    );
    expect(container.querySelector('[data-stage="embed"]')?.classList.contains("is-active")).toBe(
      false,
    );
  });

  it("emits the stage on click and null on re-click of the selected stage", async () => {
    const onselect = vi.fn();
    const { rerender } = render(PipelineDiagram, {
      stageStatus: status(),
      selected: null,
      onselect,
    });

    await fireEvent.click(screen.getByRole("button", { name: "parse" }));
    expect(onselect).toHaveBeenLastCalledWith("parse");

    await rerender({ stageStatus: status(), selected: "parse" as PipelineStage, onselect });
    expect(screen.getByRole("button", { name: "parse" }).getAttribute("aria-pressed")).toBe("true");

    await fireEvent.click(screen.getByRole("button", { name: "parse" }));
    expect(onselect).toHaveBeenLastCalledWith(null);
  });
});
