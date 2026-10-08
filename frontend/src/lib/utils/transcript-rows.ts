/**
 * Flattens the transcript node tree into the rows the virtualized
 * transcript renders.
 *
 * Prompt and standalone nodes keep their flat display-item row. An
 * assistant turn becomes a header row plus, when expanded, one row per
 * visible child event, plus a separate final-output row whenever its
 * event survives the block filters. Collapsed turns emit just the
 * header and final output, so the answer stays visible while the work
 * stays folded.
 *
 * `ordinals` is the click/selection lookup surface: a collapsed header
 * retains every member ordinal. `progressOrdinals` is what read
 * progress and the unread divider count — only ordinals whose content
 * is actually rendered by the row.
 *
 * Pure projection — no store imports. Callers supply the expansion and
 * visibility predicates so the flatten itself stays testable.
 */
import type { AssistantTurnItem, TranscriptNode, TurnEvent } from "./assistant-turns.js";
import type { DisplayItem } from "./display-items.js";

export type TranscriptRow =
  | {
      kind: "display";
      key: string;
      item: DisplayItem;
      ordinals: number[];
      progressOrdinals: number[];
    }
  | {
      kind: "turn-header";
      key: string;
      turn: AssistantTurnItem;
      ordinals: number[];
      progressOrdinals: [];
    }
  | {
      kind: "turn-event";
      key: string;
      turn: AssistantTurnItem;
      event: TurnEvent;
      ordinals: number[];
      progressOrdinals: number[];
    }
  | {
      kind: "final-output";
      key: string;
      turn: AssistantTurnItem;
      event: TurnEvent;
      ordinals: number[];
      progressOrdinals: number[];
    };

/** Wraps a flat display item as a transcript row, preserving the
 *  `${sessionId}-m-*` / `${sessionId}-tg-*` keys the virtual list used
 *  for the flat source. */
export function displayTranscriptRow(item: DisplayItem): TranscriptRow {
  const sessionId =
    item.kind === "tool-group" ? (item.messages[0]?.session_id ?? "") : item.message.session_id;
  const key =
    item.kind === "tool-group"
      ? `${sessionId}-tg-${item.ordinals[0]}`
      : `${sessionId}-m-${item.message.ordinal}`;
  return {
    kind: "display",
    key,
    item,
    ordinals: [...item.ordinals],
    progressOrdinals: [...item.ordinals],
  };
}

/**
 * Flattens `nodes` into virtual rows in display order. When
 * `newestFirst` is set, top-level nodes and each turn's child events
 * are reversed, while every turn header stays first and its
 * final-output row sits immediately after the header. Chronological
 * order places the final-output row after the child events — the same
 * tail position it holds in the source.
 *
 * A turn that would render nothing — no visible events and no visible
 * final output — is skipped entirely so no blank virtual row remains.
 * The exception is a still-empty streaming turn: it owns no events yet,
 * but `isTurnVisible` may keep its collapsed header mounted so the
 * pending reply does not vanish before the first token.
 */
export function flattenTranscriptRows(
  nodes: readonly TranscriptNode[],
  isTurnExpanded: (key: string) => boolean,
  isEventVisible: (event: TurnEvent) => boolean,
  newestFirst: boolean,
  isTurnVisible: (turn: AssistantTurnItem) => boolean = () => false,
): TranscriptRow[] {
  const rows: TranscriptRow[] = [];
  const ordered = newestFirst ? [...nodes].reverse() : nodes;

  for (const node of ordered) {
    if (node.kind !== "assistant-turn") {
      rows.push(displayTranscriptRow(node.item));
      continue;
    }

    const turn = node;
    const sessionId = turn.messages[0]?.session_id ?? "";
    const visibleEvents = turn.events.filter(isEventVisible);
    // The final output is itself a member event, so it can only render
    // when at least one event is visible — meaning an empty visible set
    // guarantees there is nothing to draw and the turn is skipped. An
    // eventless turn (the pre-first-token streaming window) is the one
    // exception the caller may keep: its collapsed header still marks
    // the pending reply.
    if (visibleEvents.length === 0 && (turn.events.length > 0 || !isTurnVisible(turn))) {
      continue;
    }

    const output =
      turn.finalOutput !== null && isEventVisible(turn.finalOutput) ? turn.finalOutput : null;
    const expanded = isTurnExpanded(turn.key);
    const events = newestFirst ? [...visibleEvents].reverse() : visibleEvents;

    const header: TranscriptRow = {
      kind: "turn-header",
      key: `${sessionId}-th-${turn.key}`,
      turn,
      ordinals: [...turn.ordinals],
      progressOrdinals: [],
    };
    const outputRow: TranscriptRow | null = output
      ? {
          kind: "final-output",
          key: `${sessionId}-fo-${turn.key}`,
          turn,
          event: output,
          ordinals: [...output.ordinals],
          progressOrdinals: [...output.ordinals],
        }
      : null;
    const eventRows: TranscriptRow[] = expanded
      ? events.map((event) => ({
          kind: "turn-event",
          key: `${sessionId}-te-${event.key}`,
          turn,
          event,
          ordinals: [...event.ordinals],
          progressOrdinals: [...event.ordinals],
        }))
      : [];

    rows.push(header);
    if (newestFirst) {
      if (outputRow) rows.push(outputRow);
      rows.push(...eventRows);
    } else {
      rows.push(...eventRows);
      if (outputRow) rows.push(outputRow);
    }
  }

  return rows;
}

