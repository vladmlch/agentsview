<script lang="ts">
  import {
    Button,
    Chip,
    IconButton,
    Typeahead,
    type TypeaheadOption,
  } from "@kenn-io/kit-ui";
  import { onDestroy, onMount } from "svelte";
  import {
    pipelineStore,
    PipelineStore,
    type PipelineStage,
  } from "../../stores/pipeline.svelte.js";
  import { RefreshCwIcon } from "../../icons.js";
  import PipelineDiagram from "./PipelineDiagram.svelte";
  import PipelineTimeline from "./PipelineTimeline.svelte";
  import PipelineStatePanel from "./PipelineStatePanel.svelte";

  interface Props {
    /** Injectable for tests; production uses the singleton. */
    store?: PipelineStore;
  }

  let { store = pipelineStore }: Props = $props();

  let scenarioId = $state("");
  let recordingName = $state("");
  let stageFilter = $state<PipelineStage | null>(null);
  let observing = $state(false);
  let fileInput: HTMLInputElement | undefined = $state();

  const view = $derived(store.viewState);

  const scenarioOptions = $derived<TypeaheadOption[]>(
    store.scenarios.map((s) => ({
      name: s.id,
      label: `${s.id} — ${s.title}`,
      displayLabel: s.id,
      meta: s.summary,
    })),
  );

  const recordingOptions = $derived<TypeaheadOption[]>(
    store.recordings.map((name) => ({ name, label: name })),
  );

  const runLabel = $derived.by(() => {
    if (store.runId === null) return "no run attached";
    const short =
      store.runId.length > 12 ? `${store.runId.slice(0, 12)}…` : store.runId;
    const kind = store.header !== null ? "recording" : "run";
    return `${kind} ${short}${view.run.finished ? " (finished)" : ""}`;
  });

  onMount(() => {
    void store.loadScenarios();
    void store.loadRecordings();
    void store.loadRuns().then(() => {
      // Reconcile the toggle with reality: an observe window left open
      // (e.g. after navigating away mid-observe) shows up in the runs
      // list as a running run whose scenario is "observe". Without
      // this the UI could only offer "Observe" — which the server
      // rejects with 409 — and no path to issue the DELETE.
      observing = store.runs.some(
        (run) => run.scenario_id === "observe" && run.state === "running",
      );
    });
  });

  onDestroy(() => {
    // detach() only closes the local stream; the server-side observe
    // tap would keep recording the real engine and 409 every later
    // run until something issues DELETE /observe.
    if (observing) void store.stopObserve();
    store.detach();
  });

  // The observe window closes server-side when its run_finished bookend
  // arrives — drop the toggle so the button doesn't lie about a live
  // tap. Only the observe run's own bookend counts: a finished
  // recording loaded mid-observe must not clear the flag.
  $effect(() => {
    if (observing && view.run.finished && view.run.scenarioId === "observe") {
      observing = false;
    }
  });

  function selectScenario(value: string) {
    scenarioId = value;
  }

  async function selectRecording(value: string) {
    recordingName = value;
    await store.playRecording(value);
  }

  async function toggleObserve() {
    if (observing) {
      await store.stopObserve();
      // capture() swallows the failure into store.error — keep the
      // toggle on when the DELETE failed so the live tap can still
      // be stopped from here.
      observing = store.error !== null;
    } else {
      recordingName = "";
      const id = await store.startObserve();
      if (id !== undefined) observing = true;
    }
  }

  async function importSelected(input: HTMLInputElement) {
    const file = input.files?.[0];
    input.value = "";
    if (!file) return;
    const name = await store.importRecording(file);
    if (name !== undefined) await selectRecording(name);
  }

  function shortRunId(id: string): string {
    return id.length > 10 ? `${id.slice(0, 10)}…` : id;
  }
</script>

