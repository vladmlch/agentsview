/**
 * Shared effective-visibility predicate for typed turn events.
 *
 * `AssistantTurn` and the transcript-row flattening in `MessageList`
 * must agree on whether a child event renders anything under the
 * current block-visibility filters, so the rule lives here once. The
 * caller supplies the saved-or-revealed block predicate
 * (`inSessionSearch.isBlockEffectivelyVisible`) to keep this module
 * store-free.
 */
import type { TurnEvent } from "./assistant-turns.js";
import type { BlockType } from "../stores/ui.svelte.js";

export function isTurnEventVisible(
  event: TurnEvent,
  isEffectivelyVisible: (type: BlockType) => boolean,
): boolean {
  switch (event.kind) {
    case "thinking":
      return isEffectivelyVisible("thinking");
    case "tool":
    case "tool-rollup":
      return isEffectivelyVisible("tool");
    case "skill":
      return isEffectivelyVisible(event.message.role === "user" ? "user" : "assistant");
    case "system":
      return isEffectivelyVisible("system");
    default: {
      const roleVisible = isEffectivelyVisible(
        event.message.role === "user" ? "user" : "assistant",
      );
      // Mirror isTranscriptBlockVisible in session-scope.ts: a filtered
      // code fence still renders its manually expandable placeholder, so
      // a code segment keeps the event visible regardless of the filter.
      return (event.segments ?? []).some((segment) => segment.type === "code" || roleVisible);
    }
  }
}
