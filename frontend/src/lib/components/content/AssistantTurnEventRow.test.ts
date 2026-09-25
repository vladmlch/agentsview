// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { mount, tick, unmount } from "svelte";
import type { Session } from "../../api/types.js";
import type {
  DbMessage as Message,
  DbSessionTiming as SessionTiming,
  DbToolCall as ToolCall,
} from "../../api/generated/index.js";
import type { TurnEvent } from "../../utils/assistant-turns.js";
import type { ContentSegment } from "../../utils/content-parser.js";
import { setLocale } from "../../i18n/index.js";
import { turnCollapse } from "../../stores/turn-collapse.svelte.js";
import AssistantTurnEventRow from "./AssistantTurnEventRow.svelte";

const timingState = vi.hoisted(() => ({ timing: null as SessionTiming | null }));
vi.mock("../../stores/sessionTiming.svelte.js", () => ({ sessionTiming: timingState }));

const copyMock = vi.hoisted(() => vi.fn().mockResolvedValue(true));
const mermaidMock = vi.hoisted(() => vi.fn(() => ({ renderNow: vi.fn(), disconnect: vi.fn() })));
const state = vi.hoisted(() => ({
  sessions: [] as Session[],
  activeSession: null as Session | null,
  readOnly: false,
  remote: false,
  searching: false,
}));
const uiState = vi.hoisted(() => {
  const hidden = new Set<string>();
  return {
    hidden,
    autoCollapseAssistantTurns: true,
    sortNewestFirst: false,
    renderUnknownXmlBlocksAsPreformatted: false,
    isBlockVisible: (type: string) => !hidden.has(type),
    hideBlock: (type: string) => hidden.add(type),
    showAllBlocks: () => hidden.clear(),
  };
});

vi.mock("../../stores/messages.svelte.js", () => ({
  messages: { sessionId: "", mainModel: "" },
}));
vi.mock("../../stores/ui.svelte.js", () => ({ ui: uiState }));
vi.mock("../../stores/pins.svelte.js", () => ({
  pins: { isPinned: () => false, togglePin: vi.fn().mockResolvedValue(undefined) },
}));
vi.mock("../../stores/sessions.svelte.js", () => ({ sessions: state }));
vi.mock("../../stores/sync.svelte.js", () => ({ sync: state }));
vi.mock("../../stores/inSessionSearch.svelte.js", () => ({
  inSessionSearch: {
    get isActive() {
      return state.searching;
    },
    get debouncedQuery() {
      return state.searching ? "SearchTarget" : "";
    },
    navigationRevision: 0,
    isCurrentBlock: () => false,
    countForBlock: () => 0,
    currentOccurrence: () => -1,
    isBlockEffectivelyVisible: (type: string) => uiState.isBlockVisible(type),
  },
}));
vi.mock("../../api/runtime.js", () => ({ isRemoteConnection: () => state.remote }));
vi.mock("../../api/generated/index", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../api/generated/index")>()),
  SessionsService: { postApiV1SessionsByIdResume: vi.fn() },
}));
vi.mock("../../utils/clipboard.js", () => ({ copyToClipboard: copyMock }));
vi.mock("@kenn-io/kit-ui/utils/markdown-mermaid", () => ({
  mermaidCodeFence: () => undefined,
  initMarkdownMermaidRendering: mermaidMock,
}));

