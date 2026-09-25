<!-- ABOUTME: Always-visible final output row of an assistant turn. -->
<!-- ABOUTME: Renders the source message's text/code segments in full so the
     answer stays readable while the turn stays collapsed. -->
<script lang="ts">
  import type { Session } from "../../api/types.js";
  import type { TurnEvent } from "../../utils/assistant-turns.js";
  import MessageContent from "./MessageContent.svelte";

  interface Props {
    /** The turn's `finalOutput` message event. */
    event: TurnEvent;
    session?: Session | null;
    allowMutations?: boolean;
  }

  let { event, session, allowMutations = true }: Props = $props();
</script>

<div class="turn-output">
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

<style>
  .turn-output {
    padding: 2px 0;
  }
</style>