<div class="pipeline-page">
  <header class="pipeline-header">
    <div class="header-row">
      <h1 class="page-title">Pipeline playground</h1>
      <Typeahead
        options={scenarioOptions}
        value={scenarioId}
        fallbackLabel={scenarioId || "Pick a scenario"}
        placeholder="Search scenarios"
        emptyLabel="No scenarios"
        title="Scenario to run in the sandbox"
        onselect={selectScenario}
      />
      <Button
        size="sm"
        surface="solid"
        tone="workflow"
        label="Run"
        title="Start a sandboxed run of the selected scenario"
        disabled={scenarioId === ""}
        onclick={() => {
          recordingName = "";
          void store.startRun(scenarioId);
        }}
      />
      <Button
        size="sm"
        surface={observing ? "solid" : "outline"}
        tone={observing ? "danger" : "neutral"}
        label={observing ? "Stop observe" : "Observe"}
        title="Attach the daemon's real ingest engine as an event source"
        onclick={() => void toggleObserve()}
      />
      <Button
        size="sm"
        surface="outline"
        label="Reset sandbox"
        title="Clear the scenario sandbox directory"
        onclick={() => void store.resetSandbox()}
      />
      <span class="run-label" title={store.runId ?? ""}>{runLabel}</span>
      {#if store.error !== null}
        <span class="pipeline-error" role="alert">{store.error}</span>
      {/if}
    </div>
    <div class="header-row">
      <span class="row-label">Recordings</span>
      <Typeahead
        options={recordingOptions}
        value={recordingName}
        fallbackLabel={recordingName || "Pick a recording"}
        placeholder="Search recordings"
        emptyLabel="No recordings on the server"
        title="Replay a server-side .jsonl run recording"
        onselect={(v) => void selectRecording(v)}
      />
      <Button
        size="sm"
        surface="outline"
        label="Import…"
        title="Upload a .jsonl recording and replay it"
        onclick={() => fileInput?.click()}
      />
      <input
        bind:this={fileInput}
        type="file"
        accept=".jsonl"
        hidden
        onchange={(e) => void importSelected(e.currentTarget)}
      />
      <IconButton
        ariaLabel="Refresh runs and recordings"
        size="sm"
        onclick={() => {
          void store.loadRuns();
          void store.loadRecordings();
        }}
      >
        <RefreshCwIcon size={14} />
      </IconButton>
      <span class="row-label">Runs</span>
      <div class="run-strip">
        {#if store.runs.length === 0}
          <span class="no-runs">none yet</span>
        {/if}
        {#each store.runs as run (run.run_id)}
          <Chip
            size="sm"
            interactive
            tone={run.run_id === store.runId ? "info" : "neutral"}
            title={`${run.run_id} · ${run.scenario_id} · ${run.state} · ${run.event_count} events — click to replay`}
            onclick={() => {
              recordingName = "";
              store.attachLive(run.run_id);
            }}
          >
            {shortRunId(run.run_id)} · {run.scenario_id}
          </Chip>
        {/each}
      </div>
    </div>
  </header>

  <PipelineDiagram
    stageStatus={view.stageStatus}
    selected={stageFilter}
    onselect={(stage) => (stageFilter = stage)}
  />

  <div class="pipeline-split">
    <PipelineTimeline
      events={store.events}
      cursor={store.cursor}
      live={store.live}
      playing={store.playing}
      speed={store.speed}
      filter={stageFilter}
      onseek={(i) => store.seek(i)}
      onstep={(d) => store.stepBy(d)}
      onplaypause={() => (store.playing ? store.pause() : store.play())}
      onspeed={(s) => (store.playing ? store.play(s) : (store.speed = s))}
      ongolive={() => store.seekToEnd()}
      onclearfilter={() => (stageFilter = null)}
    />
    <PipelineStatePanel {view} />
  </div>
</div>

<style>
  .pipeline-page {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
    min-width: 0;
  }

  .pipeline-header {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    padding: var(--space-3) var(--space-4);
    border-bottom: 1px solid var(--border-default);
  }

  .header-row {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    flex-wrap: wrap;
  }

  .page-title {
    margin: 0;
    font-size: var(--font-size-md);
    font-weight: 600;
    color: var(--text-primary);
    white-space: nowrap;
  }

  .row-label {
    font-size: var(--font-size-2xs);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
    white-space: nowrap;
  }

  .run-label {
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .pipeline-error {
    font-size: var(--font-size-xs);
    color: var(--accent-red);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 40%;
  }

  .run-strip {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    overflow-x: auto;
    min-width: 0;
    flex: 1;
    padding: var(--space-1) 0;
  }

  .no-runs {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .pipeline-split {
    flex: 1;
    display: grid;
    grid-template-columns: minmax(0, 3fr) minmax(280px, 2fr);
    min-height: 0;
    border-top: 1px solid var(--border-default);
  }
</style>
