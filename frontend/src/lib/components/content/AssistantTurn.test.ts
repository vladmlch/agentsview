// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { mount, tick, unmount } from "svelte";
import type { Session } from "../../api/types.js";
import type {
  DbMessage as Message,
  DbSessionTiming as SessionTiming,
  DbToolCall as ToolCall,
} from "../../api/generated/index.js";
import { setLocale } from "../../i18n/index.js";
import { buildDisplayItems } from "../../utils/display-items.js";
import { buildTranscriptNodes, type AssistantTurnItem } from "../../utils/assistant-turns.js";
import { turnCollapse } from "../../stores/turn-collapse.svelte.js";
import AssistantTurn from "./AssistantTurn.svelte";

const timingState = vi.hoisted(() => ({ timing: null as SessionTiming | null }));
vi.mock("../../stores/sessionTiming.svelte.js", () => ({ sessionTiming: timingState }));

const copyMock = vi.hoisted(() => vi.fn().mockResolvedValue(true));
const pinMock = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));
const forkMock = vi.hoisted(() => vi.fn());
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
  pins: { isPinned: () => false, togglePin: pinMock },
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
  SessionsService: { postApiV1SessionsByIdResume: forkMock },
}));
vi.mock("../../utils/clipboard.js", () => ({ copyToClipboard: copyMock }));
vi.mock("@kenn-io/kit-ui/utils/markdown-mermaid", () => ({
  mermaidCodeFence: (code: string, lang: string) => {
    if (lang !== "mermaid") return undefined;
    const pre = document.createElement("pre");
    pre.className = "mermaid";
    pre.textContent = code;
    return pre.outerHTML;
  },
  initMarkdownMermaidRendering: mermaidMock,
}));

const components: ReturnType<typeof mount>[] = [];
let nextId = 9000;
function msg(overrides: Partial<Message> & { id?: number } = {}): Message {
  const content = overrides.content ?? "";
  return {
    has_context_tokens: false,
    has_output_tokens: false,
    id: nextId++,
    session_id: "s1",
    ordinal: 0,
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
function userMsg(id: number, ordinal: number, content = "prompt"): Message {
  return msg({ id, ordinal, role: "user", content });
}
function asstMsg(
  id: number,
  ordinal: number,
  content: string,
  overrides: Partial<Message> = {},
): Message {
  return msg({ id, ordinal, role: "assistant", content, ...overrides });
}
function toolMsg(id: number, ordinal: number, tool = "Bash", args = "$ ls"): Message {
  return msg({ id, ordinal, content: `[${tool}]\n${args}`, has_tool_use: true });
}
function boundaryMsg(id: number, ordinal: number, subtype: string, content: string): Message {
  return msg({
    id,
    ordinal,
    role: "user",
    is_system: true,
    source_subtype: subtype,
    content,
  });
}
function session(overrides: Partial<Session> = {}): Session {
  return {
    compaction_count: 0,
    consecutive_failure_max: 0,
    edit_churn_count: 0,
    ended_with_role: "",
    final_failure_streak: 0,
    has_peak_context_tokens: false,
    has_total_output_tokens: false,
    mid_task_compaction_count: 0,
    outcome: "",
    outcome_confidence: "",
    secret_leak_count: 0,
    tool_failure_signal_count: 0,
    tool_retry_count: 0,
    id: "s1",
    agent: "claude",
    project: "proj-a",
    machine: "test",
    first_message: "hello",
    started_at: "2026-02-20T12:30:00Z",
    ended_at: "2026-02-20T12:31:00Z",
    message_count: 3,
    user_message_count: 2,
    total_output_tokens: 0,
    peak_context_tokens: 0,
    is_automated: false,
    created_at: "2026-02-20T12:30:00Z",
    ...overrides,
  } as Session;
}
function turnFor(messages: Message[]): AssistantTurnItem {
  const nodes = buildTranscriptNodes(buildDisplayItems(messages), "s1");
  const turn = nodes.find((node) => node.kind === "assistant-turn");
  if (!turn || turn.kind !== "assistant-turn") {
    throw new Error("expected an assistant-turn node");
  }
  return turn;
}
async function render(turn: AssistantTurnItem) {
  components.push(mount(AssistantTurn, { target: document.body, props: { turn } }));
  await tick();
}
async function click(target: string | Element) {
  const button =
    typeof target === "string"
      ? document.querySelector<HTMLButtonElement>(target)
      : (target.querySelector<HTMLButtonElement>("button") ?? (target as HTMLButtonElement));
  expect(button).not.toBeNull();
  button!.click();
  await Promise.resolve();
  await tick();
}
const text = (selector: string) => document.querySelector(selector)?.textContent?.trim() ?? "";
const rowKinds = () =>
  Array.from(document.querySelectorAll(".turn-event"), (row) =>
    row.getAttribute("data-event-kind"),
  );

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
  state.activeSession = null;
  state.readOnly = false;
  state.remote = false;
  state.searching = false;
  uiState.showAllBlocks();
  uiState.autoCollapseAssistantTurns = true;
  uiState.sortNewestFirst = false;
});

