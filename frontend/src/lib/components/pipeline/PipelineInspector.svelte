<script lang="ts">
  import { Button, IconButton } from "@kenn-io/kit-ui";
  import {
    eventAnomaly,
    type PipelineEvent,
  } from "../../stores/pipeline.svelte.js";
  import {
    ChevronLeftIcon,
    ChevronRightIcon,
    CopyIcon,
    TriangleAlertIcon,
    XIcon,
  } from "../../icons.js";

  interface Props {
    /** Full event buffer — `selected` indexes into it, not into a
     * filtered view. */
    events: PipelineEvent[];
    /** Buffer index of the event under inspection, or null. */
    selected: number | null;
    /** Move the inspector selection: navigation jumps and the close
     * button — never seeks the replay cursor. */
    onselect?: (index: number | null) => void;
    /** Seek so `index` becomes the count of consumed events (row
     * index + 1), like a timeline row double-click. */
    onseek?: (index: number) => void;
  }

  let {
    events,
    selected,
    onselect = () => {},
    onseek = () => {},
  }: Props = $props();

  function basename(path: string | undefined): string {
    if (!path) return "";
    const idx = path.lastIndexOf("/");
    return idx === -1 ? path : path.slice(idx + 1);
  }

  /** First buffer index walking from `from` in `step` direction whose
   * event satisfies `pred`, or -1 at the buffer bounds. */
  function findIndex(
    from: number,
    step: 1 | -1,
    pred: (e: PipelineEvent) => boolean,
  ): number {
    for (let i = from; i >= 0 && i < events.length; i += step) {
      const e = events[i];
      if (e !== undefined && pred(e)) return i;
    }
    return -1;
  }

  /** The inspected event, or null when `selected` is unset or outside
   * the buffer — the panel then shows only the empty state. */
  const event = $derived(selected === null ? null : (events[selected] ?? null));

  /** Gap to the previous buffer event — the timeline's dt convention:
   * the first event reports +0ms. */
  const dt = $derived.by(() => {
    if (event === null || selected === null || selected <= 0) return 0;
    const prev = events[selected - 1];
    return prev === undefined ? 0 : event.at_ms - prev.at_ms;
  });

  const anomaly = $derived(event === null ? null : eventAnomaly(event));

  /** Nearest earlier/later buffer index with the same non-empty path.
   * An event without a path has no file neighbours — both stay -1. */
  const prevInFile = $derived.by(() => {
    if (event === null || selected === null) return -1;
    const path = event.path ?? "";
    if (path === "") return -1;
    return findIndex(selected - 1, -1, (e) => (e.path ?? "") === path);
  });
  const nextInFile = $derived.by(() => {
    if (event === null || selected === null) return -1;
    const path = event.path ?? "";
    if (path === "") return -1;
    return findIndex(selected + 1, 1, (e) => (e.path ?? "") === path);
  });

  /** Nearest earlier/later buffer index of the same kind. */
  const prevOfKind = $derived.by(() => {
    if (event === null || selected === null) return -1;
    const kind = event.kind;
    return findIndex(selected - 1, -1, (e) => e.kind === kind);
  });
  const nextOfKind = $derived.by(() => {
    if (event === null || selected === null) return -1;
    const kind = event.kind;
    return findIndex(selected + 1, 1, (e) => e.kind === kind);
  });

  const payloadJson = $derived.by(() => {
    if (event === null || event.payload === undefined) return null;
    return JSON.stringify(event.payload, null, 2);
  });

  /** Copy the raw event JSON. jsdom has no navigator.clipboard — the
   * optional chain degrades to a no-op there — while .catch and the
   * try/catch swallow a rejected or synchronous failure (e.g. missing
   * clipboard permission). Copy is best-effort and never throws. */
  function copyEvent(): void {
    if (event === null) return;
    try {
      void navigator.clipboard
        ?.writeText(JSON.stringify(event, null, 2))
        ?.catch(() => {});
    } catch {
      // Nonstandard clipboard object threw synchronously — ignore.
    }
  }

  function seekHere(): void {
    if (selected === null) return;
    onseek(selected + 1);
  }
</script>

