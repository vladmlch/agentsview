<!-- ABOUTME: Collapsible assistant turn: header with model, counts,
     timestamp, and token/context summaries; typed child event rows; and an
     always-visible final-output row. -->
<script lang="ts">
  import type { Session } from "../../api/types.js";
  import type { AssistantTurnItem, TurnEvent } from "../../utils/assistant-turns.js";
  import { inSessionSearch } from "../../stores/inSessionSearch.svelte.js";
  import { isTurnEventVisible } from "../../utils/turn-visibility.js";
  import { turnCollapse } from "../../stores/turn-collapse.svelte.js";
  import { ui } from "../../stores/ui.svelte.js";
  import AssistantTurnHeader from "./AssistantTurnHeader.svelte";
  import AssistantTurnEventRow from "./AssistantTurnEventRow.svelte";
  import AssistantTurnOutput from "./AssistantTurnOutput.svelte";

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
    return isTurnEventVisible(event, (type) =>
      inSessionSearch.isBlockEffectivelyVisible(type),
    );
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

  let outputVisible = $derived(
    turn.finalOutput !== null && isEventVisible(turn.finalOutput),
  );
  /** Copy/Pin/Fork land on the first rendered child row of each source
   *  message. A collapsed turn mounts no child rows, so the always-visible
   *  output row carries its own message's actions then; while expanded the
   *  child row keeps them. */
  let outputOwnsSourceActions = $derived.by(() => {
    const output = turn.finalOutput;
    if (output === null) return false;
    if (!expanded) return true;
    return !orderedEvents.some((event) => event.message.id === output.message.id);
  });
  /** The header is the disclosure for the child rows; it renders whenever
   *  at least one child event survives the block filters. */
  let headerVisible = $derived(visibleEvents.length > 0);
</script>

{#if headerVisible || outputVisible}
  <div class="assistant-turn" data-turn-key={turn.key}>
    {#if headerVisible}
      <AssistantTurnHeader {turn} />
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
        ownsSourceActions={outputOwnsSourceActions}
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
  .assistant-turn-events {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding-left: 8px;
  }
</style>
