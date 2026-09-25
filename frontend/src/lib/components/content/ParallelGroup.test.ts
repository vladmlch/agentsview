// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it } from "vite-plus/test";
import { mount, tick, unmount } from "svelte";
import type { DbToolCall as ToolCall } from "../../api/generated/index.js";
import type { DbCallTiming as CallTiming } from "../../api/generated/index.js";
import { setLocale } from "../../i18n/index.js";
import { turnCollapse } from "../../stores/turn-collapse.svelte.js";
// @ts-ignore
import ParallelGroup from "./ParallelGroup.svelte";

function makeToolCall(id: string): ToolCall {
  return {
    tool_use_id: id,
    tool_name: "Read",
    category: "Read",
    input_json: "{}",
    result_content: "",
  };
}

afterEach(() => {
  setLocale("en");
  document.body.innerHTML = "";
});

describe("ParallelGroup", () => {
  it("shows only known timing rows", async () => {
    const callTimingByID = new Map<string, CallTiming>([
      [
        "a",
        {
          tool_use_id: "a",
          tool_name: "Read",
          category: "Read",
          duration_ms: 2000,
          is_parallel: true,
          input_preview: "main.go",
        },
      ],
      [
        "b",
        {
          tool_use_id: "b",
          tool_name: "Task",
          category: "Task",
          duration_ms: null,
          is_parallel: true,
          input_preview: "review",
          subagent_session_id: "child-1",
        },
      ],
    ]);
    const component = mount(ParallelGroup, {
      target: document.body,
      props: {
        toolCalls: [makeToolCall("a"), makeToolCall("b"), makeToolCall("c")],
        callTimingByID,
      },
    });
    await tick();

    expect(
      [...document.querySelectorAll(".tool-duration")].map((el) => el.textContent?.trim()),
    ).toEqual(["2.0s", "unknown"]);
    expect(document.querySelector(".pg-header")?.textContent).not.toContain("5.0s");
    expect(document.querySelector(".pg-count")?.textContent?.trim()).toBe("3 calls");
    unmount(component);
  });

  it("renders parallel tool group labels in Simplified Chinese", async () => {
    setLocale("zh-CN");
    const component = mount(ParallelGroup, {
      target: document.body,
      props: {
        toolCalls: [makeToolCall("a"), makeToolCall("b")],
      },
    });
    await tick();

    expect(document.querySelector(".pg-label")?.textContent?.trim()).toBe("并行");
    expect(document.querySelector(".pg-count")?.textContent?.trim()).toBe("2 次调用");
    expect(document.querySelectorAll(".tool-duration")).toHaveLength(0);

    unmount(component);
  });
});

describe("ParallelGroup turn-scoped collapse", () => {
  beforeEach(() => {
    turnCollapse.activateSession(null);
    turnCollapse.activateSession("s1");
  });
  afterEach(() => turnCollapse.activateSession(null));

  it("derives inner block expansion from the collapse key prefix", async () => {
    const component = mount(ParallelGroup, {
      target: document.body,
      props: {
        toolCalls: [makeToolCall("a"), makeToolCall("b")],
        collapseKeyPrefix: "s1:11:tool:2",
        defaultExpanded: true,
      },
    });
    await tick();

    const headers = document.querySelectorAll(".tool-header");
    expect(headers).toHaveLength(2);
    for (const header of headers) {
      expect(header.getAttribute("aria-expanded")).toBe("true");
    }

    turnCollapse.setEventExpanded("s1:11:tool:2:1", false);
    await tick();
    const refreshed = document.querySelectorAll(".tool-header");
    expect(refreshed[0]!.getAttribute("aria-expanded")).toBe("true");
    expect(refreshed[1]!.getAttribute("aria-expanded")).toBe("false");
    unmount(component);
  });
});
