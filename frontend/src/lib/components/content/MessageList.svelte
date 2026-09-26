<script lang="ts">
  import { onDestroy, tick, untrack } from "svelte";
  import { Button, EmptyState } from "@kenn-io/kit-ui";
  // kit-ui-check-ignore: MessageList uses the local TanStack wrapper for pinned-message scroll reconciliation and per-session measurement cache resets; kit-ui VirtualList does not expose those controls yet.
  import type { Virtualizer } from "@tanstack/virtual-core";
  import { messages } from "../../stores/messages.svelte.js";
  import { ui } from "../../stores/ui.svelte.js";
  import { sessions } from "../../stores/sessions.svelte.js";
  import { settings } from "../../stores/settings.svelte.js";
  import { readProgress } from "../../stores/read-progress.svelte.js";
  import { CircleQuestionMarkIcon, MessageSquareIcon } from "../../icons.js";
  import { createVirtualizer } from "../../virtual/createVirtualizer.svelte.js";
  import MessageContent from "./MessageContent.svelte";
  import CompactBoundaryDivider from "./CompactBoundaryDivider.svelte";
  import SystemBoundaryCard from "../system/SystemBoundaryCard.svelte";
  import ToolCallGroup from "./ToolCallGroup.svelte";
  import AssistantTurnHeader from "./AssistantTurnHeader.svelte";
  import AssistantTurnEventRow from "./AssistantTurnEventRow.svelte";
  import AssistantTurnOutput from "./AssistantTurnOutput.svelte";
  import type { DbMessage as Message } from "../../api/generated/index.js";
  import type { DisplayItem } from "../../utils/display-items.js";
  import type {
    AssistantTurnItem,
    LeadingTurnAnchor,
    TranscriptNode,
    TurnEvent,
  } from "../../utils/assistant-turns.js";
  import { isTurnEventVisible } from "../../utils/turn-visibility.js";
  import {
    displayTranscriptRow,
    findOrdinalOwner,
    flattenTranscriptRows,
    type TranscriptRow,
  } from "../../utils/transcript-rows.js";
  import { hasVisibleSegments } from "../../utils/content-parser.js";
  import type { BlockType } from "../../stores/ui.svelte.js";
  import {
    isSystemBoundaryMessage,
    isSystemMessage,
  } from "../../utils/messages.js";
  import { resolveMessageLayout } from "../../utils/message-layout.js";
  import { inSessionSearch } from "../../stores/inSessionSearch.svelte.js";
  import { turnCollapse } from "../../stores/turn-collapse.svelte.js";
  import { sessionActivity } from "../../stores/sessionActivity.svelte.js";
  import SessionFindView from "./SessionFindView.svelte";
  import {
    getLatestDisplayIndex,
    type ScrollAlign,
  } from "./message-scroll.js";
  import { m } from "../../i18n/index.js";
  import { settleVirtualScroll } from "./staged-scroll.js";
  import { revealMatch } from "../../search/reveal.js";
  import type { Match } from "../../search/session-index.js";
  import {
    keepsAnswerBeforeTrailingTools,
    projectSessionScope,
  } from "../../search/session-scope.js";

  let containerRef: HTMLDivElement | undefined = $state(undefined);
  let scrollRaf: number | null = null;
  let lastScrollRequest = 0;
  let destroyed = false;
  let activeFollowScrollRequest: number | null = null;
  let followingScrollRaf: number | null = null;
  let followSettleTimer:
    | ReturnType<typeof setTimeout>
    | null = null;
  let visibleProgressSignature: string | null = $state(null);
  let visibleProgressRaf: number | null = null;
  let unreadTraversalKey: string | null = null;
  let unreadBoundarySeen = false;
  let unreadLatestSeen = false;

  let baseMessages: Message[] = $derived.by(() =>
    messages.messages.filter((m) => !isSystemMessage(m)),
  );

  // Share transcript row visibility and searchable block filters with the
  // index. Types a live search reveals render here too, without touching the
  // saved filter.
  let sessionScope = $derived(
    projectSessionScope({
      messages: messages.messages,
      sessionId: messages.sessionId ?? undefined,
      transcriptMode: ui.transcriptMode,
      visibleBlocks: ui.visibleBlocks,
      hasBlockFilters: ui.hasBlockFilters,
      revealedBlocks: inSessionSearch.revealedBlockTypes,
      keepAnswerBeforeTrailingTools: keepsAnswerBeforeTrailingTools(
        settings.sessionProviders,
        sessions.activeSession?.agent,
      ),
    }),
  );

  let displayItemsAsc = $derived(sessionScope.displayItems);

  let normalDisplayItemsAsc = $derived(sessionScope.normalItems);

  // Drop standalone/prompt nodes whose source item renders nothing under
  // the effective filters — the same `itemVisible` rule session-scope
  // applies to its flat display items. Turn-internal filtering happens
  // per event inside `flattenTranscriptRows`.
  const isEffectivelyVisible = (type: BlockType): boolean =>
    inSessionSearch.isBlockEffectivelyVisible(type);
  // A filtered code fence still renders its expandable placeholder.
  const isTranscriptBlockVisible = (type: BlockType): boolean =>
    type === "code" || isEffectivelyVisible(type);
  const isNodeVisible = (node: TranscriptNode): boolean =>
    node.kind === "assistant-turn" ||
    node.item.kind === "tool-group" ||
    hasVisibleSegments(node.item.message, isTranscriptBlockVisible);

  let renderableNodes = $derived(sessionScope.items.filter(isNodeVisible));

  const isTurnExpanded = (key: string): boolean =>
    turnCollapse.isTurnExpanded(key, !ui.autoCollapseAssistantTurns);
  const isEventVisible = (event: TurnEvent): boolean =>
    isTurnEventVisible(event, isEffectivelyVisible);

  /** An eventless assistant turn is the pre-first-token streaming
   *  window: its header may render when any member's role is visible —
   *  the same rule `hasVisibleSegments` applies to an empty message. */
  const isEmptyTurnVisible = (turn: AssistantTurnItem): boolean =>
    turn.messages.some((message) =>
      hasVisibleSegments(message, isTranscriptBlockVisible));

  /** The child event that renders `blockKey` inside `turn`. One source
   *  ordinal can span several events of the same message (thinking,
   *  text, tool segments), so a search match resolves by block kind and
   *  segment index rather than the first ordinal hit. Returns `null`
   *  when no event maps. */
  function blockOwnerEvent(
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

  /** Expand the owning turn — and the child event disclosure when one
   *  keys it — so a scroll target folded inside a turn has a mountable
   *  row before the destination index is computed. `blockKey` picks the
   *  exact event when one ordinal spans several of them. */
  function expandOrdinalTarget(ordinal: number, blockKey?: string): void {
    if (ui.transcriptMode !== "normal") return;
    const owner = findOrdinalOwner(sessionScope.items, ordinal);
    if (!owner) return;
    turnCollapse.setTurnExpanded(owner.turn.key, true);
    const event = blockKey
      ? (blockOwnerEvent(owner.turn, ordinal, blockKey) ?? owner.event)
      : owner.event;
    // Rollups and system rows own no `isEventExpanded` disclosure; their
    // member blocks self-disclose through the search-collapsed path.
    if (event && event.kind !== "tool-rollup" && event.kind !== "system") {
      turnCollapse.setEventExpanded(event.key, true);
    }
  }

  /**
   * Virtual rows in display order. Normal mode flattens the assistant-turn
   * tree; focused mode keeps the flat message-level projection. Both row
   * kinds share the TranscriptRow contract so scrolling, selection, read
   * progress, and search geometry treat them uniformly.
   */
  let transcriptRows = $derived.by<TranscriptRow[]>(() => {
    if (ui.transcriptMode === "focused") {
      const rows = displayItemsAsc.map(displayTranscriptRow);
      return ui.sortNewestFirst ? rows.reverse() : rows;
    }
    return flattenTranscriptRows(
      renderableNodes,
      isTurnExpanded,
      isEventVisible,
      ui.sortNewestFirst,
      isEmptyTurnVisible,
    );
  });

  /** Copy/Pin/Fork land on the first rendered row of each source message:
   *  the first turn-event row for its message, or the final-output row
   *  when no child event renders that message (collapsed turns and
   *  events hidden by filters both count as not rendered). */
  let sourceActionKeys = $derived.by(() => {
    const claimed = new Set<number>();
    const events = new Set<string>();
    for (const row of transcriptRows) {
      if (row.kind !== "turn-event") continue;
      if (claimed.has(row.event.message.id)) continue;
      claimed.add(row.event.message.id);
      events.add(row.key);
    }
    const outputs = new Set<string>();
    for (const row of transcriptRows) {
      if (row.kind !== "final-output") continue;
      if (!claimed.has(row.event.message.id)) outputs.add(row.key);
    }
    return { events, outputs };
  });

  /** Progress ordinals are the only ordinals whose content a row actually
   *  renders — a collapsed turn header contributes none. Sorted ascending
   *  regardless of display direction for boundary math. */
  let displayedOrdinals = $derived.by(() =>
    transcriptRows
      .flatMap((row) => row.progressOrdinals)
      .sort((a, b) => a - b),
  );

  let displayedOrdinalsSignature = $derived(
    displayedOrdinals.join(","),
  );

  function rowAt(index: number): TranscriptRow | undefined {
    return transcriptRows[index];
  }

  /** Whether the row renders content for `ordinal` — the turn-header's
   *  empty tuple widens to a readonly list here. */
  function rowRendersOrdinal(row: TranscriptRow, ordinal: number): boolean {
    const rendered: readonly number[] = row.progressOrdinals;
    return rendered.includes(ordinal);
  }

  /** First row rendering the ordinal wins; rows only carrying it for
   *  click/selection lookup (a collapsed turn header) are the fallback so
   *  navigation lands on rendered content when both exist. */
  function findRowIndex(ordinal: number): number {
    let fallback = -1;
    for (let index = 0; index < transcriptRows.length; index++) {
      const row = transcriptRows[index]!;
      if (rowRendersOrdinal(row, ordinal)) return index;
      if (fallback < 0 && row.ordinals.includes(ordinal)) fallback = index;
    }
    return fallback;
  }

  /** Ordinals in the row's internal display order — tool-group and rollup
   *  members render newest-first when that sort is on. */
  function orderedRowOrdinals(row: TranscriptRow): number[] {
    return ui.sortNewestFirst
      ? [...row.progressOrdinals].reverse()
      : row.progressOrdinals;
  }

  /** Exactly one row carries the selection outline: the row rendering the
   *  selected ordinal, with the collapsed header as fallback — the same
   *  resolution `findRowIndex` uses for scrolling. */
  let selectedRowIndex = $derived(
    ui.selectedOrdinal === null ? -1 : findRowIndex(ui.selectedOrdinal),
  );

  function rowTimestamp(row: TranscriptRow): string | null {
    switch (row.kind) {
      case "display":
        return row.item.kind === "message"
          ? row.item.message.timestamp
          : row.item.timestamp;
      case "turn-header":
        return row.turn.timestamp;
      default:
        return row.event.message.timestamp;
    }
  }

  const virtualizer = createVirtualizer(() => {
    const count = transcriptRows.length;
    const el = containerRef ?? null;
    const sid = sessions.activeSessionId ?? "";
    return {
      count,
      getScrollElement: () => el,
      estimateSize: () => 120,
      overscan: 5,
      useAnimationFrameWithResizeObserver: true,
      measureCacheKey: sid,
      getItemKey: (index: number) => {
        const row = rowAt(index);
        if (!row) return `${sid}-${index}`;
        return row.key;
      },
    };
  });

  /** Svelte action: measure element for variable-height virtualizer */
  function measureElement(
    node: HTMLElement,
    virt: Virtualizer<HTMLElement, HTMLElement> | undefined,
  ) {
    virt?.measureElement(node);
    return {
      update(
        nextVirt:
          | Virtualizer<HTMLElement, HTMLElement>
          | undefined,
      ) {
        nextVirt?.measureElement(node);
      },
      destroy() {
        // Cleanup handled by virtualizer
      },
    };
  }

  function publishVisibleTimestamp() {
    const v = virtualizer.instance;
    if (!v) return;
    const items = v.getVirtualItems();
    // Skip overscanned items above the viewport.
    const scrollTop = v.scrollOffset ?? 0;
    for (const vi of items) {
      if (vi.end <= scrollTop) continue;
      const row = rowAt(vi.index);
      if (!row) continue;
      const ts = rowTimestamp(row);
      if (ts) {
        sessionActivity.firstVisibleTimestamp = ts;
        return;
      }
    }
    sessionActivity.firstVisibleTimestamp = null;
  }

  function recordVisibleProgress() {
    const v = virtualizer.instance;
    const sessionId = messages.sessionId;
    const currentToken = messages.activeSessionToken;
    const marker = sessionId
      ? readProgress.get(sessionId)
      : null;
    if (
      !v ||
      !sessionId ||
      !currentToken ||
      !marker ||
      marker.token === currentToken
    ) {
      return;
    }

    if (baseMessages.length === 0) {
      readProgress.markRead(
        sessionId,
        currentToken,
        latestRawLoadedOrdinal,
      );
      return;
    }

    const latestDisplayedOrdinal = displayedOrdinals.at(-1);
    if (latestDisplayedOrdinal === undefined) {
      readProgress.markRead(
        sessionId,
        currentToken,
        latestLoadedOrdinal,
      );
      return;
    }

    const top = v.scrollOffset ?? containerRef?.scrollTop ?? 0;
    const height = containerRef?.clientHeight || v.scrollRect?.height || 0;
    const bottom = top + height;
    let maxVisibleOrdinal: number | null = null;
    const visibleOrdinals = new Set<number>();

    for (const row of v.getVirtualItems()) {
      if (row.end <= top || row.start >= bottom) continue;
      const transcriptRow = rowAt(row.index);
      if (!transcriptRow) continue;

      // Multi-ordinal rows (tool groups and turn rollups) expose
      // per-member ordinal markers so only the members actually inside
      // the viewport count. Every other row counts its progress ordinals
      // outright — a collapsed turn header's empty list adds nothing.
      const ordinals = transcriptRow.progressOrdinals.length > 1
        ? (visibleMarkedOrdinals(row.index) ?? transcriptRow.progressOrdinals)
        : transcriptRow.progressOrdinals;
      for (const ordinal of ordinals) {
        visibleOrdinals.add(ordinal);
        maxVisibleOrdinal = maxVisibleOrdinal === null
          ? ordinal
          : Math.max(maxVisibleOrdinal, ordinal);
      }
    }

    if (maxVisibleOrdinal === null || latestLoadedOrdinal === null) return;

    const rawUnreadBoundary = unreadBoundaryOrdinal(
      latestLoadedOrdinal,
    );
    const unreadBoundary = displayedOrdinals.find((ordinal) =>
      ordinal >= rawUnreadBoundary
    ) ?? latestDisplayedOrdinal;
    const traversalKey =
      `${sessionId}|${currentToken}|${unreadBoundary}|${latestDisplayedOrdinal}`;
    if (unreadTraversalKey !== traversalKey) {
      unreadTraversalKey = traversalKey;
      unreadBoundarySeen = false;
      unreadLatestSeen = false;
    }
    if (visibleOrdinals.has(unreadBoundary)) {
      unreadBoundarySeen = true;
    }
    if (visibleOrdinals.has(latestDisplayedOrdinal)) {
      unreadLatestSeen = true;
    }

    if (ui.sortNewestFirst) {
      if (unreadBoundarySeen && unreadLatestSeen) {
        readProgress.markRead(
          sessionId,
          currentToken,
          latestLoadedOrdinal,
        );
      }
      return;
    }

    if (
      unreadBoundarySeen &&
      maxVisibleOrdinal >= latestDisplayedOrdinal
    ) {
      readProgress.markRead(
        sessionId,
        currentToken,
        latestLoadedOrdinal,
      );
      return;
    }
  }

  /** Member ordinals whose `data-message-ordinal` marker is inside the
   *  viewport for one virtual row. Returns `null` when the row exposes no
   *  markers at all, so callers can fall back to the row's progress
   *  ordinals — distinct from an empty list, which means the row has
   *  markers but none are on screen. */
  function visibleMarkedOrdinals(
    rowIndex: number,
  ): number[] | null {
    if (!containerRef) return null;
    const row = containerRef.querySelector<HTMLElement>(
      `.virtual-row[data-index="${rowIndex}"]`,
    );
    if (!row) return null;
    const markers = row.querySelectorAll<HTMLElement>("[data-message-ordinal]");
    if (markers.length === 0) return null;
    const rootRect = containerRef.getBoundingClientRect();
    const ordinals: number[] = [];
    for (const node of markers) {
      const ordinal = Number(node.dataset.messageOrdinal);
      if (!Number.isInteger(ordinal) || ordinal < 0) continue;
      const rect = node.getBoundingClientRect();
      if (rect.bottom <= rootRect.top || rect.top >= rootRect.bottom) continue;
      ordinals.push(ordinal);
    }
    return ordinals;
  }

  // Recompute visible timestamp when minimap opens or
  // message content changes (e.g. SSE reload).
  $effect(() => {
    if (ui.vitalsOpen) {
      // Track message array so the effect re-runs after
      // content changes while the minimap is open.
      void messages.messages.length;
      publishVisibleTimestamp();
    }
  });

  let latestLoadedOrdinal = $derived(
    baseMessages[baseMessages.length - 1]?.ordinal ?? null,
  );

  let latestRawLoadedOrdinal = $derived(
    messages.messages[messages.messages.length - 1]?.ordinal ?? null,
  );

  function unreadBoundaryOrdinal(
    latestOrdinal: number,
  ): number {
    const explicit = messages.activeSessionUnreadOrdinal;
    const earliestOrdinal = baseMessages[0]?.ordinal ?? latestOrdinal;
    const boundary = explicit ?? earliestOrdinal;
    return baseMessages.find((message) =>
      message.ordinal >= boundary
    )?.ordinal ?? latestOrdinal;
  }

  $effect(() => {
    const sessionId = messages.sessionId;
    const currentToken = messages.activeSessionToken;
    const loading = messages.loading;
    const latestOrdinal =
      latestLoadedOrdinal ?? latestRawLoadedOrdinal;
    if (!sessionId || !currentToken || loading) return;
    readProgress.baseline(sessionId, currentToken, latestOrdinal);
  });

  $effect(() => {
    const sessionId = messages.sessionId;
    const currentToken = messages.activeSessionToken;
    const loading = messages.loading;
    const count = messages.messageCount;
    const latest = latestDisplaySignature();
    const displayed = displayedOrdinalsSignature;
    const unreadOrdinal = messages.activeSessionUnreadOrdinal;
    if (!sessionId || !currentToken || loading || !containerRef) return;
    const signature =
      `${sessionId}|${currentToken}|${count}|${latest}|${unreadOrdinal}|${displayed}`;
    if (
      visibleProgressSignature === null ||
      !visibleProgressSignature.startsWith(`${sessionId}|`)
    ) {
      visibleProgressSignature = signature;
      scheduleVisibleProgress(sessionId, currentToken);
      return;
    }
    if (visibleProgressSignature === signature) return;
    visibleProgressSignature = signature;
    scheduleVisibleProgress(sessionId, currentToken);
  });

  function scheduleVisibleProgress(
    sessionId: string,
    currentToken: string,
  ) {
    if (visibleProgressRaf !== null) {
      cancelAnimationFrame(visibleProgressRaf);
    }
    visibleProgressRaf = requestAnimationFrame(() => {
      visibleProgressRaf = null;
      if (
        messages.sessionId !== sessionId ||
        messages.loading ||
        messages.activeSessionToken !== currentToken
      ) {
        return;
      }
      recordVisibleProgress();
    });
  }

  function handleScroll() {
    if (!containerRef) return;
    if (scrollRaf !== null) return;
    scrollRaf = requestAnimationFrame(() => {
      scrollRaf = null;
      if (!containerRef) return;
      const items =
        virtualizer.instance?.getVirtualItems() ?? [];
      if (items.length > 0 && messages.hasOlder) {
        const firstVisible = items[0]!.index;
        const lastVisible =
          items[items.length - 1]!.index;
        const threshold = 30;
        if (
          (ui.sortNewestFirst &&
            lastVisible >=
              transcriptRows.length - threshold) ||
          (!ui.sortNewestFirst &&
            firstVisible <= threshold)
        ) {
          messages.loadOlder();
        }
      }

      if (ui.vitalsOpen) {
        publishVisibleTimestamp();
      }

      recordVisibleProgress();

    });
  }

  function handleManualScrollIntent() {
    lastScrollRequest++;
    if (ui.followLatest) {
      cancelFollowLatestWork();
      ui.setFollowLatest(false);
    }
  }

  function manualScrollIntent(node: HTMLElement) {
    const handleKeydown = (event: KeyboardEvent) => {
      if (
        [
          "ArrowDown",
          "ArrowUp",
          "End",
          "Home",
          "PageDown",
          "PageUp",
          " ",
        ].includes(event.key)
      ) {
        handleManualScrollIntent();
      }
    };
    node.addEventListener("wheel", handleManualScrollIntent, {
      passive: true,
    });
    node.addEventListener("pointerdown", handleManualScrollIntent);
    node.addEventListener("touchmove", handleManualScrollIntent, {
      passive: true,
    });
    node.addEventListener("keydown", handleKeydown);
    return {
      destroy() {
        node.removeEventListener(
          "wheel",
          handleManualScrollIntent,
        );
        node.removeEventListener(
          "pointerdown",
          handleManualScrollIntent,
        );
        node.removeEventListener(
          "touchmove",
          handleManualScrollIntent,
        );
        node.removeEventListener("keydown", handleKeydown);
      },
    };
  }

  onDestroy(() => {
    destroyed = true;
    lastScrollRequest++;
    if (visibleProgressRaf !== null) {
      cancelAnimationFrame(visibleProgressRaf);
      visibleProgressRaf = null;
    }
    if (scrollRaf !== null) {
      cancelAnimationFrame(scrollRaf);
      scrollRaf = null;
    }
    if (followingScrollRaf !== null) {
      cancelAnimationFrame(followingScrollRaf);
      followingScrollRaf = null;
    }
    if (followSettleTimer !== null) {
      clearTimeout(followSettleTimer);
      followSettleTimer = null;
    }
  });

  function cancelFollowLatestWork() {
    if (
      activeFollowScrollRequest !== null &&
      activeFollowScrollRequest === lastScrollRequest
    ) {
      lastScrollRequest += 1;
    }
    activeFollowScrollRequest = null;
    if (followingScrollRaf !== null) {
      cancelAnimationFrame(followingScrollRaf);
      followingScrollRaf = null;
    }
    if (followSettleTimer !== null) {
      clearTimeout(followSettleTimer);
      followSettleTimer = null;
    }
  }

  function scrollToDisplayIndex(
    index: number,
    waitFrames = 0,
    scrollRetries = 0,
    reqId = lastScrollRequest,
    align: ScrollAlign = "start",
  ): Promise<boolean> {
    return settleVirtualScroll({
      index, align, waitFrames, scrollRetries,
      getVirtualizer: () => virtualizer.instance,
      getCount: () => transcriptRows.length,
      isCurrent: () => !destroyed && reqId === lastScrollRequest,
      nextFrame: raf,
    });
  }

  function raf(): Promise<void> {
    return new Promise((r) => requestAnimationFrame(() => r()));
  }

  async function scrollToOrdinalInternal(ordinal: number) {
    const reqId = ++lastScrollRequest;
    activeFollowScrollRequest = null;

    // Expanding the owning turn/event re-derives transcriptRows, so it
    // must happen before the destination index is looked up.
    expandOrdinalTarget(ordinal);
    const rowIndex = findRowIndex(ordinal);
    if (rowIndex >= 0) {
      scrollToDisplayIndex(rowIndex, 0, 0, reqId);
      return;
    }

    await messages.ensureOrdinalLoaded(ordinal);
    if (reqId !== lastScrollRequest) return;

    // Let Svelte re-derive transcriptRows and the
    // virtualizer update its count after loading.
    // Two frames: one for Svelte reactivity, one for
    // virtualizer resize observation.
    await raf();
    await raf();
    if (reqId !== lastScrollRequest) return;

    expandOrdinalTarget(ordinal);
    const loadedRowIndex = findRowIndex(ordinal);
    if (loadedRowIndex < 0) return;
    scrollToDisplayIndex(loadedRowIndex, 0, 0, reqId);
  }

  export function scrollToOrdinal(ordinal: number) {
    void scrollToOrdinalInternal(ordinal);
  }

  function scrollToLatestInternal() {
    const reqId = ++lastScrollRequest;
    activeFollowScrollRequest = reqId;
    const idx = getLatestDisplayIndex(
      transcriptRows.length,
      ui.sortNewestFirst,
    );
    if (idx < 0) return;
    scrollToDisplayIndex(
      idx,
      0,
      0,
      reqId,
      ui.sortNewestFirst ? "start" : "end",
    );
    startFollowLatestSettle(reqId);
  }

  function forceLatestEdge() {
    if (!containerRef) return;
    containerRef.scrollTop = ui.sortNewestFirst
      ? 0
      : containerRef.scrollHeight;
  }

  function startFollowLatestSettle(reqId: number) {
    if (followSettleTimer !== null) {
      clearTimeout(followSettleTimer);
      followSettleTimer = null;
    }

    const tick = () => {
      followSettleTimer = null;
      if (
        reqId !== lastScrollRequest ||
        !ui.followLatest ||
        !containerRef
      ) {
        return;
      }

      forceLatestEdge();
      followSettleTimer = setTimeout(tick, 100);
    };

    tick();
  }

  function queueFollowLatestScroll() {
    if (!ui.followLatest) return;
    if (followingScrollRaf !== null) {
      cancelAnimationFrame(followingScrollRaf);
    }
    followingScrollRaf = requestAnimationFrame(() => {
      followingScrollRaf = null;
      if (!ui.followLatest) return;
      scrollToLatestInternal();
    });
  }

  function latestDisplaySignature(): string {
    const item = displayItemsAsc[displayItemsAsc.length - 1];
    if (!item) return "";
    if (item.kind === "tool-group") {
      return item.messages
        .map((m) => `${m.ordinal}:${m.content_length}:${m.timestamp}`)
        .join("|");
    }
    const m = item.message;
    return `${m.ordinal}:${m.content_length}:${m.timestamp}`;
  }

  $effect(() => {
    const follow = ui.followLatest;
    if (!follow) {
      cancelFollowLatestWork();
    }
  });

  $effect(() => {
    const follow = ui.followLatest;
    const request = ui.followLatestRequest;
    const count = transcriptRows.length;
    const latest = latestDisplaySignature();
    const newestFirst = ui.sortNewestFirst;
    const sessionId = messages.sessionId;
    if (!follow || count === 0 || !sessionId) return;
    void request;
    void latest;
    void newestFirst;
    queueFollowLatestScroll();
  });

  export function scrollToLatest() {
    scrollToLatestInternal();
  }

  export function getDisplayItems(): DisplayItem[] {
    return displayItemsAsc;
  }

  export function getNormalDisplayItems(): DisplayItem[] {
    return normalDisplayItemsAsc;
  }

  /** Source-message ordinals a j/k navigation pass can stop at: each
   *  ordinal a rendered row contributes, once, in transcript order.
   *  Turn headers add no step — their member ordinals travel with the
   *  event and output rows that render them. */
  export function getNavigableOrdinals(): number[] {
    const seen = new Set<number>();
    const ordinals: number[] = [];
    for (const row of transcriptRows) {
      for (const ordinal of orderedRowOrdinals(row)) {
        if (seen.has(ordinal)) continue;
        seen.add(ordinal);
        ordinals.push(ordinal);
      }
    }
    return ordinals;
  }

  // Turn collapse state is session-scoped; switching sessions drops every
  // override, bulk baseline, and prompt disclosure so it cannot leak.
  $effect(() => {
    turnCollapse.activateSession(messages.sessionId);
  });

  // The leading partial turn re-anchors to an earlier message id when an
  // older page is prepended, which changes its key. Carrying the turn's
  // manual expansion override to the new key keeps a turn the user opened
  // open across loadOlder/re-anchoring. `sessionScope.leadingTurn` is the
  // unfiltered normal-mode anchor, so this runs in either transcript mode.
  let leadingTurn: LeadingTurnAnchor | null = null;
  $effect(() => {
    const next = sessionScope.leadingTurn;
    const previous = leadingTurn;
    leadingTurn = next;
    if (
      previous !== null &&
      next !== null &&
      previous.key !== next.key &&
      // Same-turn check: a re-anchor keeps the old first message inside
      // the new turn's members; a genuinely different turn does not.
      next.memberIds.includes(previous.firstMessageId)
    ) {
      turnCollapse.migrateTurnKey(previous.key, next.key);
    }
  });

  let searchRevealKey = $derived.by(() => {
    const match = inSessionSearch.resolvedCurrent;
    return match ? `${match.ordinal}:${match.blockKey}:${match.occurrence}` : "";
  });

  async function revealSearchMatch(match: Match, sessionId: string, reqId: number): Promise<boolean> {
    return revealMatch({
      ordinal: match.ordinal,
      blockKey: match.blockKey,
      getContainer: () => containerRef,
      isCurrent: () => !destroyed && reqId === lastScrollRequest &&
        messages.sessionId === sessionId && sessions.activeSessionId === sessionId &&
        inSessionSearch.isActive,
      ensureLoaded: (ordinal) => messages.ensureOrdinalLoaded(ordinal),
      mountMessage: () => {
        // The match can live inside a folded turn/event: expand before
        // the destination row index is computed.
        expandOrdinalTarget(match.ordinal, match.blockKey);
        const index = findRowIndex(match.ordinal);
        if (index < 0) return Promise.resolve(false);
        return scrollToDisplayIndex(index, 0, 0, reqId);
      },
      scrollToOffset: (offset) => virtualizer.instance?.scrollToOffset(
        Math.round(offset), { align: "start" },
      ),
      afterUpdate: tick,
      nextFrame: raf,
    });
  }

  $effect(() => {
    const request = inSessionSearch.navigationRevision;
    const key = searchRevealKey;
    const sessionId = messages.sessionId;
    const count = transcriptRows.length;
    const newestFirst = ui.sortNewestFirst;
    if (!inSessionSearch.isActive || !sessionId || !containerRef) return;
    if (!key) {
      // Active query without a renderable occurrence: drop any pending reveal
      // so a filtered-out block cannot be mounted or scrolled into view.
      lastScrollRequest++;
      return;
    }
    void request;
    void count;
    void newestFirst;
    return untrack(() => {
      const match = inSessionSearch.resolvedCurrent;
      if (!match) return;
      const reqId = ++lastScrollRequest;
      activeFollowScrollRequest = null;
      ui.selectOrdinal(match.ordinal);
      ui.setFollowLatest(false);
      void revealSearchMatch(match, sessionId, reqId).catch((error: unknown) => {
        if (reqId === lastScrollRequest) console.warn("Could not reveal search occurrence", error);
      });
      return () => {
        if (reqId === lastScrollRequest) lastScrollRequest++;
      };
    });
  });

  let effectiveLayout = $derived(
    resolveMessageLayout(ui.messageLayout, inSessionSearch.isActive),
  );

  let readProgressDivider = $derived.by(() => {
    const sessionId = messages.sessionId;
    const currentToken = messages.activeSessionToken;
    const marker = sessionId
      ? readProgress.get(sessionId)
      : null;
    const latestOrdinal = latestLoadedOrdinal;
    if (
      !sessionId ||
      !currentToken ||
      !marker ||
      marker.token === currentToken ||
      latestOrdinal === null
    ) {
      return null;
    }

    const unreadBoundary = unreadBoundaryOrdinal(latestOrdinal);

    // transcriptRows are already in display order; only ordinals whose
    // content renders (progressOrdinals) can host the boundary, so a
    // collapsed turn header never hides the divider inside folded work.
    const rows = transcriptRows;

    if (ui.sortNewestFirst) {
      if (messages.activeSessionUnreadOrdinal === null) {
        return null;
      }
      const dividerBoundary = marker.ordinal !== null &&
          unreadBoundary === marker.ordinal + 1
        ? marker.ordinal
        : unreadBoundary;
      for (const row of rows) {
        for (const ordinal of orderedRowOrdinals(row)) {
          if (ordinal <= dividerBoundary) {
            return {
              ordinal,
              label: m.read_progress_earlier_messages(),
            };
          }
        }
      }
      return null;
    }

    for (const row of rows) {
      for (const ordinal of orderedRowOrdinals(row)) {
        if (ordinal >= unreadBoundary) {
          return {
            ordinal,
            label: m.read_progress_new_messages(),
          };
        }
      }
    }
    return null;
  });
</script>

{#if !sessions.activeSessionId}
  <EmptyState title={m.message_list_empty()}>
    {#snippet icon()}
      <MessageSquareIcon size="36" strokeWidth="1.5" aria-hidden="true" />
    {/snippet}
  </EmptyState>
{:else if messages.loading && messages.messages.length === 0}
  <EmptyState title={m.message_list_loading()} />
{:else if sessions.activeSessionNotFound && messages.messages.length === 0}
  <EmptyState
    title={m.message_list_session_not_found()}
    description={m.message_list_session_not_found_hint()}
  >
    {#snippet icon()}
      <CircleQuestionMarkIcon size="36" strokeWidth="1.5" aria-hidden="true" />
    {/snippet}
    <Button size="sm" onclick={() => void sessions.retryActiveSession()}>
      {m.message_list_session_not_found_retry()}
    </Button>
  </EmptyState>
{:else}
  <SessionFindView
    rows={transcriptRows}
    totalSize={virtualizer.instance?.getTotalSize() ?? 0}
    rowOffset={(index) => virtualizer.instance?.getOffsetForIndex(index, "start")?.[0] ?? index * 120}
  >
  <div
    class="message-list-scroll layout-{effectiveLayout}"
    bind:this={containerRef}
    data-session-id={sessions.activeSessionId}
    data-messages-session-id={messages.sessionId}
    data-loaded={!messages.loading}
    onscroll={handleScroll}
    use:manualScrollIntent
  >
    <div
      style="height: {virtualizer.instance?.getTotalSize() ?? 0}px; width: 100%; position: relative;"
    >
      {#each virtualizer.instance?.getVirtualItems() ?? [] as virtualRow (virtualRow.key)}
        {@const row = rowAt(virtualRow.index)}
        {#if row}
          {@const dividerHere = readProgressDivider !== null &&
            rowRendersOrdinal(row, readProgressDivider.ordinal)}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div
            class="virtual-row"
            class:selected={virtualRow.index === selectedRowIndex}
            data-index={virtualRow.index}
            style="position: absolute; top: 0; left: 0; width: 100%; transform: translateY({virtualRow.start}px);"
            use:measureElement={virtualizer.instance}
            onclick={() => {
              const sel = window.getSelection();
              if (sel && sel.toString().length > 0) return;
              ui.selectOrdinal(row.ordinals[0]!);
            }}
          >
            {#if row.kind === "display"}
              {@const item = row.item}
              {#if item.kind === "tool-group"}
                <ToolCallGroup
                  messages={item.messages}
                  timestamp={item.timestamp}
                  searchable={true}
                  sortNewestFirst={ui.sortNewestFirst}
                  divider={dividerHere ? readProgressDivider : undefined}
                />
              {:else}
                {#if dividerHere}
                  <div class="read-progress-divider" role="separator" aria-label={m.read_progress_boundary()}>
                    {readProgressDivider!.label}
                  </div>
                {/if}
                {#if item.message.is_compact_boundary}
                  <CompactBoundaryDivider message={item.message} />
                {:else if isSystemBoundaryMessage(item.message)}
                  <SystemBoundaryCard
                    subtype={item.message.source_subtype}
                    content={item.message.content}
                    timestamp={item.message.timestamp}
                  />
                {:else}
                  <MessageContent
                    message={item.message}
                    searchOrdinal={item.message.ordinal}
                  />
                {/if}
              {/if}
            {:else if row.kind === "turn-header"}
              <AssistantTurnHeader turn={row.turn} />
            {:else if row.kind === "turn-event"}
              {#if dividerHere && row.event.kind !== "tool-rollup"}
                <div class="read-progress-divider" role="separator" aria-label={m.read_progress_boundary()}>
                  {readProgressDivider!.label}
                </div>
              {/if}
              <AssistantTurnEventRow
                event={row.event}
                ownsSourceActions={sourceActionKeys.events.has(row.key)}
                divider={dividerHere && row.event.kind === "tool-rollup"
                  ? readProgressDivider
                  : undefined}
              />
            {:else}
              {#if dividerHere}
                <div class="read-progress-divider" role="separator" aria-label={m.read_progress_boundary()}>
                  {readProgressDivider!.label}
                </div>
              {/if}
              <AssistantTurnOutput
                event={row.event}
                ownsSourceActions={sourceActionKeys.outputs.has(row.key)}
              />
            {/if}
          </div>
        {/if}
      {/each}
    </div>
  </div>
  </SessionFindView>
{/if}

<style>
  .message-list-scroll {
    flex: 1;
    overflow-y: auto;
    overflow-x: hidden;
    padding: 8px 0;
    overflow-anchor: none;
  }

  .virtual-row {
    padding: 5px 12px;
    overflow-anchor: none;
  }

  .virtual-row.selected > :global(*) {
    outline: 2px solid var(--accent-blue);
    outline-offset: -2px;
    border-radius: var(--radius-md, 6px);
  }

  .read-progress-divider {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 8px;
    color: var(--accent-blue);
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .read-progress-divider::before,
  .read-progress-divider::after {
    content: "";
    height: 1px;
    flex: 1;
    background: color-mix(
      in srgb, var(--accent-blue) 35%, transparent
    );
  }

  /* ── Compact layout ── */
  .layout-compact {
    padding: 4px 0;
  }

  .layout-compact .virtual-row {
    padding: 2px 12px;
  }

  .layout-compact :global(.message) {
    padding: 6px 12px;
    border-left-width: 2px;
    border-radius: 0;
  }

  .layout-compact :global(.message-header) {
    margin-bottom: 4px;
    gap: 6px;
  }

  .layout-compact :global(.role-icon) {
    width: 16px;
    height: 16px;
    font-size: 9px;
  }

  .layout-compact :global(.role-label) {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    font-weight: 700;
  }

  .layout-compact :global(.timestamp),
  .layout-compact :global(.group-timestamp) {
    font-size: 10px;
  }

  .layout-compact :global(.text-content) {
    font-size: 13px;
    line-height: 1.55;
  }

  .layout-compact :global(.message-body) {
    gap: 4px;
  }

  /* ── Stream layout ── */
  .layout-stream {
    padding: 0;
  }

  .layout-stream .virtual-row {
    padding: 0;
  }

  .layout-stream :global(.message) {
    border-left: none;
    border-radius: 0;
    padding: 16px 24px;
  }

  .layout-stream :global(.message.is-user) {
    background: color-mix(
      in srgb,
      var(--accent-blue) 5%,
      transparent
    ) !important;
  }

  .layout-stream :global(.message:not(.is-user)) {
    background: transparent !important;
  }

  .layout-stream :global(.message-header) {
    display: none;
  }

  .layout-stream :global(.text-content) {
    font-size: 14px;
    line-height: 1.75;
  }

  /* ── Skim layout ── */
  .layout-skim {
    padding: 0;
  }

  .layout-skim .virtual-row {
    padding: 0;
  }

  .layout-skim :global(.message-header) {
    display: none;
  }

  .layout-skim :global(.tool-block) {
    border-left: none;
    border-radius: 0;
  }

  .layout-skim :global(.tool-chevron) {
    display: none;
  }

  /* Keep the one-line summary, but make the header non-interactive so a
     click cannot silently toggle hidden collapse state (which would
     surprise the user on switching back to a full layout). Row selection
     still works via the virtual-row wrapper behind the header. */
  .layout-skim :global(.tool-header) {
    padding: 1px 12px;
    pointer-events: none;
  }

  .layout-skim :global(.tool-meta),
  .layout-skim :global(.tool-content),
  .layout-skim :global(.diff-view),
  .layout-skim :global(.output-header),
  .layout-skim :global(.history-header),
  .layout-skim :global(.result-history),
  .layout-skim :global(.show-more-btn) {
    display: none;
  }

  .layout-skim :global(.tool-group-header),
  .layout-skim :global(.pg-header) {
    display: none;
  }

  .layout-skim :global(.tool-group),
  .layout-skim :global(.parallel-group) {
    border: none;
    margin: 0;
    padding: 0;
    background: transparent;
  }

  .layout-skim :global(.subagent-inline) {
    display: none;
  }
</style>
