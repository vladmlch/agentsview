<script lang="ts">
  import { searchBlock } from "../../search/session-block.svelte.js";
  import { searchCollapsed } from "../../search/component-state.js";
  import { inSessionSearch } from "../../stores/inSessionSearch.svelte.js";
  import { turnCollapse } from "../../stores/turn-collapse.svelte.js";
  import SearchMatchCount from "./SearchMatchCount.svelte";
  import { ChevronRightIcon } from "../../icons.js";
  import { m } from "../../i18n/index.js";

  interface Props {
    content: string;
    searchKey?: string;
    /** Turn-event disclosure key; when present, expansion state resolves
     *  through `turnCollapse` instead of only local state. */
    collapseKey?: string;
    /** Default expansion used when no override or bulk baseline applies. */
    defaultExpanded?: boolean;
  }

  let { content, searchKey, collapseKey, defaultExpanded = false }: Props = $props();
  let userCollapsed = $state(true);
  let overrideSeq = $state(-1);
  let baseCollapsed = $derived(
    collapseKey === undefined
      ? userCollapsed
      : !turnCollapse.isEventExpanded(collapseKey, defaultExpanded),
  );
  let collapsed = $derived(searchCollapsed(
    baseCollapsed, inSessionSearch.isCurrentBlock(searchKey),
    inSessionSearch.navigationRevision, overrideSeq,
  ));

  /** First content line, truncated at 80 Unicode code points — an emoji
   *  counts as one point and surrogate pairs are never split. */
  let previewLine = $derived.by(() => {
    const first = content.split("\n")[0] ?? "";
    const points = Array.from(first);
    return points.length > 80 ? `${points.slice(0, 80).join("")}…` : first;
  });
</script>

<div class="thinking-block">
  <button
    class="thinking-header"
    aria-expanded={!collapsed}
    onclick={() => {
      userCollapsed = !collapsed;
      overrideSeq = inSessionSearch.navigationRevision;
      if (collapseKey !== undefined) {
        turnCollapse.setEventExpanded(collapseKey, !userCollapsed);
      }
    }}
  >
    <span class="thinking-chevron" class:open={!collapsed}>
      <ChevronRightIcon size="10" strokeWidth="2.4" aria-hidden="true" />
    </span>
    <span class="thinking-label">{m.thinking_block_label()}</span>
    <SearchMatchCount {searchKey} />
    {#if collapsed && previewLine}
      <span class="thinking-preview">{previewLine}</span>
    {/if}
  </button>
  {#if !collapsed}
    <div
      class="thinking-content"
      {@attach searchBlock(searchKey)}
    >{content}</div>
  {/if}
</div>

<style>
  .thinking-block {
    border-left: 2px solid var(--accent-purple);
    background: var(--thinking-bg);
    border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
    margin: 0;
  }

  .thinking-header {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 6px 10px;
    width: 100%;
    text-align: left;
    font-size: 12px;
    font-weight: 600;
    color: var(--accent-purple);
    letter-spacing: 0.01em;
    border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
    transition: background 0.1s;
    min-width: 0;
  }

  .thinking-header:hover {
    background: var(--bg-surface-hover);
  }

  .thinking-chevron {
    display: inline-flex;
    align-items: center;
    transition: transform 0.15s;
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .thinking-chevron.open {
    transform: rotate(90deg);
  }

  .thinking-preview {
    font-weight: 400;
    font-style: italic;
    font-size: 12px;
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }

  .thinking-content {
    padding: 8px 14px 12px;
    font-size: 13px;
    font-style: italic;
    color: var(--text-secondary);
    white-space: pre-wrap;
    word-wrap: break-word;
    line-height: 1.65;
    border-top: 1px solid var(--border-muted);
  }
</style>
