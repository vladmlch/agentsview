// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { mount, tick, unmount } from "svelte";
import type { Session } from "../../api/types.js";
import type { DbMessage as Message } from "../../api/generated/index.js";
import { setLocale } from "../../i18n/index.js";
import MessageSourceActions from "./MessageSourceActions.svelte";

const copyMock = vi.hoisted(() => vi.fn().mockResolvedValue(true));
const pinMock = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));
const pinnedState = vi.hoisted(() => ({ pinned: false }));
const forkMock = vi.hoisted(() => vi.fn());
const state = vi.hoisted(() => ({
  sessions: [] as Session[],
  activeSession: null as Session | null,
  readOnly: false,
  remote: false,
}));

vi.mock("../../stores/pins.svelte.js", () => ({
  pins: {
    isPinned: () => pinnedState.pinned,
    togglePin: pinMock,
  },
}));
vi.mock("../../stores/sessions.svelte.js", () => ({ sessions: state }));
vi.mock("../../stores/sync.svelte.js", () => ({ sync: state }));
vi.mock("../../api/runtime.js", () => ({ isRemoteConnection: () => state.remote }));
vi.mock("../../api/generated/index", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../api/generated/index")>()),
  SessionsService: { postApiV1SessionsByIdResume: forkMock },
}));
vi.mock("../../utils/clipboard.js", () => ({ copyToClipboard: copyMock }));

const components: ReturnType<typeof mount>[] = [];
function msg(overrides: Partial<Message> = {}): Message {
  const content = overrides.content ?? "Full source message.";
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
async function render(message = msg(), props: Record<string, unknown> = {}) {
  components.push(
    mount(MessageSourceActions, {
      target: document.body,
      props: { message, ...props },
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

beforeEach(() => setLocale("en"));
afterEach(async () => {
  for (const component of components.splice(0)) await unmount(component);
  document.body.replaceChildren();
  setLocale("en");
  vi.clearAllMocks();
  pinnedState.pinned = false;
  state.sessions = [];
  state.activeSession = null;
  state.readOnly = false;
  state.remote = false;
});

describe("MessageSourceActions", () => {
  it("copies the full source message", async () => {
    const content = "[Thinking]\nplan\n[/Thinking]\n\nVisible answer.";
    await render(msg({ content, has_thinking: true }));
    await click('button[aria-label="Copy message"]');
    const copied = copyMock.mock.calls[0]?.[0] as string;
    expect(copied).toContain("plan");
    expect(copied).toContain("Visible answer.");
    expect(document.querySelector('button[aria-label="Copied message"]')).not.toBeNull();
  });

  it("pins and unpins the source message by id and ordinal", async () => {
    await render(msg({ ordinal: 7 }));
    await click(".pin-btn:not(.fork-btn)");
    expect(pinMock).toHaveBeenCalledWith("s1", 11, 7);
    expect(document.querySelector(".pin-feedback")?.textContent).toContain("Pinned");
  });

  it("forks the source message from its original ordinal", async () => {
    state.sessions = [session()];
    forkMock.mockResolvedValueOnce({
      launched: false,
      command: "claude < /tmp/fork.txt",
      cwd: "/tmp",
    });
    await render(msg({ ordinal: 7 }));
    await click(".fork-btn");
    expect(forkMock).toHaveBeenCalledWith({ id: "s1" }, { from_ordinal: 7, fork_session: true });
    await vi.waitFor(() => expect(copyMock).toHaveBeenCalledWith("claude < /tmp/fork.txt"));
  });

  it("hides pin and fork controls when mutations are disallowed", async () => {
    state.sessions = [session()];
    await render(msg(), { allowMutations: false });
    expect(document.querySelector(".pin-btn")).toBeNull();
    expect(document.querySelector('button[aria-label="Copy message"]')).not.toBeNull();
  });

  it("hides forking when the session cannot fork", async () => {
    state.sessions = [session({ agent: "codex" })];
    await render(msg());
    expect(document.querySelector(".fork-btn")).toBeNull();
  });
});
