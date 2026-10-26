<script lang="ts">
  import { Button } from "@kenn-io/kit-ui";
  import type { PipelineEvent } from "../../api/client.js";
  import { getLocale, m } from "../../i18n/index.js";
  import {
    comparePipelineStages,
    summarizePipelineRun,
    type PipelineRunSummary,
  } from "../../utils/pipelineAnalysis.js";
  import type { PipelineRecordingHeader } from "../../utils/pipelineRecording.js";

  interface Props {
    events: PipelineEvent[];
    runId: string | null;
    header: PipelineRecordingHeader | null;
    dropped?: number;
  }

  let { events, runId, header, dropped = 0 }: Props = $props();
  let baseline = $state<PipelineRunSummary | null>(null);
  let expanded = $state(false);
  const current = $derived(summarizePipelineRun(events, { runId, header, dropped }));
  const stages = $derived(comparePipelineStages(current, baseline));
  const partial = $derived(
    (current.eventCount > 0 && !current.complete) || (baseline !== null && !baseline.complete),
  );
  const differentScenarios = $derived(
    baseline !== null && baseline.scenarioId !== null && current.scenarioId !== null &&
      baseline.scenarioId !== current.scenarioId,
  );

  type Metric = "eventCount" | "pathCount" | "anomalyCount" | "missingEvents" |
    "classifiedLines" | "insertedMessages" | "observedSpanMs";
  const metrics: { key: Metric; label: () => string }[] = [
    { key: "eventCount", label: m.pipeline_summary_events },
    { key: "pathCount", label: m.pipeline_summary_paths },
    { key: "anomalyCount", label: m.pipeline_summary_anomalies },
    { key: "missingEvents", label: m.pipeline_summary_missing },
    { key: "classifiedLines", label: m.pipeline_summary_lines },
    { key: "insertedMessages", label: m.pipeline_summary_inserts },
    { key: "observedSpanMs", label: m.pipeline_summary_span },
  ];

  function format(value: number | null): string {
    return value === null ? "\u2014" : new Intl.NumberFormat(getLocale()).format(value);
  }

  function delta(value: number | null, reference: number | null): string {
    if (value === null || reference === null) return "\u2014";
    return new Intl.NumberFormat(getLocale(), { signDisplay: "exceptZero" }).format(value - reference);
  }

  function pin(): void {
    // The summary has no event/payload references. Copy stage counters too so
    // later streaming or source replacement can never mutate this snapshot.
    baseline = { ...current, stages: current.stages.map((stage) => ({ ...stage })) };
    expanded = true;
  }
</script>

