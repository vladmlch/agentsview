<!-- ABOUTME: Renders one typed child event of an assistant turn. -->
<!-- ABOUTME: Message events own a collapsed preview disclosure; thinking,
     skill, tool, rollup, and system events reuse their existing components
     with turn-scoped disclosure keys. -->
<script lang="ts">
  import type { Session } from "../../api/types.js";
  import type {
    DbCallTiming as CallTiming,
    DbTurnTiming as TurnTiming,
  } from "../../api/generated/index.js";
  import type { TurnEvent } from "../../utils/assistant-turns.js";
  import { blockKey } from "../../search/block-text.js";
  import { inSessionSearch } from "../../stores/inSessionSearch.svelte.js";
  import { turnCollapse } from "../../stores/turn-collapse.svelte.js";
  import { sessionTiming } from "../../stores/sessionTiming.svelte.js";
  import { liveTick } from "../../stores/liveTick.svelte.js";
  import { ui } from "../../stores/ui.svelte.js";
  import { formatDuration } from "../../utils/duration.js";
  import { displayToolName } from "../../utils/toolDisplay.js";
  import { m } from "../../i18n/index.js";
  import MessageContent from "./MessageContent.svelte";
  import MessageSourceActions from "./MessageSourceActions.svelte";
  import ThinkingBlock from "./ThinkingBlock.svelte";
  import SkillBlock from "./SkillBlock.svelte";
  import ToolBlock from "./ToolBlock.svelte";
  import ParallelGroup from "./ParallelGroup.svelte";
  import ToolCallGroup from "./ToolCallGroup.svelte";
  import SystemBoundaryCard from "../system/SystemBoundaryCard.svelte";
  import SearchMatchCount from "./SearchMatchCount.svelte";
  import { ChevronRightIcon } from "../../icons.js";

  interface Props {
    event: TurnEvent;
    /** True when this row is the first rendered event of its source
     *  message and therefore carries the Copy/Pin/Fork controls. */
    ownsSourceActions?: boolean;
    session?: Session | null;
    allowMutations?: boolean;
  }

  let {
    event,
    ownsSourceActions = false,
    session,
    allowMutations = true,
  }: Props = $props();

  const PREVIEW_MAX = 80;

  /** First non-empty line of `content`, truncated at 80 Unicode code
   *  points — an emoji counts as one point and surrogate pairs are never
   *  split. */
  function firstLinePreview(content: string): string {
    for (const line of content.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      const points = Array.from(trimmed);
      return points.length > PREVIEW_MAX
        ? `${points.slice(0, PREVIEW_MAX).join("")}…`
        : trimmed;
    }
    return "";
  }

  let defaultExpanded = $derived(!ui.autoCollapseAssistantTurns);
  let expanded = $derived(
    turnCollapse.isEventExpanded(event.key, defaultExpanded),
  );

  let eventSegments = $derived(event.segments ?? []);
  let roleType = $derived(event.message.role === "user" ? "user" : "assistant");
  let preview = $derived.by(() => {
    if (event.kind !== "message") return "";
    // When the role filter hides assistant/user text, fall back to the code
    // fence label so the preview never surfaces filtered-out prose.
    const roleVisible = inSessionSearch.isBlockEffectivelyVisible(roleType);
    for (const segment of eventSegments) {
      if (segment.type === "text" && roleVisible) {
        const line = firstLinePreview(segment.content);
        if (line) return line;
      }
      if (segment.type === "code" && segment.label?.trim()) {
        return `\`\`\`${segment.label.trim()}`;
      }
    }
    return "";
  });
  let matchCount = $derived.by(() => {
    if (event.kind !== "message" || event.segmentIndex === undefined) return 0;
    return eventSegments.reduce(
      (count, segment, index) =>
        count +
        inSessionSearch.countForBlock(
          blockKey(event.message.ordinal, segment.type, event.segmentIndex! + index),
        ),
      0,
    );
  });

  let thinkingEvent = $derived(
    event.kind === "thinking" ? eventSegments[0] : undefined,
  );
  let skillEvent = $derived(event.kind === "skill" ? eventSegments[0] : undefined);
  let toolCalls = $derived(event.kind === "tool" ? (event.toolCalls ?? []) : []);
  let legacyToolSegments = $derived(
    event.kind === "tool" && toolCalls.length === 0
      ? eventSegments.filter((segment) => segment.type === "tool")
      : [],
  );

  let turnByMessage = $derived.by(() => {
    const map = new Map<number, TurnTiming>();
    for (const turn of sessionTiming.timing?.turns ?? []) map.set(turn.message_id, turn);
    return map;
  });
  let callByToolUseID = $derived.by(() => {
    const map = new Map<string, CallTiming>();
    for (const turn of sessionTiming.timing?.turns ?? []) {
      for (const call of turn.calls) map.set(call.tool_use_id, call);
    }
    return map;
  });
  let toolTurn = $derived(turnByMessage.get(event.message.id));
  function soloDurationLabel(callId: string | undefined): string | undefined {
    const ct = callByToolUseID.get(callId ?? "");
    if (ct?.duration_ms != null) return formatDuration(ct.duration_ms);
    if (sessionTiming.timing?.running && toolTurn != null && toolTurn.duration_ms == null) {
      const startMs = new Date(toolTurn.started_at).getTime();
      const elapsed = Number.isNaN(startMs) ? 0 : Math.max(0, liveTick.now - startMs);
      return m.message_content_running_duration({ duration: formatDuration(elapsed) });
    }
    return undefined;
  }
  let isRunningTurn = $derived(
    sessionTiming.timing?.running === true &&
      toolTurn != null &&
      toolTurn.duration_ms == null,
  );
