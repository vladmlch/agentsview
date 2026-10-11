<script lang="ts">
  import type {
    PipelineStage,
    PipelineStageStatus,
  } from "../../stores/pipeline.svelte.js";

  interface Props {
    /** Per-stage lifecycle from viewState.stageStatus. */
    stageStatus: Record<PipelineStage, PipelineStageStatus>;
    /** Stage the timeline is filtered to, if any. */
    selected?: PipelineStage | null;
    /** Click toggles: selecting the active filter again clears it. */
    onselect?: (stage: PipelineStage | null) => void;
  }

  let { stageStatus, selected = null, onselect = () => {} }: Props = $props();

  // `embed` renders as a satellite under the main chain — it runs off the
  // write path, not in sequence with the other stages.
  const MAIN_STAGES: PipelineStage[] = [
    "watch",
    "resolve",
    "gates",
    "parse",
    "batch",
    "write",
    "finalize",
  ];
  const SATELLITE_STAGES: PipelineStage[] = ["embed"];
</script>

{#snippet stageNode(stage: PipelineStage)}
  <button
    type="button"
    class="stage-node"
    class:is-active={stageStatus[stage] === "active"}
    class:is-done={stageStatus[stage] === "done"}
    class:is-selected={selected === stage}
    data-stage={stage}
    title={`${stage}: ${stageStatus[stage]}${selected === stage ? " (filtering timeline)" : ""}`}
    aria-pressed={selected === stage}
    onclick={() => onselect(selected === stage ? null : stage)}
  >
    <span class="stage-dot" aria-hidden="true"></span>
    <span class="stage-name">{stage}</span>
  </button>
{/snippet}

<div class="pipeline-diagram" role="group" aria-label="Pipeline stages">
  <div class="stage-row">
    {#each MAIN_STAGES as stage, i (stage)}
      {#if i > 0}
        <span class="stage-link" aria-hidden="true"></span>
      {/if}
      {@render stageNode(stage)}
    {/each}
  </div>
  <div class="embed-row">
    <span class="embed-lead" aria-hidden="true">&#8627;</span>
    {#each SATELLITE_STAGES as stage (stage)}
      {@render stageNode(stage)}
    {/each}
    <span class="embed-note">async off write</span>
  </div>
</div>

<style>
  .pipeline-diagram {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    padding: var(--space-3) var(--space-4);
  }

  .stage-row {
    display: flex;
    align-items: center;
    gap: var(--space-1);
    flex-wrap: wrap;
  }

  .stage-link {
    flex: 0 0 var(--space-4);
    height: var(--border-width, 1px);
    background: var(--border-muted);
  }

  .stage-node {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    height: 28px;
    padding: 0 var(--space-3);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
    cursor: pointer;
    white-space: nowrap;
  }

  .stage-node:hover {
    border-color: var(--border-default);
    color: var(--text-secondary);
  }

  .stage-node.is-done {
    color: var(--text-secondary);
    border-color: var(--border-default);
  }

  .stage-node.is-done .stage-dot {
    background: var(--accent-green);
  }

  .stage-node.is-active {
    color: var(--text-primary);
    border-color: var(--accent-blue);
    background: color-mix(in srgb, var(--accent-blue) 10%, var(--bg-surface));
  }

  .stage-node.is-active .stage-dot {
    background: var(--accent-blue);
    animation: stage-pulse 1.2s ease-in-out infinite;
  }

  .stage-node.is-selected {
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent-blue) 40%, transparent);
  }

  .stage-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--border-default);
    flex: none;
  }

  .stage-name {
    text-transform: uppercase;
    letter-spacing: 0.04em;
    font-size: var(--font-size-xs);
  }

  .embed-row {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding-left: var(--space-7);
  }

  .embed-lead {
    color: var(--text-muted);
    font-size: var(--font-size-md);
    line-height: 1;
  }

  .embed-note {
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
  }

  @keyframes stage-pulse {
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: var(--opacity-disabled);
    }
  }
</style>
