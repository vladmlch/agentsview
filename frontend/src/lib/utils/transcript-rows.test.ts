import { describe, expect, it } from "vite-plus/test";
import type { DbMessage as Message } from "../api/generated/index.js";
import { buildDisplayItems } from "./display-items.js";
import {
  buildTranscriptNodes,
  type AssistantTurnItem,
  type TranscriptNode,
  type TurnEvent,
} from "./assistant-turns.js";
import { flattenTranscriptRows } from "./transcript-rows.js";

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

function nodesOf(messages: Message[]): TranscriptNode[] {
  return buildTranscriptNodes(buildDisplayItems(messages), "s1");
}

function turnOf(nodes: TranscriptNode[]): AssistantTurnItem {
  const turn = nodes.find((node) => node.kind === "assistant-turn");
  if (!turn || turn.kind !== "assistant-turn") {
    throw new Error("expected an assistant-turn node");
  }
  return turn;
}

const expanded = () => true;
const collapsed = () => false;
const allVisible = () => true;
const noneVisible = () => false;

function kinds(rows: ReturnType<typeof flattenTranscriptRows>) {
  return rows.map((row) => row.kind);
}

describe("flattenTranscriptRows", () => {
  it("flattens a collapsed turn into a header row and a separate final-output row", () => {
    const nodes = nodesOf([
      userMsg(10, 0, "go"),
      assistantMsg(11, 1, "Working."),
      assistantMsg(12, 2, "More work."),
      assistantMsg(13, 3, "Final answer."),
    ]);
    const rows = flattenTranscriptRows(nodes, collapsed, allVisible, false);

    expect(kinds(rows)).toEqual(["display", "turn-header", "final-output"]);

    const header = rows[1]!;
    // The collapsed header keeps every member ordinal for click/selection
    // lookup but contributes nothing to read progress.
    expect(header.ordinals).toEqual([1, 2, 3]);
    expect(header.progressOrdinals).toEqual([]);
    expect(header.key).toBe("s1-th-s1:turn:11");

    const output = rows[2]!;
    expect(output.kind).toBe("final-output");
    if (output.kind !== "final-output") return;
    expect(output.event).toBe(turnOf(nodes).finalOutput);
    expect(output.ordinals).toEqual([3]);
    expect(output.progressOrdinals).toEqual([3]);
    expect(output.key).toBe("s1-fo-s1:turn:11");
  });

  it("emits one child row per event between the header and the final output when expanded", () => {
    const nodes = nodesOf([
      userMsg(10, 0, "go"),
      assistantMsg(11, 1, "[Thinking]\nplan\n[/Thinking]\n\nFirst."),
      assistantMsg(12, 2, "Final answer."),
    ]);
    const turn = turnOf(nodes);
    const rows = flattenTranscriptRows(nodes, expanded, allVisible, false);

    expect(kinds(rows)).toEqual([
      "display",
      "turn-header",
      "turn-event",
      "turn-event",
      "turn-event",
      "final-output",
    ]);

    const eventRows = rows.filter((row) => row.kind === "turn-event");
    expect(eventRows.map((row) => (row.kind === "turn-event" ? row.event : null))).toEqual(
      turn.events,
    );
    // Row keys derive from the source event keys.
    expect(eventRows.map((row) => row.key)).toEqual(
      turn.events.map((event: TurnEvent) => `s1-te-${event.key}`),
    );
    // Each child row carries its own source ordinals for read progress.
    expect(eventRows.map((row) => row.progressOrdinals)).toEqual([[1], [1], [2]]);
  });

  it("keeps the header first and the final output second while reversing nodes and child events newest-first", () => {
    const nodes = nodesOf([
      userMsg(10, 0, "go"),
      assistantMsg(11, 1, "[Thinking]\nplan\n[/Thinking]\n\nFirst."),
      assistantMsg(12, 2, "Final answer."),
      userMsg(13, 3, "again"),
      assistantMsg(14, 4, "Second answer."),
    ]);
    const rows = flattenTranscriptRows(nodes, expanded, allVisible, true);

    // Top-level nodes reverse: the second turn leads, then the second
    // prompt, then the first turn, then the first prompt. Each turn
    // keeps header first, output second, and its child events in
    // reverse order.
    expect(kinds(rows)).toEqual([
      "turn-header",
      "final-output",
      "turn-event",
      "display",
      "turn-header",
      "final-output",
      "turn-event",
      "turn-event",
      "turn-event",
      "display",
    ]);

    const firstTurnRows = rows.slice(4);
    const firstTurnEvents = firstTurnRows.filter((row) => row.kind === "turn-event");
    // Source order was thinking, message(11), message(12); newest-first
    // reverses to message(12), message(11), thinking.
    expect(
      firstTurnEvents.map((row) => (row.kind === "turn-event" ? row.event.kind : null)),
    ).toEqual(["message", "message", "thinking"]);
    expect(firstTurnEvents.map((row) => (row.kind === "turn-event" ? row.ordinals : null))).toEqual(
      [[2], [1], [1]],
    );

    const leading = rows[0]!;
    expect(leading.kind).toBe("turn-header");
    if (leading.kind === "turn-header") {
      expect(leading.turn.key).toBe("s1:turn:14");
    }
    const output = rows[1]!;
    expect(output.kind).toBe("final-output");
    if (output.kind === "final-output") {
      expect(output.ordinals).toEqual([4]);
      expect(output.progressOrdinals).toEqual([4]);
    }
  });

  it("preserves display-item keys for prompt and standalone rows", () => {
    const nodes = nodesOf([userMsg(10, 0, "go"), assistantMsg(11, 1, "Solo reply.")]);
    const rows = flattenTranscriptRows(nodes, collapsed, allVisible, false);
    const prompt = rows[0]!;
    expect(prompt.kind).toBe("display");
    expect(prompt.key).toBe("s1-m-0");
    expect(prompt.ordinals).toEqual([0]);
    expect(prompt.progressOrdinals).toEqual([0]);
  });

  it("keys tool-rollup child rows by the rollup event key", () => {
    const nodes = nodesOf([
      userMsg(10, 0, "go"),
      assistantMsg(11, 1, "Working."),
      toolMsg(12, 2),
      toolMsg(13, 3, "Read", "file.ts"),
      assistantMsg(14, 4, "Final answer."),
    ]);
    const rows = flattenTranscriptRows(nodes, expanded, allVisible, false);
    const rollup = rows.find(
      (row) => row.kind === "turn-event" && row.event.kind === "tool-rollup",
    );
    expect(rollup).toBeDefined();
    expect(rollup!.key).toBe("s1-te-s1:12:tool-rollup:0");
    // The rollup row carries every grouped tool message's source ordinals.
    expect(rollup!.ordinals).toEqual([2, 3]);
    expect(rollup!.progressOrdinals).toEqual([2, 3]);
  });

  it("skips a turn entirely when no event and no final output would render", () => {
    const nodes = nodesOf([
      userMsg(10, 0, "go"),
      assistantMsg(11, 1, "Working."),
      assistantMsg(12, 2, "Final answer."),
    ]);
    const rows = flattenTranscriptRows(nodes, expanded, noneVisible, false);
    expect(kinds(rows)).toEqual(["display"]);
  });

  it("omits the final-output row when its filter hides it but keeps the header and events", () => {
    const nodes = nodesOf([
      userMsg(10, 0, "go"),
      assistantMsg(11, 1, "[Thinking]\nplan\n[/Thinking]\n\nFirst."),
      assistantMsg(12, 2, "Final answer."),
    ]);
    // Hide "message" events; thinking still renders.
    const rows = flattenTranscriptRows(
      nodes,
      expanded,
      (event) => event.kind === "thinking",
      false,
    );
    expect(kinds(rows)).toEqual(["display", "turn-header", "turn-event"]);
  });

  it("does not emit event rows for a collapsed turn even when they are visible", () => {
    const nodes = nodesOf([
      userMsg(10, 0, "go"),
      assistantMsg(11, 1, "[Thinking]\nplan\n[/Thinking]\n\nFirst."),
      assistantMsg(12, 2, "Final answer."),
    ]);
    const rows = flattenTranscriptRows(nodes, collapsed, allVisible, false);
    expect(kinds(rows)).toEqual(["display", "turn-header", "final-output"]);
  });
});