</script>

<div class="turn-event" data-event-kind={event.kind}>
  <div class="event-content">
    {#if event.kind === "message"}
      <button
        class="event-toggle"
        aria-expanded={expanded}
        onclick={() => {
          const sel = window.getSelection();
          if (sel && sel.toString().length > 0) return;
          turnCollapse.setEventExpanded(event.key, !expanded);
        }}
      >
        <span class="event-chevron" class:open={expanded}>
          <ChevronRightIcon size="10" strokeWidth="2.4" aria-hidden="true" />
        </span>
        <span class="event-label">{m.assistant_turn_event_message()}</span>
        <SearchMatchCount count={matchCount} />
        {#if !expanded && preview}
          <span class="event-preview">{preview}</span>
        {/if}
      </button>
      {#if expanded}
        <div class="event-body">
          <MessageContent
            message={event.message}
            {eventSegments}
            eventSegmentStart={event.segmentIndex ?? 0}
            hideMessageHeader
            searchOrdinal={event.message.ordinal}
            {session}
            {allowMutations}
          />
        </div>
      {/if}
    {:else if event.kind === "thinking" && thinkingEvent}
      <ThinkingBlock
        content={thinkingEvent.content}
        searchKey={blockKey(event.message.ordinal, "thinking", event.segmentIndex ?? 0)}
        collapseKey={event.key}
        {defaultExpanded}
      />
    {:else if event.kind === "skill" && skillEvent}
      <SkillBlock
        content={skillEvent.content}
        name={skillEvent.label}
        searchKey={blockKey(event.message.ordinal, "skill", event.segmentIndex ?? 0)}
        collapseKey={event.key}
        {defaultExpanded}
      />
    {:else if event.kind === "tool"}
      {#if toolCalls.length === 1}
        {@const soloCall = toolCalls[0]!}
        <ToolBlock
          toolCall={soloCall}
          content=""
          label={displayToolName(soloCall)}
          durationLabel={soloDurationLabel(soloCall.tool_use_id)}
          isRunning={isRunningTurn}
          searchScope={{ ordinal: event.message.ordinal, callIdx: 0 }}
          collapseKey={event.key}
          {defaultExpanded}
        />
      {:else if toolCalls.length > 1}
        <ParallelGroup
          {toolCalls}
          callTimingByID={callByToolUseID}
          isRunning={isRunningTurn}
          searchOrdinal={event.message.ordinal}
          collapseKeyPrefix={event.key}
          {defaultExpanded}
        />
      {:else}
        {#each legacyToolSegments as segment, index (index)}
          <ToolBlock
            content={segment.content}
            label={segment.label}
            toolCall={segment.toolCall}
            searchScope={{ ordinal: event.message.ordinal, callIdx: `seg${index}` }}
            collapseKey={`${event.key}:seg${index}`}
            {defaultExpanded}
          />
        {/each}
      {/if}
    {:else if event.kind === "tool-rollup" && event.toolMessages}
      <ToolCallGroup
        messages={event.toolMessages}
        timestamp={event.message.timestamp}
        searchable
        sortNewestFirst={ui.sortNewestFirst}
        collapseKeyPrefix={event.key}
        {defaultExpanded}
      />
    {:else if event.kind === "system"}
      <SystemBoundaryCard
        subtype={event.label ?? event.message.source_subtype ?? "system"}
        content={event.message.content}
        timestamp={event.message.timestamp}
      />
    {:else}
      <!-- Future/unknown kinds render as the source message rather than
        vanishing; keeps storage-compatible events visible. -->
      <MessageContent
        message={event.message}
        {eventSegments}
        eventSegmentStart={event.segmentIndex ?? 0}
        hideMessageHeader
        searchOrdinal={event.message.ordinal}
        {session}
        {allowMutations}
      />
    {/if}
  </div>
  {#if ownsSourceActions}
    <div class="event-actions">
      <MessageSourceActions
        message={event.message}
        {session}
        {allowMutations}
      />
    </div>
  {/if}
</div>

<style>
  .turn-event {
    display: flex;
    align-items: flex-start;
    gap: 4px;
  }
  .event-content {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .event-toggle {
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    padding: 4px 8px;
    text-align: left;
    font-size: 12px;
    color: var(--text-secondary);
    border-radius: var(--radius-sm);
    transition: background 0.1s;
    min-width: 0;
    user-select: text;
  }
  .event-toggle:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }
  .event-chevron {
    display: inline-flex;
    align-items: center;
    transition: transform 0.15s;
    flex-shrink: 0;
    color: var(--text-muted);
  }
  .event-chevron.open {
    transform: rotate(90deg);
  }
  .event-label {
    font-weight: 600;
    letter-spacing: 0.01em;
    white-space: nowrap;
    flex-shrink: 0;
  }
  .event-preview {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }
  .event-actions {
    display: flex;
    align-items: center;
    gap: 2px;
    flex-shrink: 0;
    padding-top: 2px;
    opacity: 0;
    transition: opacity 0.15s;
  }
  .turn-event:hover .event-actions,
  .event-actions:focus-within,
  .event-actions:has(:global(.pin-btn.pinned)) {
    opacity: 1;
  }
  @media (hover: none) {
    .event-actions {
      opacity: 1;
    }
  }
  .turn-event:hover .event-actions :global(.pin-btn),
  .event-actions:focus-within :global(.pin-btn) {
    opacity: 1;
  }
  .turn-event:hover .event-actions :global(.kit-copy-btn),
  .event-actions:focus-within :global(.kit-copy-btn) {
    opacity: 1;
  }
</style>
