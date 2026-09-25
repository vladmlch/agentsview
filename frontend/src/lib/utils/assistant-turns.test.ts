import { describe, expect, it } from "vite-plus/test";
import type { DbMessage as Message, DbToolCall as ToolCall } from "../api/generated/index.js";
import { buildDisplayItems } from "./display-items.js";
import { buildTranscriptNodes, type TranscriptNode } from "./assistant-turns.js";

function msg(overrides: Partial<Message> & { id: number; content: string }): Message {
  return {
    has_context_tokens: false,
    has_output_tokens: false,
    session_id: "s1",
    ordinal: 0,
    role: "assistant",
    timestamp: "2025-02-17T21:04:00Z",
    has_thinking: false,
    thinking_text: "",
    has_tool_use: false,
    content_length: overrides.content.length,
    model: "",
    token_usage: null,
    context_tokens: 0,
    output_tokens: 0,
    is_system: false,
    ...overrides,
  };
}

function userMsg(id: number, ordinal: number, content = "user") {
  return msg({ id, ordinal, role: "user", content });
}

function assistantMsg(id: number, ordinal: number, content = "assistant") {
  return msg({ id, ordinal, role: "assistant", content });
}

function toolMsg(id: number, ordinal: number, tool = "Bash", args = "$ ls") {
  return msg({ id, ordinal, content: `[${tool}]\n${args}`, has_tool_use: true });
}

function boundaryMsg(id: number, ordinal: number, subtype: string, content: string) {
  return msg({
    id,
    ordinal,
    role: "user",
    is_system: true,
    source_subtype: subtype,
    content,
  });
}

function nodesOf(messages: Message[]): TranscriptNode[] {
  return buildTranscriptNodes(buildDisplayItems(messages), "s1");
}

function kindsOf(nodes: TranscriptNode[]): string[] {
  return nodes.map((n) => n.kind);
}

