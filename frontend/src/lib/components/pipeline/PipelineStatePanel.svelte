<script lang="ts">
  import { EmptyState, SegmentedControl } from "@kenn-io/kit-ui";
  import type { PipelineViewState } from "../../stores/pipeline.svelte.js";

  interface Props {
    /** Reduced state for events[0..cursor) — what the run has done so far. */
    view: PipelineViewState;
  }

  let { view }: Props = $props();

  type Tab = "file" | "events" | "gates" | "db" | "queue" | "cursor" | "steps";
  const TABS: { value: Tab; label: string }[] = [
    { value: "file", label: "File" },
    { value: "events", label: "Events" },
    { value: "gates", label: "Gates" },
    { value: "db", label: "DB" },
    { value: "queue", label: "Queue" },
    { value: "cursor", label: "Cursor" },
    { value: "steps", label: "Steps" },
  ];

  let tab = $state<Tab>("file");
  let selectedPath = $state<string | null>(null);

  const filePaths = $derived(Object.keys(view.files).sort());
  const activePath = $derived(
    selectedPath !== null && selectedPath in view.files
      ? selectedPath
      : (filePaths[0] ?? null),
  );

  function basename(path: string): string {
    const idx = path.lastIndexOf("/");
    return idx === -1 ? path : path.slice(idx + 1);
  }

  function shortPath(path: string): string {
    // Keep the tail of a long absolute path readable.
    if (path.length <= 48) return path;
    return "…" + path.slice(path.length - 48);
  }

  function fmt(v: unknown): string {
    if (v === undefined) return "—";
    if (typeof v === "object" && v !== null) return JSON.stringify(v);
    return String(v);
  }
</script>

