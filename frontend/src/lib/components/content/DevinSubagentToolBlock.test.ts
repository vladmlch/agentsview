// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { mount, tick, unmount, type ComponentProps } from "svelte";
import type { DbToolCall as ToolCall } from "../../api/generated/index.js";
import { setLocale } from "../../i18n/index.js";
import { displayToolName } from "../../utils/toolDisplay.js";
import ToolBlock from "./ToolBlock.svelte";

const copyToClipboardMock = vi.hoisted(() => vi.fn().mockResolvedValue(true));
const searchState = vi.hoisted(() => ({ active: false, current: "", query: "" }));
vi.mock("../../utils/clipboard.js", () => ({ copyToClipboard: copyToClipboardMock }));
vi.mock("../../stores/inSessionSearch.svelte.js", () => ({
  inSessionSearch: {
    get isActive() {
      return searchState.active;
    },
    get debouncedQuery() {
      return searchState.query;
    },
    navigationRevision: 1,
    isCurrentBlock: (key: string | undefined) => !!key && key === searchState.current,
    countForBlock: (key: string | undefined) =>
      key && key === searchState.current ? 1 : 0,
    currentOccurrence: () => 0,
  },
}));

const components: ReturnType<typeof mount>[] = [];

async function render(props: Partial<ComponentProps<typeof ToolBlock>> = {}) {
  components.push(mount(ToolBlock, { target: document.body, props: { content: "", ...props } }));
  await tick();
}

async function click(selector: string) {
  const button = document.querySelector<HTMLButtonElement>(selector);
  expect(button).not.toBeNull();
  button!.click();
  await Promise.resolve();
  await tick();
}

function call(
  tool_name: string,
  params: Record<string, unknown> = {},
  rest: Partial<ToolCall> = {},
): ToolCall {
  return {
    category: "",
    tool_name,
    input_json: JSON.stringify(params),
    ...rest,
  };
}

const text = (selector: string) => document.querySelector(selector)?.textContent ?? "";

beforeEach(() => {
  setLocale("en");
  searchState.active = false;
  searchState.current = "";
  searchState.query = "";
  copyToClipboardMock.mockClear();
});

afterEach(async () => {
  for (const component of components.splice(0)) await unmount(component);
  vi.unstubAllGlobals();
  document.body.replaceChildren();
  setLocale("en");
});

describe("Devin subagent tool blocks", () => {
  it("shows the title and keeps the full task expandable", async () => {
    const toolCall = call(
      "run_subagent",
      {
        title: "Review parser behavior",
        task: "Review the parser and summarize the current changes.\nInclude relevant checks.",
      },
      { category: "Task" },
    );
    await render({ toolCall, label: displayToolName(toolCall) });

    expect(text(".tool-header .tool-label")).toBe("Subagent:");
    expect(text(".tool-header .tool-preview")).toBe("Review parser behavior");
    expect(document.querySelector(".tool-content")).toBeNull();

    await click(".tool-header");
    expect(text(".tool-content")).toContain("Review the parser and summarize the current changes.");
    expect(text(".tool-content")).toContain("Include relevant checks.");
  });

  it("previews the result and keeps the full report expandable", async () => {
    const report = "Subagent task complete.\n## Summary\nThe full implementation report.";
    const toolCall = call(
      "read_subagent",
      { agent_id: "agent-fixture-1" },
      { category: "Task", result_content: report },
    );
    await render({ toolCall, label: displayToolName(toolCall) });

    expect(text(".tool-header .tool-label")).toBe("Subagent result:");
    expect(text(".tool-header .tool-preview")).toBe("Subagent task complete.");
    expect(text(".tool-header .tool-preview")).not.toContain("Summary");
    expect(document.querySelector(".tool-content")).toBeNull();

    await click(".tool-header");
    expect(text(".output-header .tool-preview")).toBe("Subagent task complete.");
    await click(".output-header");
    expect(text(".output-content")).toBe(report);
  });
});
