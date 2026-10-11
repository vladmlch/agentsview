<script lang="ts">
  import { Button, IconButton, SegmentedControl } from "@kenn-io/kit-ui";
  import type {
    PipelineEvent,
    PipelineStage,
  } from "../../stores/pipeline.svelte.js";
  import {
    PauseIcon,
    PlayIcon,
    StepBackIcon,
    StepForwardIcon,
  } from "../../icons.js";

  interface Props {
    /** Full event buffer — the cursor selects the consumed prefix. */
    events: PipelineEvent[];
    /** Replay position: events[0..cursor) are consumed. */
    cursor: number;
    live?: boolean;
    playing?: boolean;
    /** Events per playback tick. */
    speed?: number;
    /** When set, only events of this stage are listed (seek indexes stay real). */
    filter?: PipelineStage | null;
    /** Seek so the clicked event becomes the latest consumed one. */
    onseek?: (index: number) => void;
    onstep?: (delta: number) => void;
    onplaypause?: () => void;
    onspeed?: (speed: number) => void;
    /** Jump back to the tail and resume live following. */
    ongolive?: () => void;
    onclearfilter?: () => void;
  }

  let {
    events,
    cursor,
    live = true,
    playing = false,
    speed = 1,
    filter = null,
    onseek = () => {},
    onstep = () => {},
    onplaypause = () => {},
    onspeed = () => {},
    ongolive = () => {},
    onclearfilter = () => {},
  }: Props = $props();

  const SPEED_OPTIONS = [
    { value: "1", label: "1x" },
    { value: "5", label: "5x" },
    { value: "20", label: "20x" },
  ];

  function basename(path: string | undefined): string {
    if (!path) return "";
    const idx = path.lastIndexOf("/");
    return idx === -1 ? path : path.slice(idx + 1);
  }

  interface Row {
    index: number;
    event: PipelineEvent;
    dt: number;
  }

  const rows = $derived.by<Row[]>(() => {
    const out: Row[] = [];
    let prevAt = 0;
    let hasPrev = false;
    for (let i = 0; i < events.length; i++) {
      const event = events[i]!;
      if (filter !== null && event.stage !== filter) continue;
      const dt = hasPrev ? event.at_ms - prevAt : 0;
      out.push({ index: i, event, dt });
      prevAt = event.at_ms;
      hasPrev = true;
    }
    return out;
  });

  /** Row index the cursor marker attaches to: the first visible row at or
   * past the replay position, or the end of the list. */
  const markerBeforeIndex = $derived.by(() => {
    for (const row of rows) {
      if (row.index >= cursor) return row.index;
    }
    return -1;
  });
  const markerAtEnd = $derived(markerBeforeIndex === -1 && rows.length > 0);

  let listEl: HTMLDivElement | undefined = $state();

  // Keep the cursor on screen while replaying or tailing: aim for the
  // row the marker actually attaches to — a stage filter can hide the
  // cursor row entirely, and after a filter change the marker may sit
  // off-screen until the next cursor tick. Tracking markerBeforeIndex
  // (not cursor) covers both. jsdom has no scrollIntoView — the ?.()
  // call degrades to a no-op there.
  $effect(() => {
    if (!listEl) return;
    const idx = markerBeforeIndex;
    const target =
      (idx === -1 ? null : listEl.querySelector(`[data-idx="${idx}"]`)) ??
      listEl.querySelector(".cursor-line");
    target?.scrollIntoView?.({ block: "nearest" });
  });
</script>