const components: ReturnType<typeof mount>[] = [];
function msg(overrides: Partial<Message> & { id?: number } = {}): Message {
  const content = overrides.content ?? "";
  return {
    has_context_tokens: false,
    has_output_tokens: false,
    id: 11,
    session_id: "s1",
    ordinal: 1,
    role: "assistant",
    content,
    timestamp: "2026-02-20T12:30:00Z",
    has_thinking: false,
    thinking_text: "",
    has_tool_use: false,
    content_length: content.length,
    model: "claude-sonnet",
    token_usage: null,
    context_tokens: 0,
    output_tokens: 0,
    is_system: false,
    ...overrides,
  };
}
function event(overrides: Partial<TurnEvent> = {}): TurnEvent {
  return {
    key: "s1:11:message:0",
    kind: "message",
    message: msg(),
    ordinals: [1],
    ...overrides,
  };
}
async function render(turnEvent: TurnEvent, props: Record<string, unknown> = {}) {
  components.push(
    mount(AssistantTurnEventRow, {
      target: document.body,
      props: { event: turnEvent, ...props },
    }),
  );
  await tick();
}
async function click(selector: string) {
  const button = document.querySelector<HTMLButtonElement>(selector);
  expect(button).not.toBeNull();
  button!.click();
  await Promise.resolve();
  await tick();
}

beforeEach(() => {
  turnCollapse.activateSession(null);
  turnCollapse.activateSession("s1");
  uiState.autoCollapseAssistantTurns = true;
  setLocale("en");
});
afterEach(async () => {
  for (const component of components.splice(0)) await unmount(component);
  document.body.replaceChildren();
  turnCollapse.activateSession(null);
  setLocale("en");
  vi.clearAllMocks();
  state.sessions = [];
  state.searching = false;
  uiState.showAllBlocks();
  uiState.autoCollapseAssistantTurns = true;
});

