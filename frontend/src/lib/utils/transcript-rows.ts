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
 */
export function flattenTranscriptRows(
  nodes: readonly TranscriptNode[],
  isTurnExpanded: (key: string) => boolean,
  isEventVisible: (event: TurnEvent) => boolean,
  newestFirst: boolean,
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
    // guarantees there is nothing to draw and the turn is skipped.
    if (visibleEvents.length === 0) continue;

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
