/**
 * Projects flat display items into typed transcript nodes: real user
 * prompts, standalone structural cards (compact boundaries and
 * turn-ending system cards), and assistant turns split into ordered
 * thinking/message/skill/tool/system child events.
 *
 * Pure projection — no store imports. Source messages stay flat; turn
 * and child-event keys are built from source `DbMessage.id` values
 * scoped by the session id so they stay stable across pagination and
 * remounts.
 */
import type { DbMessage as Message, DbToolCall as ToolCall } from "../api/generated/index.js";
import { enrichSegments, parseContent, type ContentSegment } from "./content-parser.js";
import type { DisplayItem, MessageItem } from "./display-items.js";
import { isMidTurnSystemMessage, isSystemBoundaryMessage, isSystemMessage } from "./messages.js";
import { computeMainModel } from "./model.js";

export type TurnEventKind = "message" | "thinking" | "tool" | "skill" | "tool-rollup" | "system";

export interface TurnEvent {
  key: string;
  kind: TurnEventKind;
  message: Message;
  ordinals: number[];
  /** Index of the event's first segment in the source message's enriched
   *  segment array. Text/code runs are contiguous, so `segments[k]` came
   *  from index `segmentIndex + k`; a merged `tool` event's segments may
   *  be sparse. Absent on `system` and `tool-rollup` events, which cover
   *  whole messages rather than segments. */
  segmentIndex?: number;
  segments?: ContentSegment[];
  toolMessages?: Message[];
  toolCalls?: ToolCall[];
  label?: string;
}

export interface AssistantTurnItem {
  kind: "assistant-turn";
  key: string;
  /** `DbMessage.id` of the first assistant-authored member — the turn's
   *  anchor and the source of `key`. Can differ from `messages[0]` when
   *  a mid-turn system row opened the turn before the first reply. */
  firstMessageId: number;
  messages: Message[];
  events: TurnEvent[];
  ordinals: number[];
  finalOutput: TurnEvent | null;
  model: string | null;
  /** Timestamp of `messages[0]` — the turn's true start, including a
   *  mid-turn system row that arrived before the first assistant reply. */
  timestamp: string;
}

export interface PromptNode {
  kind: "prompt";
  item: MessageItem;
  ordinals: number[];
}

export interface StandaloneNode {
  kind: "standalone";
  item: DisplayItem;
  ordinals: number[];
}

export type TranscriptNode = PromptNode | StandaloneNode | AssistantTurnItem;

/** Child-event key `${sessionId}:${messageId}:${kind}:${segmentIndex}`.
 *  `segmentIndex` is the first covered segment's index for
 *  message/thinking/skill/tool events; `system` and `tool-rollup`
 *  events cover whole messages or groups and pass literal 0. */
function eventKey(
  sessionId: string,
  messageId: number,
  kind: TurnEventKind,
  segmentIndex: number,
): string {
  return `${sessionId}:${messageId}:${kind}:${segmentIndex}`;
}

/**
 * Splits one source message into ordered events. Adjacent text/code
 * segments merge into a single message event; thinking and skill
 * segments stand alone. All tool segments collapse into one trailing
 * tool event — matching the existing render order where tool blocks
 * follow the prose — and keep the message's structured tool calls
 * together so the ParallelGroup path still applies.
 */
function messageEvents(message: Message, sessionId: string): TurnEvent[] {
  const segments = enrichSegments(
    parseContent(message.content, message.has_tool_use, message.id, message.content_length),
    message.tool_calls,
  );
  const events: TurnEvent[] = [];
  const toolIndexes: number[] = [];
  let textStart = -1;
  let textRun: ContentSegment[] = [];

  const flushText = () => {
    if (textStart < 0) return;
    events.push({
      key: eventKey(sessionId, message.id, "message", textStart),
      kind: "message",
      message,
      ordinals: [message.ordinal],
      segmentIndex: textStart,
      segments: textRun,
    });
    textStart = -1;
    textRun = [];
  };

  segments.forEach((segment, index) => {
    switch (segment.type) {
      case "text":
      case "code":
        if (textStart < 0) textStart = index;
        textRun.push(segment);
        break;
      case "tool":
        flushText();
        toolIndexes.push(index);
        break;
      default:
        flushText();
        events.push({
          key: eventKey(sessionId, message.id, segment.type, index),
          kind: segment.type,
          message,
          ordinals: [message.ordinal],
          segmentIndex: index,
          segments: [segment],
          label: segment.label,
        });
    }
  });
  flushText();

  if (toolIndexes.length > 0) {
    const firstTool = toolIndexes[0]!;
    events.push({
      key: eventKey(sessionId, message.id, "tool", firstTool),
      kind: "tool",
      message,
      ordinals: [message.ordinal],
      segmentIndex: firstTool,
      segments: toolIndexes.map((i) => segments[i]!),
      toolCalls: message.tool_calls?.length ? message.tool_calls : undefined,
      label: toolIndexes.length === 1 ? segments[firstTool]!.label : undefined,
    });
  }
  return events;
}

