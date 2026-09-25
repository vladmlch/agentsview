// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { mount, tick, unmount } from "svelte";
import type { DbMessage as Message } from "../../api/generated/index.js";
import { inSessionSearch } from "../../stores/inSessionSearch.svelte.js";
import { messages } from "../../stores/messages.svelte.js";
import { sessions } from "../../stores/sessions.svelte.js";
import { ui } from "../../stores/ui.svelte.js";

const virtualizerMock = vi.hoisted(() => ({
  options: { count: 0 },
  scrollOffset: 0,
  scrollRect: { height: 500 },
  getVirtualItems: vi.fn(() => [] as { index: number; key: string; start: number; end: number }[]),
  getTotalSize: vi.fn(() => 1000),
  measureElement: vi.fn(),
  scrollToIndex: vi.fn(),
  scrollToOffset: vi.fn(),
  getOffsetForIndex: vi.fn((index: number) => [index * 120, "start"]),
}));
vi.mock("../../virtual/createVirtualizer.svelte.js", () => ({
  createVirtualizer: (read: () => { count: number }) => ({
    get instance() {
      virtualizerMock.options.count = read().count;
      virtualizerMock.getVirtualItems.mockReturnValue(
        Array.from({ length: read().count }, (_, index) => ({
          index,
          key: `search-row-${index}`,
          start: index * 120,
          end: (index + 1) * 120,
        })),
      );
      return virtualizerMock;
    },
  }),
}));
import MessageList from "./MessageList.svelte";

let component: ReturnType<typeof mount> | undefined;
let nextId = 180000;
function message(ordinal: number, content: string, overrides: Partial<Message> = {}): Message {
  return {
    has_context_tokens: false,
    has_output_tokens: false,
    id: nextId++,
    session_id: "search-list",
    ordinal,
    role: "assistant",
    content,
    content_length: content.length,
    timestamp: "2026-01-01T00:00:00Z",
    has_thinking: false,
    thinking_text: "",
    has_tool_use: false,
    model: "",
    context_tokens: 0,
    output_tokens: 0,
    is_system: false,
    ...overrides,
  };
}

const BLOCK_FILTER_KEY = "agentsview-block-filters";

async function search(query = "needle") {
  inSessionSearch.open();
  inSessionSearch.query = query;
  await tick();
  await vi.advanceTimersByTimeAsync(200);
  await tick();
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.clearAllMocks();
  inSessionSearch.close();
  inSessionSearch.clearQuery();
  messages.clear();
  messages.sessionId = "search-list";
  sessions.activeSessionId = "search-list";
  messages.hasOlder = false;
  messages.loading = false;
  messages.messages = [
    message(0, "needle in visible user", { role: "user" }),
    message(1, "[Thinking]\nneedle\n[/Thinking]", { has_thinking: true }),
    message(2, "", {
      has_tool_use: true,
      tool_calls: [
        {
          category: "",
          tool_name: "Read",
          result_content: "needle",
        },
      ],
    }),
  ];
  messages.messageCount = 3;
  ui.showAllBlocks();
  for (const type of ["assistant", "thinking", "tool", "code", "system"] as const) {
    ui.setBlockVisible(type, false);
  }
  ui.setTranscriptMode("normal");
  ui.messageLayout = "skim";
  ui.sortNewestFirst = false;
  ui.followLatest = false;
  ui.selectedOrdinal = null;
  vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) =>
    window.setTimeout(() => callback(performance.now()), 1),
  );
  vi.spyOn(window, "cancelAnimationFrame").mockImplementation((id) => window.clearTimeout(id));
});

