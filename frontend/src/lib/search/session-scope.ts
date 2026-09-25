/**
 * One projection of what the current transcript renders.
 *
 * The transcript components and the in-session search index both build their
 * view of a session here, so a block filter can never hide a message in the
 * DOM while search still counts or reveals it. The module stays free of store
 * imports: callers pass the current filters in and receive display items plus
 * the block kinds those items actually render.
 *
 * The scope separates three layers:
 *
 * - `items` — the assistant-turn tree with full group membership, built
 *   before child block-type visibility is applied.
 * - `displayItems`/`normalItems` — the flat lists the current mode and normal
 *   mode render. An ephemeral `revealedBlocks` overlay may unhide types
 *   while search is active without touching the saved filter.
 * - `messages` — every source message eligible for the selected mode,
 *   independent of block filters, so the search index sees all of them.
 */
import type { DbMessage as Message } from "../api/generated/index.js";
import type { BlockType } from "../stores/ui.svelte.js";
import { buildTranscriptNodes, type TranscriptNode } from "../utils/assistant-turns.js";
import { hasVisibleSegments } from "../utils/content-parser.js";
import { buildDisplayItems, type DisplayItem } from "../utils/display-items.js";
import { isSystemMessage } from "../utils/messages.js";
import { filterDisplayItemsByTranscriptMode } from "../utils/transcript-mode.js";
import type { SearchBlockKind } from "./block-text.js";

export interface SessionScopeInput {
  messages: readonly Message[];
  /** Transcript mode; absent means normal. */
  transcriptMode?: "normal" | "focused";
  /** Visible block types; absent means every type is visible. */
  visibleBlocks?: ReadonlySet<BlockType>;
  /** Whether any block type is hidden; absent means no filters. */
  hasBlockFilters?: boolean;
  /** Types an open search temporarily reveals; ephemeral overlay only. */
  revealedBlocks?: ReadonlySet<BlockType>;
  /** Provider rule that keeps an answer before its trailing tool calls. */
  keepAnswerBeforeTrailingTools?: boolean;
  /** Session id used for stable transcript-node keys; derived otherwise. */
  sessionId?: string;
}

export interface SessionScope {
  /**
   * Transcript nodes for the selected mode with full group membership.
   * Staged for the turn-collapse rendering tasks; nothing consumes it in
   * production yet.
   */
  items: TranscriptNode[];
  /** Display items the current transcript mode and filters render. */
  displayItems: DisplayItem[];
  /** Display items normal mode renders under the same block filters. */
  normalItems: DisplayItem[];
  /** Messages eligible for the current mode regardless of block filters. */
  messages: Message[];
  /** Whether an included message's block kind survives the saved filter. */
  allowsBlock(message: Message, kind: SearchBlockKind): boolean;
}

/** Look up the provider preference that focused mode respects. */
export function keepsAnswerBeforeTrailingTools(
  providers: readonly { id: string; post_answer_tool_work?: boolean | null }[],
  agentId: string | null | undefined,
): boolean {
  if (agentId == null) return false;
  return providers.some(
    (provider) => provider.id === agentId && provider.post_answer_tool_work === true,
  );
}

/** Map a searchable block kind to the filter type that owns it. */
export function blockTypeForKind(kind: SearchBlockKind, role: Message["role"]): BlockType {
  switch (kind) {
    case "thinking":
      return "thinking";
    case "code":
      return "code";
    case "tool-input":
    case "tool-output":
    case "tool-history":
      return "tool";
    case "text":
    case "skill":
      return role === "user" ? "user" : "assistant";
  }
}

export function projectSessionScope(input: SessionScopeInput): SessionScope {
  const visibleBlocks = input.visibleBlocks;
  const revealedBlocks = input.revealedBlocks;
  const isVisible = (type: BlockType): boolean =>
    visibleBlocks === undefined || visibleBlocks.has(type);
  // The search reveal layers over the saved filter for rendered output only;
  // it is never persisted to the user's block-visibility preference.
  // Must agree with InSessionSearchStore.isBlockEffectivelyVisible, which
  // applies the same saved-or-revealed rule inside message components.
  const isEffectivelyVisible = (type: BlockType): boolean =>
    isVisible(type) || (revealedBlocks?.has(type) ?? false);
  // A filtered code fence still renders its manually expandable placeholder.
  // Keep the row in the transcript without adding its code to the search index.
  const isTranscriptBlockVisible = (type: BlockType): boolean =>
    type === "code" || isEffectivelyVisible(type);
  const hasBlockFilters = (input.hasBlockFilters ?? false) || (revealedBlocks?.size ?? 0) > 0;
  const transcriptMode = input.transcriptMode ?? "normal";
  const sessionId = input.sessionId ?? input.messages[0]?.session_id ?? "";

  const baseMessages = input.messages.filter((message) => !isSystemMessage(message));
  const baseItems = buildDisplayItems(baseMessages);
  const filteredItems = buildDisplayItems(baseMessages, {
    skipToolGrouping: !isEffectivelyVisible("tool"),
  });
  const itemVisible = (item: DisplayItem): boolean =>
    item.kind === "tool-group" || hasVisibleSegments(item.message, isTranscriptBlockVisible);

  // Full mode-aware membership before child block-type filtering. This feeds
  // both the transcript tree and the search index's message set, so hidden
  // block types keep their membership and stay searchable.
  const scopedItems =
    transcriptMode === "normal"
      ? baseItems
      : filterDisplayItemsByTranscriptMode(baseItems, "focused", {
          keepAnswerBeforeTrailingTools: input.keepAnswerBeforeTrailingTools,
        });
  const items = buildTranscriptNodes(scopedItems, sessionId);

  const normalItems = hasBlockFilters ? filteredItems.filter(itemVisible) : baseItems;
  let displayItems: DisplayItem[];
  if (transcriptMode === "normal") {
    displayItems = normalItems;
  } else if (!hasBlockFilters) {
    displayItems = scopedItems;
  } else {
    displayItems = filterDisplayItemsByTranscriptMode(filteredItems, "focused", {
      keepAnswerBeforeTrailingTools: input.keepAnswerBeforeTrailingTools,
      isMessageVisible: (message) => hasVisibleSegments(message, isTranscriptBlockVisible),
    }).filter(itemVisible);
  }

  const messages: Message[] = [];
  const included = new Set<Message>();
  for (const item of scopedItems) {
    const group = item.kind === "tool-group" ? item.messages : [item.message];
    for (const message of group) {
      if (included.has(message)) continue;
      included.add(message);
      messages.push(message);
    }
  }

  return {
    items,
    displayItems,
    normalItems,
    messages,
    allowsBlock: (message, kind) =>
      included.has(message) && isVisible(blockTypeForKind(kind, message.role)),
  };
}