<div class="pipeline-timeline">
  <div class="timeline-bar">
    <IconButton
      ariaLabel="Step back one event"
      size="sm"
      disabled={cursor <= 0}
      onclick={() => onstep(-1)}
    >
      <StepBackIcon size={14} />
    </IconButton>
    <Button
      size="sm"
      surface="soft"
      disabled={!playing && cursor >= events.length}
      title={playing ? "Pause replay" : "Replay forward"}
      onclick={onplaypause}
    >
      {#if playing}
        <PauseIcon size={14} /> Pause
      {:else}
        <PlayIcon size={14} /> Play
      {/if}
    </Button>
    <IconButton
      ariaLabel="Step forward one event"
      size="sm"
      disabled={cursor >= events.length}
      onclick={() => onstep(1)}
    >
      <StepForwardIcon size={14} />
    </IconButton>
    <SegmentedControl
      options={SPEED_OPTIONS}
      value={String(speed)}
      ariaLabel="Replay speed, events per tick"
      onchange={(v) => onspeed(Number(v))}
    />
    <span class="timeline-pos">{cursor} / {events.length}</span>
    <span class="timeline-live" class:on={live}>
      {#if playing}replaying{:else if live}live{:else}paused{/if}
    </span>
    {#if !live}
      <Button
        size="sm"
        surface="soft"
        label="Jump to live"
        title="Seek to the tail and resume live following"
        onclick={ongolive}
      />
    {/if}
    {#if filter !== null}
      <span class="timeline-filter">
        filtered: {filter}
        <button type="button" class="filter-clear" onclick={onclearfilter}>
          clear
        </button>
      </span>
    {/if}
  </div>

  <div class="timeline-list" bind:this={listEl}>
    {#if rows.length === 0}
      <div class="timeline-empty">
        {events.length === 0 ? "No events yet — start a run or load a recording." : "No events match the stage filter."}
      </div>
    {/if}
    {#each rows as row (row.event.seq)}
      {#if row.index === markerBeforeIndex}
        <div class="cursor-line" aria-hidden="true"><span>cursor</span></div>
      {/if}
      <button
        type="button"
        class="timeline-row"
        class:consumed={row.index < cursor}
        class:pending={row.index >= cursor}
        data-idx={row.index}
        title={`seq ${row.event.seq} · ${row.event.stage} · ${row.event.kind}`}
        onclick={() => onseek(row.index + 1)}
      >
        <span class="col-seq">#{row.event.seq}</span>
        <span class="col-dt">+{row.dt}ms</span>
        <span class="col-stage" data-stage={row.event.stage}>{row.event.stage}</span>
        <span class="col-kind">{row.event.kind}</span>
        <span class="col-path">{basename(row.event.path)}</span>
      </button>
    {/each}
    {#if markerAtEnd}
      <div class="cursor-line" aria-hidden="true"><span>cursor</span></div>
    {/if}
  </div>
</div>

<style>
  .pipeline-timeline {
    display: flex;
    flex-direction: column;
    min-height: 0;
    min-width: 0;
  }

  .timeline-bar {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-2) var(--space-3);
    border-bottom: 1px solid var(--border-muted);
    flex-wrap: wrap;
  }

  .timeline-pos {
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }

  .timeline-live {
    font-size: var(--font-size-2xs);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
  }

  .timeline-live.on {
    color: var(--accent-green);
  }

  .timeline-filter {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    color: var(--accent-blue);
  }

  .filter-clear {
    border: none;
    background: none;
    color: var(--accent-blue);
    font-size: var(--font-size-xs);
    cursor: pointer;
    text-decoration: underline;
    padding: 0;
  }

  .timeline-list {
    flex: 1;
    overflow-y: auto;
    min-height: 0;
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
  }

  .timeline-empty {
    padding: var(--space-6);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    text-align: center;
  }

  .timeline-row {
    display: grid;
    grid-template-columns: 52px 64px 72px minmax(0, 1fr) minmax(0, 1fr);
    align-items: baseline;
    gap: var(--space-2);
    width: 100%;
    padding: 2px var(--space-3);
    border: none;
    border-bottom: 1px solid var(--border-muted);
    background: none;
    text-align: left;
    font: inherit;
    color: var(--text-primary);
    cursor: pointer;
  }

  .timeline-row:hover {
    background: var(--bg-surface-hover);
  }

  .timeline-row.pending {
    color: var(--text-muted);
  }

  .col-seq {
    color: var(--text-muted);
  }

  .col-dt {
    color: var(--text-muted);
    text-align: right;
  }

  .col-stage {
    text-transform: uppercase;
    font-size: var(--font-size-2xs);
    color: var(--accent-blue);
  }

  .col-kind {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .col-path {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    text-align: right;
    color: var(--text-muted);
  }

  .cursor-line {
    position: relative;
    height: 0;
    border-top: 1px dashed var(--accent-blue);
    margin: 0 var(--space-3);
  }

  .cursor-line span {
    position: absolute;
    right: 0;
    top: -0.7em;
    background: var(--bg-surface);
    padding: 0 var(--space-1);
    font-size: var(--font-size-2xs);
    text-transform: uppercase;
    color: var(--accent-blue);
  }
</style>