afterEach(async () => {
  if (component) await unmount(component);
  component = undefined;
  inSessionSearch.close();
  inSessionSearch.clearQuery();
  messages.clear();
  sessions.activeSessionId = null;
  ui.showAllBlocks();
  ui.setTranscriptMode("normal");
  ui.messageLayout = "default";
  ui.sortNewestFirst = false;
  document.body.innerHTML = "";
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("MessageList search visibility", () => {
  it("indexes filter-hidden blocks and temporarily reveals their types", async () => {
    component = mount(MessageList, { target: document.body });
    await tick();
    // Baseline: only the user row renders; hidden types stay unmounted.
    const before = document.querySelectorAll(".virtual-row").length;
    expect(before).toBe(1);
    expect(document.querySelectorAll(".thinking-header")).toHaveLength(0);
    expect(document.querySelector(".tool-block")).toBeNull();
    const filters = [...ui.visibleBlocks];
    const stored = localStorage.getItem(BLOCK_FILTER_KEY);

    await search();

    // Hidden thinking and tool-output blocks join the index…
    expect(inSessionSearch.total).toBe(3);
    expect(inSessionSearch.countForBlock("1:thinking:0")).toBe(1);
    expect(inSessionSearch.countForBlock("2:tool-output:0")).toBe(1);
    // …their owning types are temporarily revealed…
    expect(inSessionSearch.revealedBlockTypes.has("thinking")).toBe(true);
    expect(inSessionSearch.revealedBlockTypes.has("tool")).toBe(true);
    // …so the filtered rows and blocks mount without touching preferences.
    expect(document.querySelectorAll(".virtual-row").length).toBe(3);
    expect(document.querySelectorAll(".thinking-header").length).toBeGreaterThan(0);
    expect(document.querySelector(".tool-block")).not.toBeNull();
    expect([...ui.visibleBlocks]).toEqual(filters);
    expect(localStorage.getItem(BLOCK_FILTER_KEY)).toBe(stored);

    inSessionSearch.close();
    await tick();
    // Closing the find view restores the saved filter output.
    expect(document.querySelectorAll(".virtual-row")).toHaveLength(before);
    expect(document.querySelectorAll(".thinking-header")).toHaveLength(0);
    expect(document.querySelector(".tool-block")).toBeNull();
  });

  it("keeps a manually hidden revealed type suppressed until the find view closes", async () => {
    component = mount(MessageList, { target: document.body });
    await tick();
    await search();
    expect(inSessionSearch.revealedBlockTypes.has("thinking")).toBe(true);
    expect(document.querySelectorAll(".thinking-header").length).toBeGreaterThan(0);

    // The filter control reports the manual hide; the type is suppressed.
    inSessionSearch.noteManualBlockFilterChange("thinking", false);
    ui.setBlockVisible("thinking", false);
    await tick();
    expect(inSessionSearch.revealedBlockTypes.has("thinking")).toBe(false);
    expect(document.querySelectorAll(".thinking-header")).toHaveLength(0);
    // The match remains indexed while the type is suppressed.
    expect(inSessionSearch.countForBlock("1:thinking:0")).toBe(1);

    // The suppression survives query changes while the view stays open.
    inSessionSearch.query = "needl";
    await tick();
    await vi.advanceTimersByTimeAsync(200);
    await tick();
    expect(inSessionSearch.countForBlock("1:thinking:0")).toBe(1);
    expect(inSessionSearch.revealedBlockTypes.has("thinking")).toBe(false);
    expect(document.querySelectorAll(".thinking-header")).toHaveLength(0);

    // Reopening the find view reveals the hidden type again (query persists).
    inSessionSearch.close();
    inSessionSearch.open();
    await tick();
    await vi.advanceTimersByTimeAsync(200);
    await tick();
    expect(inSessionSearch.revealedBlockTypes.has("thinking")).toBe(true);
    expect(document.querySelectorAll(".thinking-header").length).toBeGreaterThan(0);
  });

  it("suppresses a saved-visible type manually hidden during search", async () => {
    ui.setBlockVisible("thinking", true);
    component = mount(MessageList, { target: document.body });
    await tick();
    await search();
    expect(inSessionSearch.revealedBlockTypes.has("thinking")).toBe(false);
    expect(document.querySelectorAll(".thinking-header").length).toBeGreaterThan(0);

    // Hiding the type while the find view is open suppresses its reveal, so
    // the thinking match stops mounting instead of auto-revealing.
    ui.setBlockVisible("thinking", false);
    await tick();
    expect(inSessionSearch.countForBlock("1:thinking:0")).toBe(1);
    expect(inSessionSearch.revealedBlockTypes.has("thinking")).toBe(false);
    expect(document.querySelectorAll(".thinking-header")).toHaveLength(0);

    // Re-enabling the saved filter clears the suppression.
    ui.setBlockVisible("thinking", true);
    await tick();
    expect(document.querySelectorAll(".thinking-header").length).toBeGreaterThan(0);
  });

  it("keeps focused-mode intermediates out of search even with matching hidden types", async () => {
    ui.setTranscriptMode("focused");
    component = mount(MessageList, { target: document.body });
    await tick();
    const before = document.querySelectorAll(".virtual-row").length;
    const filters = [...ui.visibleBlocks];
    expect(document.querySelector(".layout-skim")).not.toBeNull();
    await search();
    // Focused mode keeps its message-level projection: the intermediate
    // thinking-only and tool-only rows are not eligible for the index.
    expect(inSessionSearch.total).toBe(1);
    expect(inSessionSearch.countForOrdinal(0)).toBe(1);
    expect(inSessionSearch.countForBlock("1:thinking:0")).toBe(0);
    expect([...inSessionSearch.revealedBlockTypes]).toEqual([]);
    expect(document.querySelectorAll(".virtual-row")).toHaveLength(before);
    expect(document.querySelectorAll(".thinking-header")).toHaveLength(0);
    expect(document.querySelector(".tool-block")).toBeNull();
    // Layout is not a filter: skim may lift temporarily to reveal a match.
    expect(document.querySelector(".layout-skim")).toBeNull();
    expect([...ui.visibleBlocks]).toEqual(filters);
    expect(ui.transcriptMode).toBe("focused");
    expect(ui.messageLayout).toBe("skim");
    inSessionSearch.close();
    await tick();
    expect(document.querySelectorAll(".virtual-row")).toHaveLength(before);
    expect(document.querySelector(".layout-skim")).not.toBeNull();
  });

  it("cancels stale virtual scrolling after closing search", async () => {
    component = mount(MessageList, { target: document.body });
    await tick();
    inSessionSearch.open();
    inSessionSearch.query = "needle";
    await tick();
    await vi.advanceTimersByTimeAsync(150);
    await tick();
    inSessionSearch.close();
    await tick();
    virtualizerMock.scrollToOffset.mockClear();
    virtualizerMock.scrollToIndex.mockClear();
    await vi.advanceTimersByTimeAsync(100);
    expect(virtualizerMock.scrollToOffset).not.toHaveBeenCalled();
    expect(virtualizerMock.scrollToIndex).not.toHaveBeenCalled();
  });
});
