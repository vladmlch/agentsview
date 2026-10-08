<script lang="ts">
  import { untrack } from "svelte";
  import type { DbMessage as Message } from "../../api/generated/index.js";
  import type {
    AssistantTurnItem,
    TurnEvent,
  } from "../../utils/assistant-turns.js";
  import type { TranscriptRow } from "../../utils/transcript-rows.js";
  import { blockOwnerEvent } from "../../utils/transcript-rows.js";
  import {
    collectSearchBlocks,
    type SearchBlock,
  } from "../../search/block-text.js";
  import type { MarkdownRenderOptions } from "../../utils/markdown.js";
  import { blockTypeForKind } from "../../search/session-scope.js";
  import { nearestOverviewMatch, overviewLocations, overviewTicks, overviewY } from "../../search/overview.js";
  import { ui } from "../../stores/ui.svelte.js";
  import { inSessionSearch } from "../../stores/inSessionSearch.svelte.js";
  import { m } from "../../i18n/index.js";

  interface Props {
    /** Virtual transcript rows in display order — the same sequence the
     *  virtualizer positions via `rowOffset`. */
    rows: TranscriptRow[];
    totalSize: number;
    rowOffset: (index: number) => number;
  }
  let { rows, totalSize, rowOffset }: Props = $props();
  let rail: SVGSVGElement | undefined = $state(undefined);
  let width = $state(0);
  let height = $state(0);

  /** Search blocks of `event`'s covered messages that resolve to this
   *  event via the same ownership rule search navigation applies. */
  function blocksOwnedByEvent(
    turn: AssistantTurnItem,
    event: TurnEvent,
    options: MarkdownRenderOptions,
    visible: (message: Message, block: SearchBlock) => boolean,
  ): SearchBlock[] {
    const blocks: SearchBlock[] = [];
    for (const message of turn.messages) {
      if (!event.ordinals.includes(message.ordinal)) continue;
      for (const block of collectSearchBlocks(message, options)) {
        if (
          blockOwnerEvent(turn, block.ordinal, block.key) === event &&
          visible(message, block)
        ) {
          blocks.push(block);
        }
      }
    }
    return blocks;
  }

  /** Search blocks a match can land on for one row. Display rows own
   *  their item's blocks. Turn-event and final-output rows own only the
   *  blocks their event renders: one source message can span a thinking
   *  row and a message row, so claiming whole messages would pin a text
   *  match on the first claiming (e.g. thinking) band. The final output's
   *  preview twin mounts no search blocks — the output row owns them.
   *  A turn header adopts the blocks whose owner event has no rendered
   *  row, so matches inside a collapsed or filtered turn still pin to
   *  its visible header band. */
  function rowBlocks(
    row: TranscriptRow,
    mountedEventKeys: ReadonlySet<string>,
    options: MarkdownRenderOptions,
    visible: (message: Message, block: SearchBlock) => boolean,
  ): SearchBlock[] {
    switch (row.kind) {
      case "display": {
        const itemMessages =
          row.item.kind === "message" ? [row.item.message] : row.item.messages;
        return itemMessages.flatMap((message) =>
          collectSearchBlocks(message, options).filter((block) =>
            visible(message, block),
          ),
        );
      }
      case "turn-event":
        if (row.event.key === row.turn.finalOutput?.key) return [];
        return blocksOwnedByEvent(row.turn, row.event, options, visible);
      case "final-output":
        return blocksOwnedByEvent(row.turn, row.event, options, visible);
      default: {
        const blocks: SearchBlock[] = [];
        for (const message of row.turn.messages) {
          for (const block of collectSearchBlocks(message, options)) {
            const owner = blockOwnerEvent(row.turn, block.ordinal, block.key);
            if (
              (owner === null || !mountedEventKeys.has(owner.key)) &&
              visible(message, block)
            ) {
              blocks.push(block);
            }
          }
        }
        return blocks;
      }
    }
  }

  let locations = $derived.by(() => {
    const ordered = rows;
    const matches = inSessionSearch.matches;
    const scope = inSessionSearch.scope;
    const revealed = inSessionSearch.revealedBlockTypes;
    const total = totalSize;
    const options: MarkdownRenderOptions = {
      renderUnknownXmlBlocksAsPreformatted: ui.renderUnknownXmlBlocksAsPreformatted,
    };
    return untrack(() => {
      /* Events whose blocks already pin to their own rendered row —
       * every turn-event row except the final output's static preview,
       * plus the final-output row itself. */
      const mountedEventKeys = new Set<string>();
      for (const row of ordered) {
        if (row.kind === "turn-event") {
          if (row.event.key !== row.turn.finalOutput?.key) {
            mountedEventKeys.add(row.event.key);
          }
        } else if (row.kind === "final-output") {
          mountedEventKeys.add(row.event.key);
        }
      }
      const visible = (message: Message, block: SearchBlock) =>
        (scope?.allowsBlock(message, block.kind) ?? false) ||
        revealed.has(blockTypeForKind(block.kind, message.role));
      return overviewLocations(ordered.map((row, index) => {
        const offset = rowOffset(index);
        const end = index + 1 < ordered.length ? rowOffset(index + 1) : total;
        return { offset, size: Math.max(1, end - offset),
          blocks: rowBlocks(row, mountedEventKeys, options, visible) };
      }), matches);
    });
  });

  let ticks = $derived(overviewTicks(locations, totalSize, height));
  let currentLocation = $derived(locations.find(({ match }) =>
    match.blockKey === inSessionSearch.resolvedCurrent?.blockKey &&
    match.occurrence === inSessionSearch.resolvedCurrent?.occurrence));

  $effect(() => {
    const node = rail;
    if (!node) return;
    const measure = () => {
      width = node.clientWidth;
      height = node.clientHeight;
    };
    measure();
    const resize = new ResizeObserver(measure);
    resize.observe(node);
    return () => resize.disconnect();
  });

  function selectAt(event: MouseEvent) {
    if (!rail || !totalSize) return;
    const rect = rail.getBoundingClientRect();
    if (!rect.height) return;
    const offset = Math.max(0, Math.min(1, (event.clientY - rect.top) / rect.height)) * totalSize;
    const match = nearestOverviewMatch(locations, offset);
    if (match) inSessionSearch.goTo(match);
  }

  function navigate(event: KeyboardEvent) {
    if (!["ArrowUp", "ArrowDown", "Home", "End", "Enter", " "].includes(event.key)) return;
    event.preventDefault();
    event.stopPropagation();
    if (event.key === "ArrowUp") inSessionSearch.prev();
    else if (event.key === "ArrowDown") inSessionSearch.next();
    else if (event.key === "Home" || event.key === "End") {
      const match = event.key === "Home" ? locations[0]?.match : locations.at(-1)?.match;
      if (match) inSessionSearch.goTo(match);
    } else if (inSessionSearch.resolvedCurrent) inSessionSearch.goTo(inSessionSearch.resolvedCurrent);
  }
</script>

<svg class="find-overview-rail" bind:this={rail}
  role="button" tabindex="0" aria-label={m.session_find_rail_label({ count: inSessionSearch.total })}
  onclick={selectAt} onkeydown={navigate}>
  {#each ticks as mark}
    <rect x="2" y={mark.y} width={Math.max(1, width - 4)} height={mark.height} fill="var(--accent-amber)" />
  {/each}
  {#if currentLocation && height > 0}
    <rect x="0" y={Math.max(0, Math.min(height - 4, overviewY(currentLocation.offset, totalSize, height) - 2))}
      {width} height={Math.min(4, height)} fill="currentColor" />
  {/if}
</svg>

<style>
  .find-overview-rail {
    position: absolute;
    inset-block: 0;
    inset-inline-end: 0;
    width: 12px;
    height: 100%;
    color: var(--accent-blue);
    cursor: pointer;
  }
</style>