describe("buildTranscriptNodes", () => {
  it("returns an empty array for empty input", () => {
    expect(buildTranscriptNodes([], "s1")).toEqual([]);
  });

  it("groups a prompt, a mixed turn, a continuation card, and a second exchange", () => {
    const nodes = nodesOf([
      userMsg(10, 0, "fix the flaky test"),
      assistantMsg(11, 1, "[Thinking]\nplan\n[/Thinking]\n\nOn it."),
      toolMsg(12, 2),
      toolMsg(13, 3, "Read", "file.ts"),
      boundaryMsg(14, 4, "task_notification", "<task-notification>done</task-notification>"),
      assistantMsg(15, 5, "All fixed."),
      boundaryMsg(16, 6, "continuation", "This session is being continued"),
      userMsg(17, 7, "thanks"),
      assistantMsg(18, 8, "anytime"),
    ]);

    expect(kindsOf(nodes)).toEqual([
      "prompt",
      "assistant-turn",
      "standalone",
      "prompt",
      "assistant-turn",
    ]);

    const turn = nodes[1]!;
    expect(turn.kind).toBe("assistant-turn");
    if (turn.kind !== "assistant-turn") return;
    expect(turn.key).toBe("s1:turn:11");
    expect(turn.firstMessageId).toBe(11);
    expect(turn.ordinals).toEqual([1, 2, 3, 4, 5]);
    expect(turn.events.map((e) => e.kind)).toEqual([
      "thinking",
      "message",
      "tool-rollup",
      "system",
      "message",
    ]);
    expect(turn.finalOutput?.message.id).toBe(15);

    const second = nodes[4]!;
    expect(second.kind).toBe("assistant-turn");
    if (second.kind !== "assistant-turn") return;
    expect(second.key).toBe("s1:turn:18");
    expect(second.finalOutput?.message.id).toBe(18);
  });

  it("keys child events by source message, kind, and segment index", () => {
    const nodes = nodesOf([
      userMsg(10, 0),
      assistantMsg(11, 1, "[Thinking]\nplan\n[/Thinking]\n\nOn it."),
      toolMsg(12, 2),
      toolMsg(13, 3, "Read", "file.ts"),
      boundaryMsg(14, 4, "task_notification", "<task-notification>done</task-notification>"),
      assistantMsg(15, 5, "All fixed."),
    ]);
    const turn = nodes[1]!;
    if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
    expect(turn.events.map((e) => e.key)).toEqual([
      "s1:11:thinking:0",
      "s1:11:message:1",
      "s1:12:tool-rollup:0",
      "s1:14:system:0",
      "s1:15:message:0",
    ]);
  });

  it("keeps adjacent text and code segments in one message event", () => {
    const nodes = nodesOf([
      userMsg(10, 0),
      assistantMsg(11, 1, "Here is the diff.\n\n```ts\nconst n = 1;\n```\n\nDone."),
    ]);
    const turn = nodes[1]!;
    if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
    expect(turn.events.map((e) => e.kind)).toEqual(["message"]);
    expect(turn.events[0]!.segments?.map((s) => s.type)).toEqual(["text", "code", "text"]);
    expect(turn.events[0]!.segmentIndex).toBe(0);
  });

  it("splits thinking, message, skill, and tool content from one message in order", () => {
    const content = [
      "[Thinking]",
      "plan",
      "[/Thinking]",
      "",
      "Running the fix.",
      "",
      "[Skill: review]",
      "checklist",
      "[/Skill]",
      "",
      "[Bash]",
      "$ npm test",
    ].join("\n");
    const nodes = nodesOf([
      userMsg(10, 0),
      msg({ id: 11, ordinal: 1, content, has_thinking: true, has_tool_use: true }),
    ]);
    const turn = nodes[1]!;
    if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
    expect(turn.events.map((e) => e.kind)).toEqual(["thinking", "message", "skill", "tool"]);
    const skill = turn.events[2]!;
    expect(skill.label).toBe("review");
    const tool = turn.events[3]!;
    expect(tool.segments?.[0]?.label).toBe("Bash");
    expect(turn.finalOutput?.kind).toBe("message");
  });

  it("carries structured tool calls on a single tool event so parallel calls stay grouped", () => {
    const calls: ToolCall[] = [
      { tool_name: "Read", category: "Read", tool_use_id: "a", input_json: "{}" },
      { tool_name: "Read", category: "Read", tool_use_id: "b", input_json: "{}" },
    ];
    const nodes = nodesOf([
      userMsg(10, 0),
      msg({ id: 11, ordinal: 1, content: "Reading both.", has_tool_use: true, tool_calls: calls }),
    ]);
    const turn = nodes[1]!;
    if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
    expect(turn.events.map((e) => e.kind)).toEqual(["message", "tool"]);
    expect(turn.events[1]!.toolCalls).toEqual(calls);
  });

  it("picks the last non-empty message event as finalOutput when tools follow", () => {
    const nodes = nodesOf([
      userMsg(10, 0),
      assistantMsg(11, 1, "Here is the answer."),
      toolMsg(12, 2),
    ]);
    const turn = nodes[1]!;
    if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
    expect(turn.finalOutput?.message.id).toBe(11);
    expect(turn.events.map((e) => e.kind)).toEqual(["message", "tool-rollup"]);
  });

  it("returns null finalOutput for a turn with no user-facing text", () => {
    const nodes = nodesOf([userMsg(10, 0), toolMsg(11, 1), toolMsg(12, 2)]);
    const turn = nodes[1]!;
    if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
    expect(turn.events.map((e) => e.kind)).toEqual(["tool-rollup"]);
    expect(turn.finalOutput).toBeNull();
  });

  it("keeps task_notification and stop_hook inside the current turn", () => {
    for (const subtype of ["task_notification", "stop_hook"]) {
      const nodes = nodesOf([
        userMsg(10, 0),
        assistantMsg(11, 1, "working"),
        boundaryMsg(12, 2, subtype, "system event"),
        assistantMsg(13, 3, "done"),
      ]);
      expect(kindsOf(nodes)).toEqual(["prompt", "assistant-turn"]);
      const turn = nodes[1]!;
      if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
      expect(turn.events.map((e) => e.kind)).toEqual(["message", "system", "message"]);
      expect(turn.events[1]!.label).toBe(subtype);
    }
  });

  it("joins the turn for a mid-turn system row that arrives before the first reply", () => {
    const nodes = nodesOf([
      userMsg(10, 0),
      msg({
        id: 11,
        ordinal: 1,
        role: "user",
        is_system: true,
        source_subtype: "task_notification",
        content: "<task-notification>done</task-notification>",
        timestamp: "2025-02-17T21:04:30Z",
      }),
      msg({ id: 12, ordinal: 2, content: "reply", timestamp: "2025-02-17T21:05:00Z" }),
    ]);
    expect(kindsOf(nodes)).toEqual(["prompt", "assistant-turn"]);
    const turn = nodes[1]!;
    if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
    // The key and firstMessageId still anchor on the first assistant
    // message, but timestamp is the first member's — the system row.
    expect(turn.key).toBe("s1:turn:12");
    expect(turn.firstMessageId).toBe(12);
    expect(turn.timestamp).toBe("2025-02-17T21:04:30Z");
    expect(turn.events.map((e) => e.kind)).toEqual(["system", "message"]);
  });

  it("ends the turn at continuation, resume, and interrupted cards", () => {
    for (const subtype of ["continuation", "resume", "interrupted"]) {
      const nodes = nodesOf([
        userMsg(10, 0),
        assistantMsg(11, 1, "partial"),
        boundaryMsg(12, 2, subtype, "card"),
        assistantMsg(13, 3, "continued"),
      ]);
      expect(kindsOf(nodes)).toEqual(["prompt", "assistant-turn", "standalone", "assistant-turn"]);
      const standalone = nodes[2]!;
      if (standalone.kind !== "standalone") throw new Error("expected standalone");
      expect(standalone.ordinals).toEqual([2]);
    }
  });

  it("ends the turn at a compact boundary divider", () => {
    const nodes = nodesOf([
      userMsg(10, 0),
      assistantMsg(11, 1, "answer"),
      msg({ id: 12, ordinal: 2, content: "summary so far", is_compact_boundary: true }),
      userMsg(13, 3, "next"),
    ]);
    expect(kindsOf(nodes)).toEqual(["prompt", "assistant-turn", "standalone", "prompt"]);
  });

  it("groups assistant items before the first prompt into their own turn", () => {
    const nodes = nodesOf([assistantMsg(10, 0, "continuing work"), userMsg(11, 1, "new ask")]);
    expect(kindsOf(nodes)).toEqual(["assistant-turn", "prompt"]);
    const turn = nodes[0]!;
    if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
    expect(turn.key).toBe("s1:turn:10");
  });

  it("keeps sidechain items inside the parent turn", () => {
    const nodes = nodesOf([
      userMsg(10, 0),
      assistantMsg(11, 1, "spawning an agent"),
      msg({ id: 12, ordinal: 2, content: "subagent step", is_sidechain: true }),
      assistantMsg(13, 3, "done"),
    ]);
    expect(kindsOf(nodes)).toEqual(["prompt", "assistant-turn"]);
    const turn = nodes[1]!;
    if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
    expect(turn.ordinals).toEqual([1, 2, 3]);
  });

  it("does not treat hidden system user rows as prompts", () => {
    const nodes = nodesOf([
      userMsg(10, 0, "real ask"),
      msg({ id: 11, ordinal: 1, role: "user", is_system: true, content: "hidden notice" }),
      assistantMsg(12, 2, "answer"),
    ]);
    expect(kindsOf(nodes)).toEqual(["prompt", "assistant-turn"]);
  });

  it("keeps a tool-only assistant run as one tool-rollup event inside the turn", () => {
    const nodes = nodesOf([
      userMsg(10, 0),
      assistantMsg(11, 1, "checking"),
      toolMsg(12, 2),
      toolMsg(13, 3, "Read", "a.ts"),
      toolMsg(14, 4, "Edit", "b.ts"),
      assistantMsg(15, 5, "final"),
    ]);
    const turn = nodes[1]!;
    if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
    const rollup = turn.events[1]!;
    expect(rollup.kind).toBe("tool-rollup");
    expect(rollup.toolMessages?.map((m) => m.id)).toEqual([12, 13, 14]);
    expect(rollup.ordinals).toEqual([2, 3, 4]);
  });

  it("emits a mid-turn system row as standalone when no turn ever opens", () => {
    const nodes = nodesOf([
      boundaryMsg(10, 0, "task_notification", "<task-notification>done</task-notification>"),
      userMsg(11, 1),
    ]);
    expect(kindsOf(nodes)).toEqual(["standalone", "prompt"]);
  });

  it("keeps an empty-content assistant message as an eventless turn", () => {
    const nodes = nodesOf([userMsg(10, 0), assistantMsg(11, 1, "")]);
    expect(kindsOf(nodes)).toEqual(["prompt", "assistant-turn"]);
    const turn = nodes[1]!;
    if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
    expect(turn.events).toEqual([]);
    expect(turn.finalOutput).toBeNull();
  });

  it("keeps a whitespace-only message event out of finalOutput", () => {
    const nodes = nodesOf([userMsg(10, 0), assistantMsg(11, 1, "   ")]);
    const turn = nodes[1]!;
    if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
    expect(turn.events.map((e) => e.kind)).toEqual(["message"]);
    expect(turn.finalOutput).toBeNull();
  });

  it("merges non-contiguous tool segments into one trailing tool event", () => {
    const content = "[Bash]\n$ a\n\nbetween calls\n\n[Read]\nf.ts";
    const nodes = nodesOf([
      userMsg(10, 0),
      msg({ id: 11, ordinal: 1, content, has_tool_use: true }),
    ]);
    const turn = nodes[1]!;
    if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
    expect(turn.events.map((e) => e.kind)).toEqual(["message", "tool"]);
    const tool = turn.events[1]!;
    // The tool event keys off its first segment but its segment list is
    // sparse: indexes 0 and 2 of the source message's segments.
    expect(tool.segmentIndex).toBe(0);
    expect(tool.segments?.map((s) => s.label)).toEqual(["Bash", "Read"]);
    expect(turn.events[0]!.segmentIndex).toBe(1);
  });

  it("emits separate message events for text runs split by thinking", () => {
    const content = "First part.\n\n[Thinking]\nreconsider\n[/Thinking]\n\nSecond part.";
    const nodes = nodesOf([
      userMsg(10, 0),
      msg({ id: 11, ordinal: 1, content, has_thinking: true }),
    ]);
    const turn = nodes[1]!;
    if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
    expect(turn.events.map((e) => e.kind)).toEqual(["message", "thinking", "message"]);
    expect(turn.events[0]!.segmentIndex).toBe(0);
    expect(turn.events[2]!.segmentIndex).toBe(2);
    expect(turn.events[0]!.key).toBe("s1:11:message:0");
    expect(turn.events[2]!.key).toBe("s1:11:message:2");
  });

  it("reports the turn model and first member timestamp", () => {
    const nodes = nodesOf([
      userMsg(10, 0),
      msg({
        id: 11,
        ordinal: 1,
        content: "working",
        model: "claude-opus-4",
        timestamp: "2025-02-17T21:05:00Z",
      }),
      msg({
        id: 12,
        ordinal: 2,
        content: "done",
        model: "claude-opus-4",
        timestamp: "2025-02-17T21:06:00Z",
      }),
    ]);
    const turn = nodes[1]!;
    if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
    expect(turn.model).toBe("claude-opus-4");
    expect(turn.timestamp).toBe("2025-02-17T21:05:00Z");
  });

  it("returns no nodes when every row is a hidden system message", () => {
    const nodes = nodesOf([
      msg({ id: 10, ordinal: 0, role: "user", is_system: true, content: "hidden a" }),
      msg({ id: 11, ordinal: 1, role: "user", is_system: true, content: "hidden b" }),
    ]);
    expect(nodes).toEqual([]);
  });

  it("emits per-message tool events for ungrouped tool-only items", () => {
    const items = buildDisplayItems(
      [userMsg(10, 0), toolMsg(11, 1), toolMsg(12, 2, "Read", "f.ts")],
      { skipToolGrouping: true },
    );
    const nodes = buildTranscriptNodes(items, "s1");
    expect(kindsOf(nodes)).toEqual(["prompt", "assistant-turn"]);
    const turn = nodes[1]!;
    if (turn.kind !== "assistant-turn") throw new Error("expected a turn");
    expect(turn.events.map((e) => e.kind)).toEqual(["tool", "tool"]);
    expect(turn.events.map((e) => e.message.id)).toEqual([11, 12]);
    expect(turn.events.map((e) => e.key)).toEqual(["s1:11:tool:0", "s1:12:tool:0"]);
  });
});