describe("AssistantTurn", () => {
  it("collapses the turn while keeping the final output visible", async () => {
    const turn = turnFor([
      userMsg(10, 0, "go"),
      asstMsg(11, 1, "Working on it."),
      asstMsg(12, 2, "Final answer."),
    ]);
    await render(turn);

    const header = document.querySelector(".turn-header");
    expect(header).not.toBeNull();
    expect(header!.getAttribute("aria-expanded")).toBe("false");
    expect(document.querySelector(".assistant-turn-events")).toBeNull();
    expect(text(".turn-output")).toContain("Final answer.");
    expect(text(".turn-output")).not.toContain("Working on it.");
    expect(text(".turn-header")).toContain("Assistant");
    expect(text(".turn-header")).toContain("claude-sonnet");
    expect(text(".turn-header")).toContain("2 messages");
    expect(document.querySelector(".turn-timestamp")?.textContent).not.toBe("");
  });

  it("expands into separate typed event rows per message", async () => {
    const content = [
      "[Thinking]",
      "plan",
      "[/Thinking]",
      "",
      "Visible answer.",
      "",
      "```ts",
      "const n = 1;",
      "```",
    ].join("\n");
    const turn = turnFor([
      userMsg(10, 0, "go"),
      asstMsg(11, 1, content, { has_thinking: true }),
      asstMsg(12, 2, "Final answer."),
    ]);
    await render(turn);
    await click(".turn-header");

    expect(rowKinds()).toEqual(["thinking", "message", "message"]);
    const messageRows = document.querySelectorAll('[data-event-kind="message"]');
    expect(
      messageRows[0]!
        .querySelector(".event-preview")!
        .textContent!.trim()
        .startsWith("Visible answer."),
    ).toBe(true);
    // The code fence appears only inside the expanded Message row.
    expect(document.querySelector(".assistant-turn-events .code-content")).toBeNull();
    expect(document.querySelector(".turn-output .code-content")).toBeNull();
    await click(messageRows[0]!.querySelector<HTMLElement>(".event-toggle")!);
    expect(document.querySelector(".assistant-turn-events .code-content")?.textContent).toContain(
      "const n = 1;",
    );
    // The final output's own child row stays a separate collapsed preview
    // while the output row still renders the full text.
    const refreshedRows = document.querySelectorAll('[data-event-kind="message"]');
    expect(refreshedRows[1]!.querySelector(".event-preview")!.textContent!.trim()).toBe(
      "Final answer.",
    );
    expect(text(".turn-output")).toContain("Final answer.");
  });

  it("truncates event previews at 80 Unicode code points", async () => {
    const eighty = `${"x".repeat(79)}🙂`;
    const eightyOne = `${"y".repeat(80)}🙂`;
    const turn = turnFor([userMsg(10, 0, "go"), asstMsg(11, 1, eighty), asstMsg(12, 2, eightyOne)]);
    await render(turn);
    turnCollapse.setTurnExpanded(turn.key, true);
    await tick();

    const previews = Array.from(
      document.querySelectorAll('[data-event-kind="message"] .event-preview'),
      (node) => node.textContent ?? "",
    );
    expect(previews).toEqual([eighty, `${"y".repeat(80)}…`]);
  });

  it("renders source actions once on the first rendered event of each message", async () => {
    state.sessions = [session()];
    const content = "[Thinking]\nplan\n[/Thinking]\n\nVisible answer.";
    const turn = turnFor([
      userMsg(10, 0, "go"),
      asstMsg(11, 1, content, { has_thinking: true }),
      asstMsg(12, 2, "Final answer."),
    ]);
    await render(turn);
    turnCollapse.setTurnExpanded(turn.key, true);
    await tick();

    const rows = Array.from(document.querySelectorAll(".turn-event"));
    const pinCounts = rows.map((row) => row.querySelectorAll(".pin-btn:not(.fork-btn)").length);
    expect(pinCounts).toEqual([1, 0, 1]);
  });

  it("copies the full source message from a child event row", async () => {
    const content = "[Thinking]\nplan\n[/Thinking]\n\nVisible answer.";
    const turn = turnFor([userMsg(10, 0, "go"), asstMsg(11, 1, content, { has_thinking: true })]);
    await render(turn);
    turnCollapse.setTurnExpanded(turn.key, true);
    await tick();

    const copy = document.querySelector<HTMLButtonElement>(
      '.turn-event button[aria-label="Copy message"]',
    );
    expect(copy).not.toBeNull();
    copy!.click();
    await Promise.resolve();
    const copied = copyMock.mock.calls[0]?.[0] as string;
    expect(copied).toContain("plan");
    expect(copied).toContain("Visible answer.");
  });

  it("pins and forks the source message by its original ordinal", async () => {
    state.sessions = [session()];
    forkMock.mockResolvedValueOnce({
      launched: false,
      command: "claude < /tmp/fork.txt",
      cwd: "/tmp",
    });
    const turn = turnFor([userMsg(10, 0, "go"), asstMsg(11, 1, "Visible answer.")]);
    await render(turn);
    turnCollapse.setTurnExpanded(turn.key, true);
    await tick();

    await click(document.querySelector<HTMLElement>(".turn-event .pin-btn")!);
    expect(pinMock).toHaveBeenCalledWith("s1", 11, 1);
    await click(document.querySelector<HTMLElement>(".turn-event .fork-btn")!);
    expect(forkMock).toHaveBeenCalledWith({ id: "s1" }, { from_ordinal: 1, fork_session: true });
  });

  it.each([true, false])(
    "opens error tool output but keeps normal drawers collapsed (autoCollapse=%s)",
    async (autoCollapse) => {
      uiState.autoCollapseAssistantTurns = autoCollapse;
      const errorCall: ToolCall = {
        tool_use_id: "c1",
        tool_name: "Bash",
        category: "Bash",
        input_json: '{"command":"false"}',
        result_content: "boom",
        result_events: [
          {
            event_index: 0,
            status: "errored",
            source: "result",
            content: "boom",
            content_length: 4,
          },
        ],
      };
      const okCall: ToolCall = {
        tool_use_id: "c2",
        tool_name: "Read",
        category: "Read",
        input_json: '{"file_path":"a.ts"}',
        result_content: "fine",
        result_events: [
          {
            event_index: 0,
            status: "completed",
            source: "result",
            content: "fine",
            content_length: 4,
          },
        ],
      };
      const turn = turnFor([
        userMsg(10, 0, "go"),
        asstMsg(11, 1, "Checking.", {
          has_tool_use: true,
          tool_calls: [errorCall, okCall],
        }),
        asstMsg(12, 2, "Done."),
      ]);
      await render(turn);
      if (autoCollapse) {
        turnCollapse.setTurnExpanded(turn.key, true);
        await tick();
        for (const block of document.querySelectorAll(
          ".assistant-turn-events .tool-block .tool-header",
        )) {
          (block as HTMLButtonElement).click();
        }
        await tick();
      }

      const blocks = Array.from(document.querySelectorAll(".assistant-turn-events .tool-block"));
      expect(blocks).toHaveLength(2);
      expect(blocks[0]!.querySelector(".output-content")?.textContent).toContain("boom");
      expect(blocks[0]!.querySelector(".result-history")).toBeNull();
      expect(blocks[1]!.querySelector(".output-content")).toBeNull();
      expect(blocks[1]!.querySelector(".result-history")).toBeNull();
    },
  );

  it("renders tool rollups and system events inside the turn", async () => {
    const turn = turnFor([
      userMsg(10, 0, "go"),
      asstMsg(11, 1, "Reading."),
      toolMsg(12, 2),
      toolMsg(13, 3, "Read", "file.ts"),
      boundaryMsg(14, 4, "task_notification", "<task-notification>done</task-notification>"),
      asstMsg(15, 5, "All fixed."),
    ]);
    await render(turn);
    turnCollapse.setTurnExpanded(turn.key, true);
    await tick();

    expect(rowKinds()).toEqual(["message", "tool-rollup", "system", "message"]);
    expect(document.querySelector(".assistant-turn-events .tool-group")).not.toBeNull();
    expect(document.querySelector(".assistant-turn-events .system-boundary")).not.toBeNull();
    expect(text(".turn-header")).toContain("2 tool calls");
    expect(text(".turn-header")).toContain("4 messages");
  });

  it.each([
    { hidden: "thinking", expected: ["message", "message", "tool", "system", "message"] },
    { hidden: "tool", expected: ["thinking", "message", "message", "system", "message"] },
    { hidden: "system", expected: ["thinking", "message", "message", "tool", "message"] },
    { hidden: "assistant", expected: ["thinking", "tool", "system"], output: false },
  ])(
    "hides the matching event rows when the $hidden block type is filtered",
    async ({ hidden, expected, output = true }) => {
      uiState.hideBlock(hidden);
      const bashCall: ToolCall = {
        tool_use_id: "c1",
        tool_name: "Bash",
        category: "Bash",
        input_json: '{"command":"ls"}',
      };
      const turn = turnFor([
        userMsg(10, 0, "go"),
        asstMsg(11, 1, "[Thinking]\nplan\n[/Thinking]\n\nWorking.", { has_thinking: true }),
        asstMsg(12, 2, "Checking.", { has_tool_use: true, tool_calls: [bashCall] }),
        boundaryMsg(13, 3, "task_notification", "<task-notification>done</task-notification>"),
        asstMsg(14, 4, "Final answer."),
      ]);
      await render(turn);
      turnCollapse.setTurnExpanded(turn.key, true);
      await tick();

      expect(rowKinds()).toEqual(expected);
      expect(document.querySelector(".turn-output") !== null).toBe(output);
    },
  );

  it("keeps a code-only message event rendered through the fence placeholder", async () => {
    uiState.hideBlock("code");
    uiState.hideBlock("assistant");
    const turn = turnFor([
      userMsg(10, 0, "go"),
      asstMsg(11, 1, ["```ts", "const n = 1;", "```"].join("\n")),
    ]);
    await render(turn);
    turnCollapse.setTurnExpanded(turn.key, true);
    await tick();

    // The code fence stays transcript-visible so the collapsed
    // placeholder renders in the always-visible output row. The final
    // output's own child row stays a static preview with no toggle —
    // expanding it would only duplicate the output row's content.
    expect(rowKinds()).toEqual(["message"]);
    expect(document.querySelector('[data-event-kind="message"] .event-preview')?.textContent).toBe(
      "```ts",
    );
    expect(document.querySelector('[data-event-kind="message"] button.event-toggle')).toBeNull();
    expect(document.querySelector(".turn-output .code-fence-toggle")).not.toBeNull();
    expect(document.querySelector(".turn-output .code-content")).toBeNull();
  });

  it("moves source actions to the next rendered sibling when the first event is filtered", async () => {
    state.sessions = [session()];
    const content = "[Thinking]\nplan\n[/Thinking]\n\nVisible answer.";
    const turn = turnFor([
      userMsg(10, 0, "go"),
      asstMsg(11, 1, content, { has_thinking: true }),
      asstMsg(12, 2, "Final answer."),
    ]);
    await render(turn);
    turnCollapse.setTurnExpanded(turn.key, true);
    await tick();

    const pinCounts = () =>
      Array.from(
        document.querySelectorAll(".turn-event"),
        (row) => row.querySelectorAll(".pin-btn:not(.fork-btn)").length,
      );
    expect(pinCounts()).toEqual([1, 0, 1]);
    const first = components.pop()!;
    await unmount(first);
    document.body.replaceChildren();

    uiState.hideBlock("thinking");
    await render(turn);
    turnCollapse.setTurnExpanded(turn.key, true);
    await tick();

    expect(rowKinds()).toEqual(["message", "message"]);
    expect(pinCounts()).toEqual([1, 1]);
  });

  it("reverses child event order when the session sorts newest first", async () => {
    uiState.sortNewestFirst = true;
    const turn = turnFor([
      userMsg(10, 0, "go"),
      asstMsg(11, 1, "[Thinking]\nplan\n[/Thinking]\n\nFirst answer.", { has_thinking: true }),
      asstMsg(12, 2, "Final answer."),
    ]);
    await render(turn);
    turnCollapse.setTurnExpanded(turn.key, true);
    await tick();

    expect(rowKinds()).toEqual(["message", "message", "thinking"]);
    const previews = Array.from(
      document.querySelectorAll('[data-event-kind="message"] .event-preview'),
      (node) => node.textContent?.trim(),
    );
    expect(previews).toEqual(["Final answer.", "First answer."]);
  });

  it("carries the final output's source actions while the turn is collapsed", async () => {
    state.sessions = [session()];
    const turn = turnFor([
      userMsg(10, 0, "go"),
      asstMsg(11, 1, "Working on it."),
      asstMsg(12, 2, "Final answer."),
    ]);
    await render(turn);

    // The collapsed turn mounts no child rows, so the output row carries
    // the Copy/Pin/Fork controls for its own message.
    expect(document.querySelector(".assistant-turn-events")).toBeNull();
    const output = document.querySelector(".turn-output");
    expect(output?.querySelector('button[aria-label="Copy message"]')).not.toBeNull();
    expect(output?.querySelector(".pin-btn:not(.fork-btn)")).not.toBeNull();
    expect(output?.querySelector(".fork-btn")).not.toBeNull();

    await click(output!.querySelector<HTMLElement>(".pin-btn:not(.fork-btn)")!);
    expect(pinMock).toHaveBeenCalledWith("s1", 12, 2);
  });

  it("keeps the final output's source actions on its child event row when expanded", async () => {
    state.sessions = [session()];
    const turn = turnFor([
      userMsg(10, 0, "go"),
      asstMsg(11, 1, "Working on it."),
      asstMsg(12, 2, "Final answer."),
    ]);
    await render(turn);
    turnCollapse.setTurnExpanded(turn.key, true);
    await tick();

    const pins = Array.from(
      document.querySelectorAll(".turn-event"),
      (row) => row.querySelectorAll(".pin-btn:not(.fork-btn)").length,
    );
    expect(pins).toEqual([1, 1]);
    expect(document.querySelector(".turn-output .pin-btn")).toBeNull();
    expect(document.querySelector('.turn-output button[aria-label="Copy message"]')).toBeNull();
  });

  it("keeps the final output's own event row as a static collapsed preview", async () => {
    const turn = turnFor([
      userMsg(10, 0, "go"),
      asstMsg(11, 1, "Working on it."),
      asstMsg(12, 2, "Final answer."),
    ]);
    await render(turn);
    turnCollapse.setTurnExpanded(turn.key, true);
    await tick();

    // The final message's event row is a non-interactive preview: no
    // toggle, no expanded body, and no duplicate source text. The output
    // row owns the only mounted copy of the message's search blocks.
    const outputEventRow = document.querySelector(
      '.turn-event[data-event-kind="message"][data-message-ordinal="2"]',
    );
    expect(outputEventRow).not.toBeNull();
    expect(outputEventRow!.querySelector("button.event-toggle")).toBeNull();
    expect(outputEventRow!.querySelector(".event-body")).toBeNull();
    expect(outputEventRow!.querySelector(".event-preview")).not.toBeNull();

    const key = '[data-search-block="2:text:0"]';
    expect(document.querySelectorAll(key)).toHaveLength(1);
    expect(document.querySelector(`.turn-output ${key}`)).not.toBeNull();

    // A search reveal that expands the event's disclosure cannot change
    // that: the preview row ignores expansion state entirely.
    turnCollapse.setEventExpanded(turn.finalOutput!.key, true);
    await tick();
    expect(outputEventRow!.querySelector("button.event-toggle")).toBeNull();
    expect(outputEventRow!.querySelector(".event-body")).toBeNull();
    expect(document.querySelectorAll(key)).toHaveLength(1);
    expect(document.querySelector(`.turn-output ${key}`)).not.toBeNull();

    // Sibling message events keep their normal toggle disclosure.
    const otherRow = document.querySelector(
      '.turn-event[data-event-kind="message"][data-message-ordinal="1"]',
    );
    expect(otherRow?.querySelector("button.event-toggle")).not.toBeNull();
  });
});