/**
 * The last non-empty assistant-authored message event wins; trailing
 * tool, thinking, and system events never replace it.
 */
function findFinalOutput(events: TurnEvent[]): TurnEvent | null {
  for (let i = events.length - 1; i >= 0; i--) {
    const event = events[i]!;
    if (event.kind !== "message" || event.message.role !== "assistant") continue;
    if (event.segments?.some((s) => s.content.trim().length > 0)) return event;
  }
  return null;
}

export function buildTranscriptNodes(
  items: readonly DisplayItem[],
  sessionId: string,
): TranscriptNode[] {
  const nodes: TranscriptNode[] = [];

  // Accumulates the open assistant turn. `anchor` is the first
  // assistant-authored message and only gets set by assistant content or
  // tool rollups; a pending group holding nothing but mid-turn system
  // rows degrades back to standalone cards instead of forming a turn.
  interface PendingTurn {
    items: DisplayItem[];
    messages: Message[];
    events: TurnEvent[];
    anchor?: Message;
  }
  let pending: PendingTurn | null = null;

  const openTurn = (): PendingTurn => (pending ??= { items: [], messages: [], events: [] });

  const standalone = (item: DisplayItem) => {
    nodes.push({ kind: "standalone", item, ordinals: [...item.ordinals] });
  };

  const flushTurn = () => {
    if (!pending) return;
    const anchor = pending.anchor;
    if (anchor === undefined) {
      for (const item of pending.items) {
        standalone(item);
      }
    } else {
      const messages = pending.messages;
      nodes.push({
        kind: "assistant-turn",
        key: `${sessionId}:turn:${anchor.id}`,
        firstMessageId: anchor.id,
        messages,
        events: pending.events,
        ordinals: messages.map((m) => m.ordinal),
        finalOutput: findFinalOutput(pending.events),
        model: computeMainModel(messages) || null,
        timestamp: messages[0]!.timestamp,
      });
    }
    pending = null;
  };

  for (const item of items) {
    if (item.kind === "tool-group") {
      const [firstTool] = item.messages;
      if (!firstTool) continue;
      const turn = openTurn();
      turn.items.push(item);
      turn.messages.push(...item.messages);
      turn.anchor ??= firstTool;
      turn.events.push({
        key: eventKey(sessionId, firstTool.id, "tool-rollup", 0),
        kind: "tool-rollup",
        message: firstTool,
        ordinals: [...item.ordinals],
        toolMessages: item.messages,
      });
      continue;
    }

    const message = item.message;

    if (message.is_compact_boundary) {
      flushTurn();
      standalone(item);
      continue;
    }
    // Hidden system rows never render; skipping them here keeps an open
    // turn intact instead of splitting it on an invisible message.
    if (isSystemMessage(message)) continue;
    if (isSystemBoundaryMessage(message)) {
      if (isMidTurnSystemMessage(message)) {
        const turn = openTurn();
        turn.items.push(item);
        turn.messages.push(message);
        turn.events.push({
          key: eventKey(sessionId, message.id, "system", 0),
          kind: "system",
          message,
          ordinals: [message.ordinal],
          label: message.source_subtype,
        });
      } else {
        flushTurn();
        standalone(item);
      }
      continue;
    }
    // Sidechain rows belong to their parent turn even on the user role;
    // only a main-line user message is a real prompt boundary.
    if (message.role === "user" && !message.is_sidechain) {
      flushTurn();
      nodes.push({ kind: "prompt", item, ordinals: [...item.ordinals] });
      continue;
    }

    const turn = openTurn();
    turn.items.push(item);
    turn.messages.push(message);
    if (message.role === "assistant") turn.anchor ??= message;
    turn.events.push(...messageEvents(message, sessionId));
  }
  flushTurn();

  return nodes;
}
