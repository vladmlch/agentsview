<script lang="ts">
  import {
    Button,
    Chip,
    IconButton,
    SearchInput,
    SegmentedControl,
  } from "@kenn-io/kit-ui";
  import {
    PIPELINE_STAGES,
    eventAnomaly,
    type PipelineEvent,
    type PipelineStage,
  } from "../../stores/pipeline.svelte.js";
  import {
    ChevronLeftIcon,
    ChevronRightIcon,
    FunnelIcon,
    PauseIcon,
    PlayIcon,
    StepBackIcon,
    StepForwardIcon,
    TriangleAlertIcon,
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
    /** Inspector selection: an index into `events`, not a seq. */
    selected?: number | null;
    /** Kinds to list; an empty (or absent) set lists every kind. */
    kindFilter?: Set<string>;
    /** Case-insensitive substring matched against kind, path, and the
     * serialized payload. */
    query?: string;
    /** When true, only rows eventAnomaly flags are listed. */
    anomaliesOnly?: boolean;
    /** Events the transport skipped (seq gaps) — shown as a counter. */
    dropped?: number;
    /** Seek so the event at `index` becomes the latest consumed one
     * (row double-click). */
    onseek?: (index: number) => void;
    /** Inspect: fires on a single row click with the event index. */
    onselect?: (index: number) => void;
    onkindchange?: (kinds: Set<string>) => void;
    onquerychange?: (query: string) => void;
    onanomalieschange?: (on: boolean) => void;
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
    selected = null,
    kindFilter = new Set<string>(),
    query = "",
    anomaliesOnly = false,
    dropped = 0,
    onseek = () => {},
    onselect = () => {},
    onkindchange = () => {},
    onquerychange = () => {},
    onanomalieschange = () => {},
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

  /** Lowercased haystack for the query filter: kind, path, and the
   * serialized payload. Buffer events are immutable, so the string is
   * memoized per event object instead of rebuilt on each keystroke. */
  const haystacks = new WeakMap<PipelineEvent, string>();
  function haystack(ev: PipelineEvent): string {
    let h = haystacks.get(ev);
    if (h === undefined) {
      h =
        `${ev.kind}\n${ev.path ?? ""}\n${JSON.stringify(ev.payload ?? null)}`.toLowerCase();
      haystacks.set(ev, h);
    }
    return h;
  }

  interface Row {
    index: number;
    event: PipelineEvent;
    dt: number;
    anomaly: string | null;
  }

  // The row list ANDs every active filter: the stage prop, the kind
  // set, the text query, and anomalies-only. `index` always stays the
  // real buffer index so seek/select keep working after filtering.
  const rows = $derived.by<Row[]>(() => {
    const out: Row[] = [];
    const needle = query.trim().toLowerCase();
    let prevAt = 0;
    let hasPrev = false;
    for (let i = 0; i < events.length; i++) {
      const event = events[i]!;
      if (filter !== null && event.stage !== filter) continue;
      if (kindFilter.size > 0 && !kindFilter.has(event.kind)) continue;
      const anomaly = eventAnomaly(event);
      if (anomaliesOnly && anomaly === null) continue;
      if (needle !== "" && !haystack(event).includes(needle)) continue;
      const dt = hasPrev ? event.at_ms - prevAt : 0;
      out.push({ index: i, event, dt, anomaly });
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

  /** Anomalies across the whole buffer, not just the listed rows. */
  const anomalyCount = $derived.by(() => {
    let n = 0;
    for (const event of events) {
      if (eventAnomaly(event) !== null) n += 1;
    }
    return n;
  });

  interface KindGroup {
    stage: PipelineStage;
    kinds: string[];
  }

  /** Distinct kinds present in the buffer, grouped under their stage
   * heading in PIPELINE_STAGES order. Kinds keep first-seen order —
   * the sequence the pipeline actually produced them. */
  const kindGroups = $derived.by<KindGroup[]>(() => {
    const byStage = new Map<PipelineStage, Set<string>>();
    for (const event of events) {
      let set = byStage.get(event.stage);
      if (set === undefined) {
        set = new Set();
        byStage.set(event.stage, set);
      }
      set.add(event.kind);
    }
    const groups: KindGroup[] = [];
    for (const stage of PIPELINE_STAGES) {
      const kinds = byStage.get(stage);
      if (kinds !== undefined) groups.push({ stage, kinds: [...kinds] });
      byStage.delete(stage);
    }
    // Stages outside PIPELINE_STAGES (a recording newer than this
    // client) still get a group, appended after the known ones.
    for (const [stage, kinds] of byStage) {
      groups.push({ stage, kinds: [...kinds] });
    }
    return groups;
  });

  /** Nearest listed row around the cursor — the prev/next match jump
   * targets. The selected row is excluded: after a jump lands at idx
   * (cursor = idx+1), "previous" must walk on to the match before it,
   * not land on the row the user is already looking at. */
  const prevMatch = $derived.by(() => {
    for (let i = rows.length - 1; i >= 0; i--) {
      const idx = rows[i]!.index;
      if (idx < cursor && idx !== selected) return idx;
    }
    return -1;
  });
  const nextMatch = $derived.by(() => {
    for (const row of rows) {
      if (row.index >= cursor && row.index !== selected) return row.index;
    }
    return -1;
  });

  /** Whether the second-row filters (kind set, query, anomalies) narrow
   * the list — drives the funnel accent and the clear button. */
  const listFiltersActive = $derived(
    kindFilter.size > 0 || query.trim() !== "" || anomaliesOnly,
  );

  let showFilters = $state(false);
  let listEl: HTMLDivElement | undefined = $state();

  // The page owns the query through `query`/`onquerychange`; the draft
  // gives SearchInput a bindable local mirror. The effect seeds it on
  // mount and re-seeds whenever the prop changes (e.g. the page clears
  // the filter programmatically).
  let queryDraft = $state("");
  $effect(() => {
    queryDraft = query;
  });

  function toggleKind(kind: string): void {
    const next = new Set(kindFilter);
    if (next.has(kind)) {
      next.delete(kind);
    } else {
      next.add(kind);
    }
    onkindchange(next);
  }

  function clearListFilters(): void {
    onkindchange(new Set());
    onquerychange("");
    onanomalieschange(false);
  }

  /** Jump to a matching row: seek so it becomes the latest consumed
   * event, then select it for the inspector. */
  function jumpToMatch(index: number): void {
    if (index < 0) return;
    onseek(index + 1);
    onselect(index);
  }

  function rowTitle(row: Row): string {
    const base = `seq ${row.event.seq} · ${row.event.stage} · ${row.event.kind} — click inspects, double-click seeks`;
    return row.anomaly === null ? base : `${base} · anomaly: ${row.anomaly}`;
  }

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
    <span class="timeline-spacer"></span>
    {#if !anomaliesOnly}
      <span
        class="timeline-anomalies"
        class:has-anomalies={anomalyCount > 0}
        title="Anomalous events in the buffer"
      >
        <TriangleAlertIcon size={12} />{anomalyCount}
      </span>
    {/if}
    {#if dropped > 0}
      <span
        class="timeline-dropped"
        title="Events the transport skipped between the server and this page"
      >
        {dropped} events lost in transport
      </span>
    {/if}
    <IconButton
      ariaLabel="Toggle event filters"
      title="Show kind, text, and anomaly filters"
      size="sm"
      tone={listFiltersActive ? "info" : "neutral"}
      ariaPressed={showFilters}
      onclick={() => (showFilters = !showFilters)}
    >
      <FunnelIcon size={14} />
    </IconButton>
  </div>

  {#if showFilters}
    <div class="timeline-filters">
      <SearchInput
        size="sm"
        bind:value={queryDraft}
        placeholder="kind, path, payload substring"
        ariaLabel="Filter events by text"
        clearLabel="Clear event filter"
        oninput={(v) => onquerychange(v)}
      />
      <IconButton
        ariaLabel="Previous matching event"
        title="Seek to the nearest matching event before the cursor"
        size="sm"
        disabled={prevMatch === -1}
        onclick={() => jumpToMatch(prevMatch)}
      >
        <ChevronLeftIcon size={14} />
      </IconButton>
      <IconButton
        ariaLabel="Next matching event"
        title="Seek to the nearest matching event after the cursor"
        size="sm"
        disabled={nextMatch === -1}
        onclick={() => jumpToMatch(nextMatch)}
      >
        <ChevronRightIcon size={14} />
      </IconButton>
      <Chip
        size="xs"
        interactive
        tone={anomaliesOnly ? "info" : "neutral"}
        title="List only anomalous events"
        onclick={() => onanomalieschange(!anomaliesOnly)}
      >
        anomalies
      </Chip>
      {#each kindGroups as group (group.stage)}
        <span class="kind-group">
          <span class="kind-stage">{group.stage}</span>
          {#each group.kinds as kind (kind)}
            <Chip
              size="xs"
              interactive
              tone={kindFilter.has(kind) ? "info" : "neutral"}
              title={`${kind} — click to toggle this kind filter`}
              onclick={() => toggleKind(kind)}
            >
              {kind}
            </Chip>
          {/each}
        </span>
      {/each}
      {#if listFiltersActive}
        <button type="button" class="filter-clear" onclick={clearListFilters}>
          clear
        </button>
      {/if}
    </div>
  {/if}

  <div class="timeline-list" bind:this={listEl}>
    {#if rows.length === 0}
      <div class="timeline-empty">
        {events.length === 0 ? "No events yet — start a run or load a recording." : "No events match the current filters."}
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
        class:is-selected={row.index === selected}
        class:anomaly={row.anomaly !== null}
        data-idx={row.index}
        title={rowTitle(row)}
        onclick={() => onselect(row.index)}
        ondblclick={() => onseek(row.index + 1)}
      >
        <span class="col-seq">#{row.event.seq}</span>
        <span class="col-dt">+{row.dt}ms</span>
        <span class="col-stage" data-stage={row.event.stage}>{row.event.stage}</span>
        <span class="col-kind">
          {#if row.anomaly !== null}
            <TriangleAlertIcon size={11} />
          {/if}
          {row.event.kind}
        </span>
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

  .timeline-spacer {
    flex: 1;
  }

  .timeline-anomalies {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }

  .timeline-anomalies.has-anomalies {
    color: var(--accent-red);
  }

  .timeline-dropped {
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    color: var(--accent-amber);
  }

  .timeline-filters {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-2) var(--space-3);
    border-bottom: 1px solid var(--border-muted);
    flex-wrap: wrap;
  }

  .kind-group {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
    flex-wrap: wrap;
  }

  .kind-stage {
    font-size: var(--font-size-2xs);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
    margin-right: var(--space-1);
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

  .timeline-row.is-selected {
    border-left: 2px solid var(--accent-blue);
    /* Compensate the border so the grid columns stay aligned with the
     * unselected rows. */
    padding-left: calc(var(--space-3) - 2px);
    background: var(--bg-surface-hover);
  }

  .timeline-row.anomaly {
    /* Red edge stripe independent of the selection border — an
     * anomalous selected row shows both marks side by side. */
    box-shadow: inset 2px 0 0 var(--accent-red);
  }

  .timeline-row.anomaly .col-kind {
    color: var(--accent-red);
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

  .col-kind :global(svg) {
    vertical-align: -1px;
    margin-right: 2px;
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