<div class="inspector">
  {#if event === null}
    <div class="inspector-empty">
      Click an event to inspect it. Double-click seeks the cursor.
    </div>
  {:else}
    <div class="inspector-head">
      <span class="inspector-seq">#{event.seq}</span>
      <span class="inspector-stage" data-stage={event.stage}>{event.stage}</span>
      <span class="inspector-kind">{event.kind}</span>
      {#if event.path !== undefined && event.path !== ""}
        <span class="inspector-path" title={event.path}
          >{basename(event.path)}</span
        >
      {/if}
      <span class="inspector-time">
        {new Date(event.at_ms).toLocaleTimeString()} · +{dt}ms
      </span>
      <span class="inspector-spacer"></span>
      <IconButton
        ariaLabel="Copy event JSON"
        title="Copy the raw event as JSON"
        size="sm"
        onclick={copyEvent}
      >
        <CopyIcon size={14} />
      </IconButton>
      <IconButton
        ariaLabel="Close inspector"
        size="sm"
        onclick={() => onselect(null)}
      >
        <XIcon size={14} />
      </IconButton>
    </div>
    {#if anomaly !== null}
      <div class="inspector-anomaly">
        <TriangleAlertIcon size={12} />{anomaly}
      </div>
    {/if}
    <div class="inspector-nav">
      <span class="nav-group">
        <span class="nav-label">in file</span>
        <IconButton
          ariaLabel="Previous event in file"
          title="Select the previous event with the same path"
          size="sm"
          disabled={prevInFile === -1}
          onclick={() => onselect(prevInFile)}
        >
          <ChevronLeftIcon size={14} />
        </IconButton>
        <IconButton
          ariaLabel="Next event in file"
          title="Select the next event with the same path"
          size="sm"
          disabled={nextInFile === -1}
          onclick={() => onselect(nextInFile)}
        >
          <ChevronRightIcon size={14} />
        </IconButton>
      </span>
      <span class="nav-group">
        <span class="nav-label">of kind</span>
        <IconButton
          ariaLabel="Previous event of kind"
          title="Select the previous event of the same kind"
          size="sm"
          disabled={prevOfKind === -1}
          onclick={() => onselect(prevOfKind)}
        >
          <ChevronLeftIcon size={14} />
        </IconButton>
        <IconButton
          ariaLabel="Next event of kind"
          title="Select the next event of the same kind"
          size="sm"
          disabled={nextOfKind === -1}
          onclick={() => onselect(nextOfKind)}
        >
          <ChevronRightIcon size={14} />
        </IconButton>
      </span>
      <span class="inspector-spacer"></span>
      <Button
        size="sm"
        surface="soft"
        label="Seek here"
        title="Seek the replay cursor to just after this event"
        onclick={seekHere}
      />
    </div>
    {#if payloadJson === null}
      <pre class="inspector-payload empty">no payload</pre>
    {:else}
      <pre class="inspector-payload">{payloadJson}</pre>
    {/if}
  {/if}
</div>

<style>
  .inspector {
    display: flex;
    flex-direction: column;
    min-height: 0;
    min-width: 0;
    max-height: 40%;
    overflow-y: auto;
    border-top: 1px solid var(--border-default);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
  }

  .inspector-empty {
    padding: var(--space-6);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    text-align: center;
  }

  .inspector-head {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-2) var(--space-3);
    border-bottom: 1px solid var(--border-muted);
    flex-wrap: wrap;
  }

  .inspector-seq {
    color: var(--text-muted);
  }

  .inspector-stage {
    text-transform: uppercase;
    font-size: var(--font-size-2xs);
    color: var(--accent-blue);
  }

  .inspector-kind {
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .inspector-path {
    color: var(--text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 30%;
  }

  .inspector-time {
    color: var(--text-muted);
    white-space: nowrap;
  }

  .inspector-spacer {
    flex: 1;
  }

  .inspector-anomaly {
    display: flex;
    align-items: center;
    gap: var(--space-1);
    padding: var(--space-1) var(--space-3);
    border-bottom: 1px solid var(--border-muted);
    color: var(--accent-red);
  }

  .inspector-nav {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-1) var(--space-3);
    border-bottom: 1px solid var(--border-muted);
    flex-wrap: wrap;
  }

  .nav-group {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
  }

  .nav-label {
    font-size: var(--font-size-2xs);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
    margin-right: var(--space-1);
  }

  .inspector-payload {
    margin: 0;
    padding: var(--space-2) var(--space-3);
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    line-height: 1.5;
    color: var(--text-primary);
    overflow-x: auto;
    white-space: pre;
  }

  .inspector-payload.empty {
    color: var(--text-muted);
  }
</style>
