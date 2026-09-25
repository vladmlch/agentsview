<!-- ABOUTME: Collapsible assistant turn: header with model, counts,
     timestamp, and token/context summaries; typed child event rows; and an
     always-visible final-output row. -->
<script lang="ts">
  import type { Session } from "../../api/types.js";
  import type { DbTurnTiming as TurnTiming } from "../../api/generated/index.js";
  import type { AssistantTurnItem, TurnEvent } from "../../utils/assistant-turns.js";
  import { enrichSegments, parseContent } from "../../utils/content-parser.js";
  import { formatTimestamp, formatTokenUsage } from "../../utils/format.js";
  import { formatDuration } from "../../utils/duration.js";
  import { formatNumber } from "../../utils/format.js";
  import { sessionTiming } from "../../stores/sessionTiming.svelte.js";
  import { liveTick } from "../../stores/liveTick.svelte.js";
  import { inSessionSearch } from "../../stores/inSessionSearch.svelte.js";
  import { turnCollapse } from "../../stores/turn-collapse.svelte.js";
  import { ui } from "../../stores/ui.svelte.js";
  import { m } from "../../i18n/index.js";
  import AssistantTurnEventRow from "./AssistantTurnEventRow.svelte";
  import AssistantTurnOutput from "./AssistantTurnOutput.svelte";
  import { ChevronRightIcon } from "../../icons.js";

  interface Props {
    turn: AssistantTurnItem;
    session?: Session | null;
    allowMutations?: boolean;
  }

  let { turn, session, allowMutations = true }: Props = $props();

  let defaultExpanded = $derived(!ui.autoCollapseAssistantTurns);
  let expanded = $derived(turnCollapse.isTurnExpanded(turn.key, defaultExpanded));

  /** Whether a child event renders anything under the current block-visibility
   *  filters — the same gating the standalone renderers apply per segment. */
  function isEventVisible(event: TurnEvent): boolean {
    switch (event.kind) {
      case "thinking":
        return inSessionSearch.isBlockEffectivelyVisible("thinking");
      case "tool":
      case "tool-rollup":
        return inSessionSearch.isBlockEffectivelyVisible("tool");
      case "skill":
        return inSessionSearch.isBlockEffectivelyVisible(
          event.message.role === "user" ? "user" : "assistant",
        );
      case "system":
        return true;
      default: {
        const roleVisible = inSessionSearch.isBlockEffectivelyVisible(
          event.message.role === "user" ? "user" : "assistant",
        );
        const codeVisible = inSessionSearch.isBlockEffectivelyVisible("code");
        return (event.segments ?? []).some((segment) =>
          segment.type === "code" ? codeVisible : roleVisible,
        );
      }
    }
  }

  let visibleEvents = $derived(turn.events.filter(isEventVisible));
  let orderedEvents = $derived(
    ui.sortNewestFirst ? [...visibleEvents].reverse() : visibleEvents,
  );
  /** The first rendered row for each source message carries Copy/Pin/Fork. */
  let owningEventKeys = $derived.by(() => {
    const seen = new Set<number>();
    const keys = new Set<string>();
    for (const event of orderedEvents) {
      if (seen.has(event.message.id)) continue;
      seen.add(event.message.id);
      keys.add(event.key);
    }
    return keys;
  });

  /** "Messages" counts rendered source rows: a tool-group rollup counts once
   *  and a message split into thinking/text/tool events still counts once. */
  let messageCount = $derived(
    new Set(visibleEvents.map((event) => event.message.id)).size,
  );
  let toolCallCount = $derived.by(() => {
    let count = 0;
    for (const event of visibleEvents) {
      if (event.kind === "tool") {
        count += event.segments?.length ?? event.toolCalls?.length ?? 0;
      } else if (event.kind === "tool-rollup") {
        for (const toolMessage of event.toolMessages ?? []) {
          count += enrichSegments(
            parseContent(
              toolMessage.content,
              toolMessage.has_tool_use,
              toolMessage.id,
              toolMessage.content_length,
            ),
            toolMessage.tool_calls,
          ).filter((segment) => segment.type === "tool").length;
        }
      }
    }
    return count;
  });

  let outputVisible = $derived(
    turn.finalOutput !== null &&
      inSessionSearch.isBlockEffectivelyVisible("assistant"),
  );
  /** The header is the disclosure for the child rows; when filters hide every
   *  child, the always-visible final output stands alone without it. */
  let headerVisible = $derived(visibleEvents.length > 0);

  /** Context tokens are cumulative per message, so the member maximum is the
   *  turn's peak; output tokens sum across member messages. */
  let tokenSummary = $derived.by(() => {
    let context = 0;
    let output = 0;
    let hasContext = false;
    let hasOutput = false;
    for (const message of turn.messages) {
      context = Math.max(context, message.context_tokens ?? 0);
      output += message.output_tokens ?? 0;
      hasContext ||= (message.has_context_tokens ?? false) || (message.context_tokens ?? 0) > 0;
      hasOutput ||= (message.has_output_tokens ?? false) || (message.output_tokens ?? 0) > 0;
    }
    return formatTokenUsage(context, hasContext, output, hasOutput);
  });

  let memberTurns = $derived.by(() => {
    const byMessage = new Map<number, TurnTiming>();
    for (const turnTiming of sessionTiming.timing?.turns ?? []) {
      byMessage.set(turnTiming.message_id, turnTiming);
    }
    return turn.messages
      .map((message) => byMessage.get(message.id))
      .filter((timing): timing is TurnTiming => timing !== undefined);
  });
  /** Sum of known member-turn durations, or a running label while the last
   *  member turn is still open. Omitted when no timing data exists. */
  let durationLabel = $derived.by(() => {
    if (memberTurns.length === 0) return null;
    const total = memberTurns.reduce((sum, timing) => sum + (timing.duration_ms ?? 0), 0);
    const last = memberTurns[memberTurns.length - 1]!;
    if (sessionTiming.timing?.running && last.duration_ms == null) {
      const startMs = new Date(last.started_at ?? turn.timestamp).getTime();
      const elapsed = Number.isNaN(startMs) ? 0 : Math.max(0, liveTick.now - startMs);
      return m.message_content_running_duration({ duration: formatDuration(elapsed) });
    }
    return total > 0 ? formatDuration(total) : null;
  });
