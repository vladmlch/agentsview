// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { mount, tick, unmount } from "svelte";
import type { DbMessage as Message } from "../../api/generated/index.js";
import { inSessionSearch } from "../../stores/inSessionSearch.svelte.js";
import { messages } from "../../stores/messages.svelte.js";
import { sessions } from "../../stores/sessions.svelte.js";
import { turnCollapse } from "../../stores/turn-collapse.svelte.js";
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

/** Lets the reveal loop finish: expansion, remount, and its frame settles. */
async function settleReveal() {
  await tick();
  for (let i = 0; i < 12; i++) {
    await vi.advanceTimersByTimeAsync(250);
    await tick();
  }
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
    message(1, "[Thinking]\nneedle\n[/Thinking]\nneedle in middle text", {
      has_thinking: true,
    }),
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
    message(3, "final answer text"),
  ];
  messages.messageCount = 4;
  ui.showAllBlocks();
  for (const type of ["assistant", "thinking", "tool", "code", "system"] as const) {
    ui.setBlockVisible(type, false);
  }
  ui.setTranscriptMode("normal");
  // Turns start collapsed so reveal must expand the owning turn and event.
  ui.setAutoCollapseAssistantTurns(true);
  turnCollapse.activateSession(null);
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
  ui.setAutoCollapseAssistantTurns(true);
  turnCollapse.activateSession(null);
  ui.messageLayout = "default";
  ui.sortNewestFirst = false;
  document.body.innerHTML = "";
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("MessageList search visibility", () => {
  it("expands the owning turn and event disclosures to reach hidden-type matches", async () => {
    component = mount(MessageList, { target: document.body });
    await tick();
    // Baseline: only the user row renders; hidden types stay unmounted.
    expect(document.querySelectorAll(".virtual-row")).toHaveLength(1);
    expect(document.querySelector(".turn-header")).toBeNull();
    const filters = [...ui.visibleBlocks];
    const stored = localStorage.getItem(BLOCK_FILTER_KEY);

    await search();

    // Hidden thinking, mid-turn text, and tool-output blocks join the index…
    expect(inSessionSearch.total).toBe(4);
    expect(inSessionSearch.countForBlock("1:thinking:0")).toBe(1);
    expect(inSessionSearch.countForBlock("1:text:1")).toBe(1);
    expect(inSessionSearch.countForBlock("2:tool-output:0")).toBe(1);
    // …their owning types are temporarily revealed…
    expect(inSessionSearch.revealedBlockTypes.has("thinking")).toBe(true);
    expect(inSessionSearch.revealedBlockTypes.has("tool")).toBe(true);
    expect(inSessionSearch.revealedBlockTypes.has("assistant")).toBe(true);
    // …so the turn header and final-output row mount; the current match is
    // the visible user row, so the turn itself stays collapsed.
    expect(document.querySelectorAll(".virtual-row")).toHaveLength(3);
    const header = document.querySelector(".turn-header");
    expect(header).not.toBeNull();
    expect(header!.getAttribute("aria-expanded")).toBe("false");
    expect(document.querySelectorAll(".thinking-header")).toHaveLength(0);
    expect(document.querySelector(".tool-block")).toBeNull();
    expect([...ui.visibleBlocks]).toEqual(filters);
    expect(localStorage.getItem(BLOCK_FILTER_KEY)).toBe(stored);

    // The thinking match expands the turn and mounts the exact block.
    inSessionSearch.next();
    await settleReveal();
    expect(document.querySelector(".turn-header")!.getAttribute("aria-expanded")).toBe("true");
    expect(document.querySelector('[data-search-block="1:thinking:0"]')).not.toBeNull();

    // The mid-turn text match lives inside the message event disclosure:
    // it expands so the block can mount.
    inSessionSearch.next();
    await settleReveal();
    expect(document.querySelector('[data-search-block="1:text:1"]')).not.toBeNull();
    const messageToggle = document.querySelector(
      '.turn-event[data-event-kind="message"] .event-toggle',
    );
    expect(messageToggle?.getAttribute("aria-expanded")).toBe("true");

    // The tool-output match nested inside the rollup mounts too.
    inSessionSearch.next();
    await settleReveal();
    expect(document.querySelector('[data-search-block="2:tool-output:0"]')).not.toBeNull();

    // Saved filters and localStorage stay untouched by the temporary reveal.
    expect([...ui.visibleBlocks]).toEqual(filters);
    expect(localStorage.getItem(BLOCK_FILTER_KEY)).toBe(stored);
  });

  it("expands the event disclosure that owns a later-run segment", async () => {
    // Adjacent text/code segments merge into one event per run, so the
    // second run's disclosure — not the first message event — owns the
    // code block at segment index 3.
    messages.messages = [
      message(0, "prompt", { role: "user" }),
      message(1, "part zero\n[Thinking]\nthought\n[/Thinking]\npart two\n```\nneedle\n```", {
        has_thinking: true,
      }),
      message(2, "[Skill: one]\nfirst\n[/Skill]\n[Skill: two]\nneedle\n[/Skill]\ntail"),
      message(3, "final answer"),
    ];
    messages.messageCount = 4;
    component = mount(MessageList, { target: document.body });
    await tick();
    const msg1Id = messages.messages[1]!.id;
    const msg2Id = messages.messages[2]!.id;

    await search();
    expect(inSessionSearch.countForBlock("1:code:3")).toBe(1);
    expect(inSessionSearch.countForBlock("2:skill:1")).toBe(1);

    // The pinned first match reveals itself: the code block sits in the
    // SECOND text/code run, so its own `message:2` disclosure opens —
    // not the first message event on the same message.
    await settleReveal();
    expect(turnCollapse.isEventExpanded(`search-list:${msg1Id}:message:2`, false)).toBe(true);
    expect(turnCollapse.isEventExpanded(`search-list:${msg1Id}:message:0`, false)).toBe(false);
    expect(document.querySelector('[data-search-block="1:code:3"]')).not.toBeNull();

    // Each skill segment keys its own disclosure too — the second one,
    // not the first skill event on the same message. A mounted skill row
    // self-discloses the current block, so the store path only runs when
    // the row is unmounted: fold the turn and let the reveal re-mount it.
    inSessionSearch.next();
    await settleReveal();
    expect(document.querySelector('[data-search-block="2:skill:1"]')).not.toBeNull();
    turnCollapse.setTurnExpanded(`search-list:turn:${msg1Id}`, false);
    await settleReveal();
    expect(turnCollapse.isEventExpanded(`search-list:${msg2Id}:skill:1`, false)).toBe(true);
    expect(turnCollapse.isEventExpanded(`search-list:${msg2Id}:skill:0`, false)).toBe(false);
    expect(document.querySelector('[data-search-block="2:skill:1"]')).not.toBeNull();
  });

  it("opens a truncated prompt's disclosure so a tail match mounts and highlights", async () => {
    messages.messages = [
      message(0, `${"pad ".repeat(160)}needle`, { role: "user" }),
      message(1, "final answer text"),
    ];
    messages.messageCount = 2;
    component = mount(MessageList, { target: document.body });
    await tick();
    const promptId = messages.messages[0]!.id;
    // The tail folds behind the prompt disclosure: no needle in the DOM yet.
    expect(turnCollapse.isUserPromptExpanded(promptId)).toBe(false);
    expect(document.querySelector(".prompt-toggle")).not.toBeNull();
    expect(document.body.textContent).not.toContain("needle");

    await search();
    // The match is indexed while folded; navigation expands the disclosure
    // so the block mounts and takes the current highlight.
    expect(inSessionSearch.countForBlock("0:text:0")).toBe(1);
    await settleReveal();
    expect(turnCollapse.isUserPromptExpanded(promptId)).toBe(true);
    const block = document.querySelector('[data-search-block="0:text:0"]');
    expect(block).not.toBeNull();
    expect(block!.getAttribute("data-search-current")).toBe("true");
  });

  it("keeps a manually hidden revealed type suppressed until the find view closes", async () => {
    component = mount(MessageList, { target: document.body });
    await tick();
    await search();
    // Expand the owning turn by navigating to its thinking match.
    inSessionSearch.next();
    await settleReveal();
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
    inSessionSearch.next();
    await settleReveal();
    expect(document.querySelectorAll(".thinking-header").length).toBeGreaterThan(0);
  });

  it("suppresses a saved-visible type manually hidden during search", async () => {
    ui.setBlockVisible("thinking", true);
    ui.setAutoCollapseAssistantTurns(false);
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
    // thinking/text and tool-only rows are not eligible for the index.
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
