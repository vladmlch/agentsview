<script lang="ts">
  import { untrack } from "svelte";
  import type { DbMessage as Message } from "../../api/generated/index.js";
  import type { TranscriptRow } from "../../utils/transcript-rows.js";
  import { collectSearchBlocks } from "../../search/block-text.js";
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

  /** Messages a match can land on for one row. The first rendered row of
   *  a source message claims it (`taken`), so a message behind several
   *  events emits its blocks exactly once; a turn header adopts the
   *  members no child or final-output row renders, so matches inside a
   *  collapsed or filtered turn still pin to its visible header band. */
  function rowMessages(
    row: TranscriptRow,
    claimed: ReadonlySet<number>,
    taken: Set<number>,
  ): Message[] {
    let mine: Message[];
    switch (row.kind) {
      case "display":
        mine = row.item.kind === "message"
          ? [row.item.message]
          : row.item.messages;
        break;
      case "turn-header":
        mine = row.turn.messages.filter(
          (message) => !claimed.has(message.ordinal),
        );
        break;
      default: {
        // The header's empty-tuple progressOrdinals widens here; every
        // other row kind carries a plain number list.
        const rendered: readonly number[] = row.progressOrdinals;
        mine = row.turn.messages.filter((message) =>
          rendered.includes(message.ordinal),
        );
      }
    }
    const fresh = mine.filter((message) => !taken.has(message.ordinal));
    for (const message of fresh) taken.add(message.ordinal);
    return fresh;
  }

  let locations = $derived.by(() => {
    const ordered = rows;
    const matches = inSessionSearch.matches;
    const scope = inSessionSearch.scope;
    const revealed = inSessionSearch.revealedBlockTypes;
    const total = totalSize;
    const renderUnknownXmlBlocksAsPreformatted = ui.renderUnknownXmlBlocksAsPreformatted;
    return untrack(() => {
      const claimed = new Set<number>();
      for (const row of ordered) {
        for (const ordinal of row.progressOrdinals) claimed.add(ordinal);
      }
      const taken = new Set<number>();
      return overviewLocations(ordered.map((row, index) => {
        const offset = rowOffset(index);
        const end = index + 1 < ordered.length ? rowOffset(index + 1) : total;
        const messages = rowMessages(row, claimed, taken);
        return { offset, size: Math.max(1, end - offset), blocks: messages.flatMap((message) =>
          collectSearchBlocks(message, { renderUnknownXmlBlocksAsPreformatted })
            .filter((block) => (scope?.allowsBlock(message, block.kind) ?? false)
              || revealed.has(blockTypeForKind(block.kind, message.role)))) };
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
