// @vitest-environment jsdom
import { describe, expect, it } from "vite-plus/test";
import type { DbMessage as Message } from "../api/generated/index.js";
import type { BlockType } from "../stores/ui.svelte.js";
import { buildSessionIndex } from "./session-index.js";
import { keepsAnswerBeforeTrailingTools, projectSessionScope } from "./session-scope.js";

const ALL: ReadonlySet<BlockType> = new Set([
  "user",
  "assistant",
  "thinking",
  "tool",
  "code",
  "system",
]);

let nextId = 970000;
function message(ordinal: number, content: string, overrides: Partial<Message> = {}): Message {
  return {
    has_context_tokens: false,
    has_output_tokens: false,
    id: nextId++,
    session_id: "scope",
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

function scope(
  messages: Message[],
  overrides: Partial<Parameters<typeof projectSessionScope>[0]> = {},
) {
  return projectSessionScope({
    messages,
    transcriptMode: "normal",
    visibleBlocks: ALL,
    hasBlockFilters: false,
    ...overrides,
  });
}

function filtered(visible: BlockType[]) {
  return { visibleBlocks: new Set(visible), hasBlockFilters: visible.length < ALL.size };
}

describe("session search scope", () => {
  it("keeps visible prose in a mixed message and drops its hidden tool blocks", () => {
    const mixed = message(1, "needle prose\n\n[Bash]\necho needle", { has_tool_use: true });
    const result = scope([mixed], filtered(["user", "assistant"]));
    expect(result.messages).toContain(mixed);
    expect(result.allowsBlock(mixed, "text")).toBe(true);
    expect(result.allowsBlock(mixed, "tool-input")).toBe(false);
    expect(result.allowsBlock(mixed, "tool-output")).toBe(false);
    expect(result.allowsBlock(mixed, "tool-history")).toBe(false);
  });

  it("keeps thinking in a message whose tools are hidden and drops legacy tool-only rows", () => {
    const thinkingPlusTool = message(2, "[Thinking]\nneedle\n[/Thinking]", {
      has_thinking: true,
      has_tool_use: true,
      tool_calls: [
        {
          category: "",
          tool_name: "Bash",
        },
      ],
    });
    const legacyTool = message(3, "[Bash]\necho needle", { has_tool_use: true });
    const result = scope(
      [thinkingPlusTool, legacyTool],
      filtered(["user", "assistant", "thinking"]),
    );
    expect(result.messages).toContain(thinkingPlusTool);
    expect(result.allowsBlock(thinkingPlusTool, "thinking")).toBe(true);
    expect(result.allowsBlock(thinkingPlusTool, "tool-output")).toBe(false);
    // Hidden tool-only rows leave the rendered list but stay searchable.
    expect(result.messages).toContain(legacyTool);
    expect(result.displayItems.flatMap((item) => item.ordinals)).toEqual([2]);
    expect(result.allowsBlock(legacyTool, "tool-input")).toBe(false);
  });

  it("maps text and skill blocks to the message role filter", () => {
    const user = message(4, "hello", { role: "user" });
    const assistant = message(5, "hello");
    const result = scope([user, assistant], filtered(["assistant"]));
    // Filter-hidden source messages remain eligible for the search index.
    expect(result.messages).toContain(user);
    expect(result.displayItems.flatMap((item) => item.ordinals)).toEqual([5]);
    expect(result.allowsBlock(user, "text")).toBe(false);
    expect(result.allowsBlock(user, "skill")).toBe(false);
    expect(result.messages).toContain(assistant);
    expect(result.allowsBlock(assistant, "text")).toBe(true);
    expect(result.allowsBlock(assistant, "skill")).toBe(true);
  });

  it("keeps filter-hidden blocks searchable while rendering only visible content", () => {
    const prompt = message(0, "find the needle", { role: "user" });
    const thinking = message(1, "[Thinking]\nneedle\n[/Thinking]", {
      has_thinking: true,
    });
    const result = scope([prompt, thinking], filtered(["user", "assistant"]));
    // Transcript nodes keep full group membership before block filtering.
    expect(result.items.map((node) => node.kind)).toEqual(["prompt", "assistant-turn"]);
    expect(result.items[1]!.ordinals).toEqual([1]);
    expect(result.messages).toContain(thinking);
    // The rendered list and the per-kind gate still respect the filter.
    expect(result.displayItems.flatMap((item) => item.ordinals)).toEqual([0]);
    expect(result.allowsBlock(thinking, "thinking")).toBe(false);
    const index = buildSessionIndex(result.messages, "needle");
    expect(index.matches).toContainEqual(
      expect.objectContaining({
        kind: "thinking",
        role: "assistant",
        blockKey: "1:thinking:0",
      }),
    );
  });

  it("lets a temporarily revealed type render without joining the saved filter", () => {
    const prompt = message(0, "find the needle", { role: "user" });
    const thinking = message(1, "[Thinking]\nneedle\n[/Thinking]", {
      has_thinking: true,
    });
    const result = scope([prompt, thinking], {
      ...filtered(["user", "assistant"]),
      revealedBlocks: new Set<BlockType>(["thinking"]),
    });
    // The reveal overlay renders the hidden row but never enters the saved
    // filter gate that non-search consumers consult.
    expect(result.displayItems.flatMap((item) => item.ordinals)).toEqual([0, 1]);
    expect(result.normalItems.flatMap((item) => item.ordinals)).toEqual([0, 1]);
    expect(result.allowsBlock(thinking, "thinking")).toBe(false);
    expect(result.allowsBlock(thinking, "code")).toBe(false);
  });

  it("maps thinking and code to their own filters", () => {
    const both = message(6, "[Thinking]\nthink\n[/Thinking]\n\n```ts\nconst x = 1;\n```");
    const result = scope([both], filtered(["user", "assistant", "thinking"]));
    expect(result.allowsBlock(both, "thinking")).toBe(true);
    expect(result.allowsBlock(both, "code")).toBe(false);
    expect(result.allowsBlock(both, "text")).toBe(true);
  });

  it("focused mode narrows the scope to the rendered turns", () => {
    const messages = [
      message(0, "prompt", { role: "user" }),
      message(1, "intermediate"),
      message(2, "final"),
      message(3, "next", { role: "user" }),
    ];
    const result = scope(messages, { transcriptMode: "focused" });
    expect(result.displayItems.flatMap((item) => item.ordinals)).toEqual([0, 2, 3]);
    expect(result.items.map((node) => node.kind)).toEqual(["prompt", "assistant-turn", "prompt"]);
    expect(result.messages.map((item) => item.ordinal)).toEqual([0, 2, 3]);
    expect(result.allowsBlock(messages[1]!, "text")).toBe(false);
    expect(result.normalItems.flatMap((item) => item.ordinals)).toEqual([0, 1, 2, 3]);
  });

  it("keeps intermediate focused-mode messages out of search under block filters", () => {
    const messages = [
      message(0, "prompt", { role: "user" }),
      message(1, "intermediate needle"),
      message(2, "final needle"),
      message(3, "next", { role: "user" }),
    ];
    const result = scope(messages, {
      transcriptMode: "focused",
      ...filtered(["user"]),
    });
    // The filtered focused projection hides the assistant answer, but the
    // mode-selected source messages stay eligible for the search index. Only
    // the non-selected intermediate assistant message stays out.
    expect(result.displayItems.flatMap((item) => item.ordinals)).toEqual([0, 3]);
    expect(result.messages.map((item) => item.ordinal)).toEqual([0, 2, 3]);
    expect(result.messages).not.toContain(messages[1]!);
    const index = buildSessionIndex(result.messages, "needle");
    expect(index.total).toBe(1);
  });

  it("keeps the answer before trailing tools only when the provider asks", () => {
    const messages = [
      message(0, "prompt", { role: "user" }),
      message(1, "answer"),
      message(2, "[Bash]\necho done", { has_tool_use: true }),
    ];
    const dropped = scope(messages, { transcriptMode: "focused" });
    expect(dropped.messages.map((item) => item.ordinal)).toEqual([0]);
    const kept = scope(messages, {
      transcriptMode: "focused",
      keepAnswerBeforeTrailingTools: true,
    });
    expect(kept.messages.map((item) => item.ordinal)).toEqual([0, 1]);
  });

  it("keeps system rows out of the searchable scope", () => {
    const system = message(7, "needle", { is_system: true });
    const result = scope([system]);
    expect(result.messages).toHaveLength(0);
    expect(result.allowsBlock(system, "text")).toBe(false);
  });

  it("resolves the provider trailing-tool preference by agent id", () => {
    const providers = [
      { id: "claude", post_answer_tool_work: true },
      { id: "codex", post_answer_tool_work: false },
    ];
    expect(keepsAnswerBeforeTrailingTools(providers, "claude")).toBe(true);
    expect(keepsAnswerBeforeTrailingTools(providers, "codex")).toBe(false);
    expect(keepsAnswerBeforeTrailingTools(providers, "other")).toBe(false);
    expect(keepsAnswerBeforeTrailingTools(providers, undefined)).toBe(false);
  });
});

it.each(["normal", "focused"] as const)(
  "keeps filtered code-only messages expandable in %s mode",
  (transcriptMode) => {
    const prompt = message(0, "Show the example", { role: "user" });
    const code = message(1, "```ts\nconst needle = 1;\n```");
    const result = scope([prompt, code], { ...filtered(["user", "assistant"]), transcriptMode });
    expect(result.displayItems.flatMap((item) => item.ordinals)).toEqual([0, 1]);
    expect(result.allowsBlock(code, "code")).toBe(false);
  },
);
