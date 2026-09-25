<script lang="ts">
  import { searchBlock } from "../../search/session-block.svelte.js";
  import { searchCollapsed } from "../../search/component-state.js";
  import { inSessionSearch } from "../../stores/inSessionSearch.svelte.js";
  import { turnCollapse } from "../../stores/turn-collapse.svelte.js";
  import SearchMatchCount from "./SearchMatchCount.svelte";
  import { m } from "../../i18n/index.js";
  import { loadAssetImages, renderMarkdown } from "../../utils/markdown.js";
  import { highlightCodeFences } from "../../utils/highlight-fences.js";
  import { ChevronRightIcon } from "../../icons.js";
  import { ui } from "../../stores/ui.svelte.js";

  interface Props {
    content: string;
    name?: string;
    searchKey?: string;
    /** Turn-event disclosure key; when present, expansion state resolves
     *  through `turnCollapse` instead of only local state. */
    collapseKey?: string;
    /** Default expansion used when no override or bulk baseline applies. */
    defaultExpanded?: boolean;
  }

  let { content, name, searchKey, collapseKey, defaultExpanded = false }: Props = $props();
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

<div class="skill-block">
  <button
    class="skill-header"
    aria-expanded={!collapsed}
    onclick={() => {
      const sel = window.getSelection();
      if (sel && sel.toString().length > 0) return;
      userCollapsed = !collapsed;
      overrideSeq = inSessionSearch.navigationRevision;
      if (collapseKey !== undefined) {
        turnCollapse.setEventExpanded(collapseKey, !userCollapsed);
      }
    }}
  >
    <span class="skill-chevron" class:open={!collapsed}>
      <ChevronRightIcon size="10" strokeWidth="2.4" aria-hidden="true" />
    </span>
    <span class="skill-label">
      {m.skill_block_label({ name: name ?? m.shared_unknown() })}
    </span>
    <SearchMatchCount {searchKey} />
    {#if collapsed && previewLine}
      <span class="skill-preview">{previewLine}</span>
    {/if}
  </button>
  {#if !collapsed}
    <div
      class="skill-content markdown"
      use:highlightCodeFences={{ content }}
      {@attach searchBlock(searchKey)}
      use:loadAssetImages={content}
    >
      {@html renderMarkdown(content, {
        renderUnknownXmlBlocksAsPreformatted: ui.renderUnknownXmlBlocksAsPreformatted,
      })}
    </div>
  {/if}
</div>

<style>
  .skill-block {
    border-left: 2px solid var(--accent-teal, #14b8a6);
    background: var(--tool-bg);
    border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
    margin: 0;
  }

  .skill-header {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 6px 10px;
    width: 100%;
    text-align: left;
    font-size: 12px;
    color: var(--text-secondary);
    min-width: 0;
    border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
    transition: background 0.1s;
    user-select: text;
  }

  .skill-header:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .skill-chevron {
    display: inline-flex;
    align-items: center;
    transition: transform 0.15s;
    flex-shrink: 0;
    color: var(--text-muted);
  }

  .skill-chevron.open {
    transform: rotate(90deg);
  }

  .skill-label {
    font-family: var(--font-mono);
    font-weight: 500;
    font-size: 11px;
    color: var(--accent-teal, #14b8a6);
    white-space: nowrap;
    flex-shrink: 0;
  }

  .skill-preview {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }

  .skill-content {
    padding: 8px 14px 12px;
    font-size: 13px;
    color: var(--text-secondary);
    line-height: 1.65;
    border-top: 1px solid var(--border-muted);
    overflow-x: auto;
  }
</style>