/** The child event that renders `blockKey` inside `turn`. One source
 *  ordinal can span several events of the same message (thinking, text,
 *  tool segments), so a search match resolves by block kind and segment
 *  index rather than the first ordinal hit. Returns `null` when no event
 *  maps. Kept in sync with the ownership helper `MessageList` applies for
 *  search navigation — the find overview rail uses this copy to pin
 *  matches to the event row that renders them. */
export function blockOwnerEvent(
  turn: AssistantTurnItem,
  ordinal: number,
  blockKey: string,
): TurnEvent | null {
  const parts = blockKey.split(":");
  const kind = parts[1] ?? "";
  // `${ordinal}:${kind}:${index}` carries the enriched segment index
  // for text/code/thinking/skill blocks; `seg`-prefixed or fractional
  // trailing parts parse to NaN and only degrade to kind matching.
  const segmentIndex = Number(parts[2]);
  const hasSegmentIndex = Number.isInteger(segmentIndex);
  const candidates = turn.events.filter((event) =>
    event.ordinals.includes(ordinal),
  );
  const byKinds = (...kinds: TurnEvent["kind"][]) =>
    candidates.find((event) => kinds.includes(event.kind)) ?? null;
  const bySegmentIndex = (kind: TurnEvent["kind"]) =>
    candidates.find(
      (event) =>
        hasSegmentIndex &&
        event.kind === kind &&
        event.segmentIndex === segmentIndex,
    ) ?? null;
  // Adjacent text/code segments merge into one event covering
  // [segmentIndex, segmentIndex + segments.length); an index from a
  // later segment still belongs to that run's disclosure.
  const inRun = (kind: TurnEvent["kind"]) =>
    candidates.find(
      (event) =>
        hasSegmentIndex &&
        event.kind === kind &&
        event.segmentIndex !== undefined &&
        segmentIndex >= event.segmentIndex &&
        segmentIndex < event.segmentIndex + (event.segments?.length ?? 1),
    ) ?? null;
  switch (kind) {
    case "text":
    case "code":
      return inRun("message") ?? byKinds("message");
    case "thinking":
      return bySegmentIndex("thinking") ?? byKinds("thinking");
    case "skill":
      return bySegmentIndex("skill") ?? byKinds("skill");
    case "tool-input":
    case "tool-output":
    case "tool-history":
      // The nested input/output/history sections share the owning
      // tool event's row; the reveal only needs the row mounted —
      // `searchCollapsed` discloses the matching section itself.
      return byKinds("tool", "tool-rollup");
    default:
      return null;
  }
}

/**
 * Maps a source message ordinal to the turn and child event that own
 * it — the seam navigation and search reveal use to expand the owning
 * turn before scrolling. Returns `null` when no turn contains the
 * ordinal; `event` is `null` when the ordinal belongs to a member
 * message that produced no event rows of its own.
 */
export function findOrdinalOwner(
  nodes: readonly TranscriptNode[],
  ordinal: number,
): { turn: AssistantTurnItem; event: TurnEvent | null } | null {
  for (const node of nodes) {
    if (node.kind !== "assistant-turn" || !node.ordinals.includes(ordinal)) {
      continue;
    }
    return {
      turn: node,
      event: node.events.find((event) => event.ordinals.includes(ordinal)) ?? null,
    };
  }
  return null;
}
