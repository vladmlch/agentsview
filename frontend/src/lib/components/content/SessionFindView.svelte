<script lang="ts">
  import type { Snippet } from "svelte";
  import type { TranscriptRow } from "../../utils/transcript-rows.js";
  import { inSessionSearch } from "../../stores/inSessionSearch.svelte.js";
  import FindResultsList from "./FindResultsList.svelte";
  import FindOverviewRail from "./FindOverviewRail.svelte";
  import SessionFindBar from "./SessionFindBar.svelte";

  interface Props {
    children: Snippet;
    /** Virtual transcript rows in display order — the same sequence the
     *  virtualizer positions. */
    rows: TranscriptRow[];
    totalSize: number;
    rowOffset: (index: number) => number;
  }
  let { children, rows, totalSize, rowOffset }: Props = $props();
</script>

<SessionFindBar />
{#if inSessionSearch.isOpen && inSessionSearch.resultsOpen}
  <FindResultsList />
{/if}
<div class="find-viewport" class:has-find={inSessionSearch.isActive}>
  {@render children()}
  {#if inSessionSearch.isActive}
    <FindOverviewRail {rows} {totalSize} {rowOffset} />
  {/if}
</div>

<style>
  .find-viewport { position: relative; display: flex; flex-direction: column; flex: 1; min-height: 0; }
  .has-find :global(.message-list-scroll) { padding-inline-end: 12px; }
</style>
