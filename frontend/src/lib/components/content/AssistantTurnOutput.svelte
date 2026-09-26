<!-- ABOUTME: Always-visible final output row of an assistant turn. -->
<!-- ABOUTME: Renders the source message's text/code segments in full so the
     answer stays readable while the turn stays collapsed. Carries the
     message's Copy/Pin/Fork actions when no expanded child row does. -->
<script lang="ts">
  import type { Session } from "../../api/types.js";
  import type { TurnEvent } from "../../utils/assistant-turns.js";
  import MessageContent from "./MessageContent.svelte";
  import MessageSourceActions from "./MessageSourceActions.svelte";

  interface Props {
    /** The turn's `finalOutput` message event. */
    event: TurnEvent;
    /** True when no rendered child event carries this message's
     *  Copy/Pin/Fork controls — the output row shows them instead. */
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
</script>

<div class="turn-output">
  <div class="output-content">
    <MessageContent
      message={event.message}
      eventSegments={event.segments ?? []}
      eventSegmentStart={event.segmentIndex ?? 0}
      hideMessageHeader
      searchOrdinal={event.message.ordinal}
      {session}
      {allowMutations}
    />
  </div>
  {#if ownsSourceActions}
    <div class="output-actions">
      <MessageSourceActions
        message={event.message}
        {session}
        {allowMutations}
      />
    </div>
  {/if}
</div>

<style>
  .turn-output {
    display: flex;
    align-items: flex-start;
    gap: 4px;
    padding: 2px 0;
  }
  .output-content {
    flex: 1;
    min-width: 0;
  }
  .output-actions {
    display: flex;
    align-items: center;
    gap: 2px;
    flex-shrink: 0;
    padding-top: 10px;
    opacity: 0;
    transition: opacity 0.15s;
  }
  .turn-output:hover .output-actions,
  .output-actions:focus-within,
  .output-actions:has(:global(.pin-btn.pinned)) {
    opacity: 1;
  }
  @media (hover: none) {
    .output-actions {
      opacity: 1;
    }
  }
  .turn-output:hover .output-actions :global(.pin-btn),
  .output-actions:focus-within :global(.pin-btn) {
    opacity: 1;
  }
  .turn-output:hover .output-actions :global(.kit-copy-btn),
  .output-actions:focus-within :global(.kit-copy-btn) {
    opacity: 1;
  }
</style>