<div class="state-panel">
  <div class="tab-strip">
    <SegmentedControl
      options={TABS}
      value={tab}
      ariaLabel="State panel tabs"
      variant="borderless"
      onchange={(v) => (tab = v as Tab)}
    />
  </div>

  <div class="tab-body">
    {#if tab === "file"}
      {#if filePaths.length === 0}
        <EmptyState title="No file content yet" description="line_classified events appear here once the parser runs." />
      {:else}
        <div class="path-strip">
          {#each filePaths as path (path)}
            <button
              type="button"
              class="path-chip"
              class:is-active={path === activePath}
              title={path}
              onclick={() => (selectedPath = path)}
            >
              {basename(path)}
            </button>
          {/each}
        </div>
        {#if activePath !== null}
          <pre class="file-view">{#each view.files[activePath] ?? [] as line, i (i)}<span class="fl" class:malformed={!line.parsed}><span class="fl-no">{line.no}</span><span class="fl-raw">{line.raw}</span>{#if line.truncated}<span class="fl-trunc">… truncated</span>{/if}</span>{/each}</pre>
        {/if}
      {/if}
    {:else if tab === "events"}
      {#if view.parsedEvents.length === 0}
        <EmptyState title="No events consumed" description="Consumed events up to the cursor appear here." />
      {:else}
        <ul class="event-list">
          {#each view.parsedEvents as ev (ev.seq)}
            <li class="event-row">
              <span class="ev-head">
                <span class="ev-seq">#{ev.seq}</span>
                <span class="ev-stage">{ev.stage}</span>
                <span class="ev-kind">{ev.kind}</span>
                {#if ev.path}<span class="ev-path">{basename(ev.path)}</span>{/if}
              </span>
              {#if ev.payload && Object.keys(ev.payload).length > 0}
                <code class="ev-payload">{JSON.stringify(ev.payload)}</code>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
    {:else if tab === "gates"}
      {#if view.gates.length === 0}
        <EmptyState title="No gate verdicts" description="gate_verdict events appear here." />
      {:else}
        <table class="kv-table">
          <thead>
            <tr><th>Gate</th><th>Decision</th><th>Reason</th><th>Path</th></tr>
          </thead>
          <tbody>
            {#each view.gates as g, i (i)}
              <tr>
                <td>{g.gate}</td>
                <td class="decision" data-decision={g.decision}>{g.decision}</td>
                <td>{g.reason}</td>
                <td class="path-cell" title={g.path}>{g.path ? basename(g.path) : ""}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    {:else if tab === "db"}
      {#if view.dbDiff.length === 0}
        <EmptyState title="No writes yet" description="messages_written and tx_committed events appear here." />
      {:else}
        <table class="kv-table">
          <thead>
            <tr><th>Session</th><th>Inserted</th><th>Updated</th><th>Sessions</th><th>Failed</th></tr>
          </thead>
          <tbody>
            {#each view.dbDiff as d, i (i)}
              <tr>
                <td class="path-cell" title={d.session_id}>{d.session_id ?? "batch"}</td>
                <td>{d.inserted}</td>
                <td>{d.updated}</td>
                <td>{fmt(d.sessions)}</td>
                <td>{fmt(d.failed)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
      <div class="stat-line">FTS rows written: <strong>{view.ftsRows}</strong></div>
    {:else if tab === "queue"}
      <dl class="kv-list">
        <dt>Pending writes</dt>
        <dd>{view.queue.pending}</dd>
        <dt>Pending bytes</dt>
        <dd>{view.queue.bytes}</dd>
        {#if view.lastFlush}
          <dt>Last flush reason</dt>
          <dd>{view.lastFlush.reason}</dd>
          <dt>Last flush pending</dt>
          <dd>{view.lastFlush.pending}</dd>
          <dt>Last flush bytes</dt>
          <dd>{view.lastFlush.bytes}</dd>
        {:else}
          <dt>Last flush</dt>
          <dd>none yet</dd>
        {/if}
      </dl>
    {:else if tab === "cursor"}
      {#if view.cursor === null}
        <EmptyState title="No cursor update" description="cursor_updated events appear here." />
      {:else}
        <dl class="kv-list">
          {#each Object.entries(view.cursor) as [key, value] (key)}
            <dt>{key}</dt>
            <dd class="path-cell" title={fmt(value)}>{fmt(value)}</dd>
          {/each}
        </dl>
      {/if}
      <dl class="kv-list run-block">
        <dt>run_id</dt>
        <dd>{view.run.runId ?? "—"}</dd>
        <dt>scenario</dt>
        <dd>{view.run.scenarioId ?? "—"}</dd>
        <dt>source</dt>
        <dd class="path-cell" title={view.run.sourceFile ?? ""}>
          {view.run.sourceFile ? shortPath(view.run.sourceFile) : "—"}
        </dd>
        <dt>finished</dt>
        <dd>{view.run.finished ? "yes" : "no"}</dd>
        {#if view.run.stats}
          <dt>stats</dt>
          <dd><code>{JSON.stringify(view.run.stats)}</code></dd>
        {/if}
      </dl>
    {:else if tab === "steps"}
      {#if view.steps.length === 0}
        <EmptyState title="No scenario steps" description="scenario_step events appear here." />
      {:else}
        <table class="kv-table">
          <thead>
            <tr><th>#</th><th>Action</th><th>Path</th><th>Detail</th></tr>
          </thead>
          <tbody>
            {#each view.steps as s, i (i)}
              <tr>
                <td>{s.n}</td>
                <td>{s.action}</td>
                <td class="path-cell" title={s.path}>{s.path ?? ""}</td>
                <td class="path-cell" title={s.detail}>{s.detail ?? ""}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    {/if}
  </div>
</div>

<style>
  .state-panel {
    display: flex;
    flex-direction: column;
    min-height: 0;
    min-width: 0;
    border-left: 1px solid var(--border-muted);
  }

  .tab-strip {
    padding: var(--space-2) var(--space-3);
    border-bottom: 1px solid var(--border-muted);
    overflow-x: auto;
  }

  .tab-body {
    flex: 1;
    overflow-y: auto;
    min-height: 0;
    padding: var(--space-3);
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }

  .path-strip {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-1);
  }

  .path-chip {
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-secondary);
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    padding: 1px var(--space-2);
    cursor: pointer;
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .path-chip:hover {
    border-color: var(--border-default);
    color: var(--text-primary);
  }

  .path-chip.is-active {
    border-color: var(--accent-blue);
    color: var(--accent-blue);
  }

  .file-view {
    margin: 0;
    padding: var(--space-2) var(--space-3);
    background: var(--bg-inset);
    border-radius: var(--radius-sm);
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    line-height: 1.5;
    overflow-x: auto;
    white-space: pre;
    flex: 1;
    min-height: 0;
  }

  .fl {
    display: block;
  }

  .fl-no {
    display: inline-block;
    width: 4ch;
    margin-right: var(--space-3);
    color: var(--text-muted);
    text-align: right;
    user-select: none;
  }

  .fl.malformed .fl-raw {
    color: var(--accent-red);
    background: color-mix(in srgb, var(--accent-red) 8%, transparent);
  }

  .fl-trunc {
    color: var(--accent-amber);
    margin-left: var(--space-2);
  }

  .event-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
  }

  .event-row {
    padding: var(--space-1) 0;
    border-bottom: 1px solid var(--border-muted);
  }

  .ev-head {
    display: flex;
    gap: var(--space-2);
    align-items: baseline;
  }

  .ev-seq {
    color: var(--text-muted);
    min-width: 4ch;
  }

  .ev-stage {
    color: var(--accent-blue);
    text-transform: uppercase;
  }

  .ev-kind {
    color: var(--text-primary);
  }

  .ev-path {
    color: var(--text-muted);
    margin-left: auto;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 40%;
  }

  .ev-payload {
    display: block;
    color: var(--text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .kv-table {
    border-collapse: collapse;
    width: 100%;
    font-size: var(--font-size-xs);
  }

  .kv-table th {
    text-align: left;
    font-weight: 500;
    color: var(--text-muted);
    padding: var(--space-1) var(--space-2);
    border-bottom: 1px solid var(--border-default);
    white-space: nowrap;
  }

  .kv-table td {
    padding: var(--space-1) var(--space-2);
    border-bottom: 1px solid var(--border-muted);
    color: var(--text-primary);
    vertical-align: top;
  }

  .decision[data-decision="append"],
  .decision[data-decision="accept"],
  .decision[data-decision="pass"] {
    color: var(--accent-green);
  }

  .decision[data-decision="reject"],
  .decision[data-decision="drop"],
  .decision[data-decision="rescan"] {
    color: var(--accent-red);
  }

  .path-cell {
    max-width: 180px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
  }

  .stat-line {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
  }

  .kv-list {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: var(--space-1) var(--space-4);
    margin: 0;
    font-size: var(--font-size-xs);
  }

  .kv-list dt {
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    align-self: baseline;
  }

  .kv-list dd {
    margin: 0;
    color: var(--text-primary);
    overflow-wrap: anywhere;
  }

  .run-block {
    margin-top: var(--space-4);
    padding-top: var(--space-3);
    border-top: 1px solid var(--border-muted);
  }
</style>