describe("AssistantTurnEventRow", () => {
  it("renders a collapsed preview and expands into the message segments", async () => {
    await render(
      event({
        segments: [
          { type: "text", content: "Visible answer." },
          { type: "code", content: "const n = 1;\n", label: "ts" },
        ],
        segmentIndex: 1,
      }),
    );
    const toggle = document.querySelector(".event-toggle");
    expect(toggle).not.toBeNull();
    expect(toggle!.getAttribute("aria-expanded")).toBe("false");
    expect(document.querySelector(".event-preview")?.textContent).toBe("Visible answer.");
    expect(document.querySelector(".text-content")).toBeNull();

    await click(".event-toggle");
    expect(document.querySelector(".text-content")?.textContent).toContain("Visible answer.");
    expect(document.querySelector(".code-content")?.textContent).toContain("const n = 1;");
  });

  it("keeps original source segment indexes in search block keys", async () => {
    await render(
      event({
        message: msg({ ordinal: 5 }),
        ordinals: [5],
        segments: [{ type: "text", content: "Visible answer." }],
        segmentIndex: 3,
      }),
    );
    await click(".event-toggle");
    expect(document.querySelector('[data-search-block="5:text:3"]')).not.toBeNull();
  });

  it("keeps an 80-code-point first line as the full preview", async () => {
    const eighty = `${"x".repeat(79)}🙂`;
    await render(event({ segments: [{ type: "text", content: `${eighty}\nsecond line` }] }));
    expect(document.querySelector(".event-preview")?.textContent).toBe(eighty);
  });

  it("truncates an 81-code-point preview without splitting an emoji", async () => {
    const eightyOne = `${"y".repeat(80)}🙂`;
    await render(event({ segments: [{ type: "text", content: eightyOne }] }));
    expect(document.querySelector(".event-preview")?.textContent).toBe(`${"y".repeat(80)}…`);
  });

  it("renders source actions only when it owns them", async () => {
    await render(event({ segments: [{ type: "text", content: "answer" }] }), {
      ownsSourceActions: true,
    });
    const owned = components.pop()!;
    expect(document.querySelector(".turn-event .pin-btn")).not.toBeNull();
    await unmount(owned);
    document.body.replaceChildren();

    await render(
      event({
        key: "s1:11:message:2",
        segments: [{ type: "text", content: "other" }],
      }),
      { ownsSourceActions: false },
    );
    expect(document.querySelector(".turn-event .pin-btn")).toBeNull();
  });

  it("renders thinking events through the keyed ThinkingBlock", async () => {
    await render(
      event({
        key: "s1:11:thinking:0",
        kind: "thinking",
        segments: [{ type: "thinking", content: "plan first" }],
        segmentIndex: 0,
      }),
    );
    const block = document.querySelector(".thinking-block");
    expect(block).not.toBeNull();
    expect(document.querySelector(".thinking-content")).toBeNull();
    turnCollapse.setEventExpanded("s1:11:thinking:0", true);
    await tick();
    expect(document.querySelector(".thinking-content")?.textContent).toContain("plan first");
  });

  it("renders skill events through the keyed SkillBlock", async () => {
    await render(
      event({
        key: "s1:11:skill:0",
        kind: "skill",
        label: "review",
        segments: [{ type: "skill", content: "use the guide", label: "review" }],
        segmentIndex: 0,
      }),
    );
    expect(document.querySelector(".skill-block")).not.toBeNull();
    expect(document.querySelector(".skill-label")?.textContent).toContain("review");
  });

  it("renders a solo structured tool call as a keyed ToolBlock", async () => {
    const toolCall: ToolCall = {
      tool_use_id: "c1",
      tool_name: "Bash",
      category: "Bash",
      input_json: '{"command":"pwd"}',
      result_content: "/tmp",
      result_content_length: 4,
    };
    await render(
      event({
        key: "s1:11:tool:1",
        kind: "tool",
        toolCalls: [toolCall],
        segmentIndex: 1,
      }),
    );
    const block = document.querySelector(".tool-block");
    expect(block).not.toBeNull();
    expect(document.querySelector(".tool-header")?.getAttribute("aria-expanded")).toBe("false");
    turnCollapse.setEventExpanded("s1:11:tool:1", true);
    await tick();
    expect(document.querySelector(".tool-header")?.getAttribute("aria-expanded")).toBe("true");
    expect(document.querySelector(".tool-content")?.textContent).toContain("pwd");
  });

  it("renders multi-call tool events through ParallelGroup", async () => {
    const calls: ToolCall[] = [
      { tool_use_id: "a", tool_name: "Read", category: "Read", input_json: "{}" },
      { tool_use_id: "b", tool_name: "Read", category: "Read", input_json: "{}" },
    ];
    await render(event({ key: "s1:11:tool:1", kind: "tool", toolCalls: calls, segmentIndex: 1 }));
    expect(document.querySelector(".parallel-group")).not.toBeNull();
    expect(document.querySelectorAll(".tool-block")).toHaveLength(2);
  });

  it("renders legacy tool segments as keyed ToolBlocks", async () => {
    const segments: ContentSegment[] = [
      { type: "tool", content: "$ ls", label: "Bash" },
      { type: "tool", content: "file.ts", label: "Read file.ts" },
    ];
    await render(event({ key: "s1:11:tool:2", kind: "tool", segments, segmentIndex: 2 }));
    expect(document.querySelectorAll(".tool-block")).toHaveLength(2);
  });

  it("renders tool rollups through ToolCallGroup", async () => {
    const toolMessage = msg({
      id: 20,
      ordinal: 2,
      content: "[Bash]\n$ ls",
      has_tool_use: true,
    });
    await render(
      event({
        key: "s1:20:tool-rollup:0",
        kind: "tool-rollup",
        message: toolMessage,
        toolMessages: [toolMessage],
      }),
    );
    expect(document.querySelector(".tool-group")).not.toBeNull();
  });

  it("renders system events through SystemBoundaryCard", async () => {
    await render(
      event({
        key: "s1:14:system:0",
        kind: "system",
        label: "task_notification",
        message: msg({
          id: 14,
          ordinal: 4,
          role: "user",
          is_system: true,
          source_subtype: "task_notification",
          content: "<task-notification>done</task-notification>",
        }),
      }),
    );
    expect(document.querySelector(".system-boundary")).not.toBeNull();
    expect(document.querySelector(".system-boundary .label")?.textContent).toContain("Task");
  });
});