<section class="run-summary" aria-labelledby="pipeline-summary-title">
  <div class="summary-heading">
    <h2 id="pipeline-summary-title">{m.pipeline_summary_title()}</h2>
    <span>{m.pipeline_summary_events()}: {format(current.eventCount)}</span>
    <span>{m.pipeline_summary_anomalies()}: {format(current.anomalyCount)}</span>
    <Button size="sm" surface="outline" label={m.pipeline_summary_pin()}
      disabled={current.eventCount === 0} onclick={pin} />
    {#if baseline !== null}
      <Button size="sm" surface="outline" label={m.pipeline_summary_clear()}
        onclick={() => (baseline = null)} />
    {/if}
  </div>
  <div class="capture-labels">
    <span>
      {m.pipeline_summary_current()}: <code>{current.runId ?? m.pipeline_summary_unknown()}</code>
      ({current.eventCount === 0 ? m.pipeline_summary_empty() : current.complete
        ? m.pipeline_summary_complete() : m.pipeline_summary_incomplete()})
    </span>
    {#if baseline !== null}
      <span>
        {m.pipeline_summary_baseline()}: <code>{baseline.runId ?? m.pipeline_summary_unknown()}</code>
        ({baseline.complete ? m.pipeline_summary_complete() : m.pipeline_summary_incomplete()})
      </span>
    {/if}
  </div>
  {#if partial}
    <p class="summary-warning">{m.pipeline_summary_partial()}</p>
  {/if}
  {#if differentScenarios}
    <p class="summary-warning">{m.pipeline_summary_scenario_mismatch({
      current: current.scenarioId ?? "", baseline: baseline?.scenarioId ?? "",
    })}</p>
  {/if}
  <details bind:open={expanded}>
    <summary>{m.pipeline_summary_details()}</summary>
    <p class="summary-note">{m.pipeline_summary_full_buffer()}</p>
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div class="summary-tables" tabindex="0" role="region" aria-label={m.pipeline_summary_details()}>
      <table aria-label={m.pipeline_summary_metric()}>
        <thead><tr>
          <th scope="col">{m.pipeline_summary_metric()}</th>
          <th scope="col">{m.pipeline_summary_current()}</th>
          {#if baseline !== null}
            <th scope="col">{m.pipeline_summary_baseline()}</th>
            <th scope="col">{m.pipeline_summary_delta()}</th>
          {/if}
        </tr></thead>
        <tbody>
          {#each metrics as metric (metric.key)}
            <tr>
              <th scope="row">{metric.label()}</th>
              <td>{format(current[metric.key])}</td>
              {#if baseline !== null}
                <td>{format(baseline[metric.key])}</td>
                <td>{delta(current[metric.key], baseline[metric.key])}</td>
              {/if}
            </tr>
          {/each}
        </tbody>
      </table>
      <table aria-label={m.pipeline_summary_stage()}>
        <thead>
          <tr>
            <th scope="col" rowspan="2">{m.pipeline_summary_stage()}</th>
            <th scope="colgroup" colspan="2">{m.pipeline_summary_current()}</th>
            {#if baseline !== null}
              <th scope="colgroup" colspan="2">{m.pipeline_summary_baseline()}</th>
              <th scope="colgroup" colspan="2">{m.pipeline_summary_delta()}</th>
            {/if}
          </tr>
          <tr>
            <th scope="col">{m.pipeline_summary_events()}</th>
            <th scope="col">{m.pipeline_summary_anomalies()}</th>
            {#if baseline !== null}
              <th scope="col">{m.pipeline_summary_events()}</th>
              <th scope="col">{m.pipeline_summary_anomalies()}</th>
              <th scope="col">{m.pipeline_summary_events()}</th>
              <th scope="col">{m.pipeline_summary_anomalies()}</th>
            {/if}
          </tr>
        </thead>
        <tbody>
          {#each stages as stage (stage.stage)}
            <tr>
              <th scope="row"><code>{stage.stage}</code></th>
              <td>{format(stage.current.events)}</td>
              <td>{format(stage.current.anomalies)}</td>
              {#if stage.baseline !== null}
                <td>{format(stage.baseline.events)}</td>
                <td>{format(stage.baseline.anomalies)}</td>
                <td>{delta(stage.current.events, stage.baseline.events)}</td>
                <td>{delta(stage.current.anomalies, stage.baseline.anomalies)}</td>
              {/if}
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <p class="summary-note">{m.pipeline_summary_metric_notes()}</p>
  </details>
</section>

<style>
  .run-summary {
    padding: var(--space-2) var(--space-4);
    border-bottom: 1px solid var(--border-default);
    min-width: 0;
    flex: none;
    font-size: var(--font-size-xs);
  }
  .summary-heading, .capture-labels {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2) var(--space-3);
  }
  h2 { font-size: var(--font-size-sm); margin: 0; }
  .capture-labels { margin-top: var(--space-1); color: var(--text-secondary); }
  .capture-labels span { overflow-wrap: anywhere; }
  code { font-family: var(--font-mono); }
  details { margin-top: var(--space-2); }
  summary { cursor: pointer; color: var(--text-secondary); }
  .summary-note, .summary-warning { margin: var(--space-1) 0; }
  .summary-note { color: var(--text-muted); }
  .summary-warning { color: var(--text-secondary); }
  .summary-tables { overflow: auto; max-height: 240px; }
  table { width: 100%; border-collapse: collapse; margin: var(--space-2) 0; }
  th, td {
    padding: var(--space-1) var(--space-2);
    border-bottom: 1px solid var(--border-muted);
    text-align: right;
    white-space: nowrap;
  }
  th { font-weight: 500; }
  th:first-child { text-align: left; }
  td { font-variant-numeric: tabular-nums; }
</style>