</script>

{#if headerVisible || outputVisible}
  <div class="assistant-turn" data-turn-key={turn.key}>
    {#if headerVisible}
      <button
        class="turn-header"
        aria-expanded={expanded}
        onclick={() => turnCollapse.setTurnExpanded(turn.key, !expanded)}
      >
        <span class="turn-chevron" class:open={expanded}>
          <ChevronRightIcon size="10" strokeWidth="2.4" aria-hidden="true" />
        </span>
        <span class="turn-label">{m.message_content_role_assistant()}</span>
        {#if turn.model}
          <span class="turn-model" title={turn.model}>{turn.model}</span>
        {/if}
        <span class="turn-count">
          {m.assistant_turn_message_count({
            count: messageCount,
            countLabel: formatNumber(messageCount),
          })}
        </span>
        {#if toolCallCount > 0}
          <span class="turn-count">
            {m.assistant_turn_tool_call_count({
              count: toolCallCount,
              countLabel: formatNumber(toolCallCount),
            })}
          </span>
        {/if}
        {#if tokenSummary}<span class="turn-tokens">{tokenSummary}</span>{/if}
        {#if durationLabel}<span class="turn-duration">{durationLabel}</span>{/if}
        <span class="turn-timestamp">{formatTimestamp(turn.timestamp)}</span>
      </button>
      {#if expanded}
        <div class="assistant-turn-events">
          {#each orderedEvents as event (event.key)}
            <AssistantTurnEventRow
              {event}
              ownsSourceActions={owningEventKeys.has(event.key)}
              {session}
              {allowMutations}
            />
          {/each}
        </div>
      {/if}
    {/if}
    {#if outputVisible && turn.finalOutput}
      <AssistantTurnOutput
        event={turn.finalOutput}
        {session}
        {allowMutations}
      />
    {/if}
  </div>
{/if}

<style>
  .assistant-turn {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .turn-header {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 6px 10px;
    text-align: left;
    font-size: 12px;
    color: var(--text-secondary);
    border-radius: var(--radius-sm);
    border-left: 4px solid var(--accent-purple);
    background: var(--assistant-bg);
    transition: background 0.1s;
    min-width: 0;
    user-select: text;
  }
  .turn-header:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }
  .turn-chevron {
    display: inline-flex;
    align-items: center;
    transition: transform 0.15s;
    flex-shrink: 0;
    color: var(--text-muted);
  }
  .turn-chevron.open {
    transform: rotate(90deg);
  }
  .turn-label {
    font-weight: 600;
    letter-spacing: 0.01em;
    color: var(--accent-purple);
    white-space: nowrap;
    flex-shrink: 0;
  }
  .turn-model {
    font-size: 10px;
    color: var(--text-muted);
    padding: 1px 4px;
    border-radius: 3px;
    background: var(--bg-tertiary);
    white-space: nowrap;
    flex-shrink: 0;
    opacity: 0.8;
  }
  .turn-count {
    font-size: 11px;
    color: var(--text-muted);
    white-space: nowrap;
    flex-shrink: 0;
  }
  .turn-tokens,
  .turn-duration {
    font-size: 10px;
    color: var(--text-muted);
    font-family: var(--font-mono);
    white-space: nowrap;
    flex-shrink: 0;
  }
  .turn-timestamp {
    font-size: 12px;
    color: var(--text-muted);
    margin-left: auto;
    flex-shrink: 0;
  }
  .assistant-turn-events {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding-left: 8px;
  }
</style>
