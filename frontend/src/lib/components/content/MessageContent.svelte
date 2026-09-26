<script lang="ts">
  import type { Session } from "../../api/types.js";
  import type { DbMessage as Message } from "../../api/generated/index.js";
  import type { DbCallTiming as CallTiming, DbTurnTiming as TurnTiming } from "../../api/generated/index.js";
  import {
    parseContent,
    enrichSegments,
    splitPromptAttachments,
    PROMPT_TRUNCATE_POINTS,
    PROMPT_PREVIEW_POINTS,
    type ContentSegment,
  } from "../../utils/content-parser.js";
  import { formatTimestamp, formatTokenUsage } from "../../utils/format.js";
  import { formatDuration } from "../../utils/duration.js";
  import { sessionAncestryMatches } from "../../utils/session-ancestry.js";
  import { messages as messagesStore } from "../../stores/messages.svelte.js";
  import { sessionTiming } from "../../stores/sessionTiming.svelte.js";
  import { liveTick } from "../../stores/liveTick.svelte.js";
  import ThinkingBlock from "./ThinkingBlock.svelte";
  import ToolBlock from "./ToolBlock.svelte";
  import ParallelGroup from "./ParallelGroup.svelte";
  import CodeBlock from "./CodeBlock.svelte";
  import MermaidBlock from "./MermaidBlock.svelte";
  import SkillBlock from "./SkillBlock.svelte";
  import MessageSourceActions from "./MessageSourceActions.svelte";
  import { Button } from "@kenn-io/kit-ui";
  import { ui } from "../../stores/ui.svelte.js";
  import { sessions } from "../../stores/sessions.svelte.js";
  import { inSessionSearch } from "../../stores/inSessionSearch.svelte.js";
  import { turnCollapse } from "../../stores/turn-collapse.svelte.js";
  import { blockKey } from "../../search/block-text.js";
  import { searchBlock } from "../../search/session-block.svelte.js";
  import { highlightCodeFences } from "../../utils/highlight-fences.js";
  import { loadAssetImages, renderMarkdown } from "../../utils/markdown.js";
  import { displayToolName } from "../../utils/toolDisplay.js";
  import { ChevronDownIcon, ChevronRightIcon } from "../../icons.js";
  import { m } from "../../i18n/index.js";

  interface Props {
    message: Message;
    session?: Session | null;
    isSubagentContext?: boolean;
    searchOrdinal?: number;
    compact?: boolean;
    allowMutations?: boolean;
    /** Subset of the message's enriched segments to render (assistant-turn
     *  event rows). Contiguous in the source, so `eventSegmentStart` recovers
     *  the original search-key indexes. */
    eventSegments?: readonly ContentSegment[];
    /** Index of `eventSegments[0]` in the source message's segment array. */
    eventSegmentStart?: number;
    /** Suppress the message header; the event row owns actions and metadata. */
    hideMessageHeader?: boolean;
  }
  let {
    message,
    session,
    isSubagentContext = false,
    searchOrdinal,
    compact = false,
    allowMutations = true,
    eventSegments,
    eventSegmentStart = 0,
    hideMessageHeader = false,
  }: Props = $props();
  let allSegments = $derived(enrichSegments(
    parseContent(message.content, message.has_tool_use, message.id, message.content_length),
    message.tool_calls,
  ));
  let segments = $derived(eventSegments ?? allSegments);
  // Embedded subagents have their own ordinal namespace and are never searched here.
  let activeSearchOrdinal = $derived(isSubagentContext ? undefined : searchOrdinal);
  let hasSearchQuery = $derived(activeSearchOrdinal !== undefined && inSessionSearch.isActive);
  let isUser = $derived(message.role === "user");
  let mainModel = $derived(!isSubagentContext && messagesStore.sessionId === message.session_id
    ? messagesStore.mainModel : "");
  let offMainModel = $derived.by((): string => {
    if (isUser || !message.model || !mainModel) return "";
    return message.model !== mainModel ? message.model : "";
  });
  let hasContextTokens = $derived(message.has_context_tokens ?? message.context_tokens > 0);
  let hasOutputTokens = $derived(message.has_output_tokens ?? message.output_tokens > 0);
  let tokenSummary = $derived(formatTokenUsage(message.context_tokens, hasContextTokens, message.output_tokens, hasOutputTokens));
  let owningSession = $derived(session !== undefined ? session
    : sessions.sessions.find((s) => s.id === message.session_id) ?? sessions.activeSession);

  const INLINE_TEAMMATE_MESSAGE_RE =
    /<teammate-message\b[^>]*\bteammate_id\s*=\s*(?:"[^"]+"|'[^']+'|[^\s>]+)[^>]*>[\s\S]*?<\/teammate-message\s*>/;
  let hasInlineTeammateMessage = $derived(isUser && !isSubagentContext && segments.some(
    (segment) => segment.type === "text" && INLINE_TEAMMATE_MESSAGE_RE.test(segment.content),
  ));
  let sessionKind = $derived.by((): "teammate" | "subagent" | "user" => {
    const s = owningSession;
    if (!s) return "user";
    const all = sessions.sessions;
    if (sessionAncestryMatches(s, all, (ancestor) => ancestor.relationship_type === "subagent")) return "subagent";
    if (sessionAncestryMatches(s, all, (ancestor) => (ancestor.first_message ?? "").includes("<teammate-message"))) return "teammate";
    return "user";
  });
  let roleLabel = $derived.by(() => {
    if (!isUser) return m.message_content_role_assistant();
    if (isSubagentContext || sessionKind === "subagent") return m.message_content_role_agent();
    if (sessionKind === "teammate" || hasInlineTeammateMessage) return m.message_content_role_teammate();
    return m.message_content_role_user();
  });
  let roleIcon = $derived.by(() => {
    if (!isUser) return "A";
    if (isSubagentContext || sessionKind === "subagent") return "S";
    if (sessionKind === "teammate" || hasInlineTeammateMessage) return "T";
    return "U";
  });
  /** Code fences expanded from their filtered placeholder, keyed by segment index. */
  let expandedCodeBlocks = $state(new Set<number>());

  function toggleCodeBlock(segmentIndex: number) {
    const next = new Set(expandedCodeBlocks);
    if (next.has(segmentIndex)) {
      next.delete(segmentIndex);
    } else {
      next.add(segmentIndex);
    }
    expandedCodeBlocks = next;
  }

  function codeFenceToggleLabel(language: string, expanded: boolean): string {
    if (expanded) {
      return language
        ? m.message_content_code_expanded_with_language({ language })
        : m.message_content_code_expanded();
    }
    return language
      ? m.message_content_code_collapsed_with_language({ language })
      : m.message_content_code_collapsed();
  }

  let showText = $derived(
    inSessionSearch.isBlockEffectivelyVisible(isUser ? "user" : "assistant"),
  );

  let promptPoints = $derived(Array.from(message.content));
  let promptTruncated = $derived(
    isUser && eventSegments === undefined && promptPoints.length > PROMPT_TRUNCATE_POINTS,
  );
  let promptExpanded = $derived(turnCollapse.isUserPromptExpanded(message.id));
  let promptCollapsed = $derived(promptTruncated && !promptExpanded);
  // Attachment references lift out of the collapsed prompt so they keep
  // rendering outside the disclosure; code-aware splitting leaves markers
  // inside code spans and fenced blocks in the body. The preview slices the
  // body after extraction so markers render once, in the attachment region.
  let promptSplit = $derived(splitPromptAttachments(message.content));
  let promptAttachmentText = $derived(promptSplit.attachments.join("\n\n"));
  let promptPreview = $derived(
    Array.from(promptSplit.body).slice(0, PROMPT_PREVIEW_POINTS).join("").trimEnd(),
  );
  let promptSearchKey = $derived.by(() => {
    if (activeSearchOrdinal === undefined) return undefined;
    const textIndex = segments.findIndex((segment) => segment.type === "text");
    if (textIndex < 0) return undefined;
    return blockKey(activeSearchOrdinal, "text", textIndex);
  });
  let accentColor = $derived(isUser ? "var(--accent-blue)" : "var(--accent-purple)");
  let accentForeground = $derived(isUser ? "var(--accent-blue-foreground)" : "var(--accent-purple-foreground)");
  let roleBg = $derived(isUser ? "var(--user-bg)" : "var(--assistant-bg)");
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
  function soloDurationLabel(
    ct: CallTiming | undefined,
    turn: TurnTiming | undefined,
    msg: Message,
  ): string | undefined {
    if (ct?.duration_ms != null) return formatDuration(ct.duration_ms);
    if (sessionTiming.timing?.running && turn != null && turn.duration_ms == null) {
      const startMs = new Date(turn.started_at ?? msg.timestamp).getTime();
      const elapsed = Number.isNaN(startMs) ? 0 : Math.max(0, liveTick.now - startMs);
      return m.message_content_running_duration({ duration: formatDuration(elapsed) });
    }
    return undefined;
  }
  function isRunningTurn(msg: Message): boolean {
    if (!sessionTiming.timing?.running) return false;
    const turn = turnByMessage.get(msg.id);
    return turn != null && turn.duration_ms == null;
  }
  let turnSummary = $derived.by(() => {
    if (isUser || !message.has_tool_use) return null;
    const calls = message.tool_calls?.length ?? 0;
    const turn = turnByMessage.get(message.id);
    if (turn?.duration_ms != null) {
      return { text: m.message_content_turn_summary({ duration: formatDuration(turn.duration_ms), count: calls }), slow: false, running: false };
    }
    if (sessionTiming.timing?.running && turn != null) {
      const startMs = new Date(turn.started_at ?? message.timestamp).getTime();
      const elapsed = Number.isNaN(startMs) ? 0 : Math.max(0, liveTick.now - startMs);
      return { text: m.message_content_running_turn_summary({ duration: formatDuration(elapsed), count: calls }), slow: false, running: true };
    }
    return null;
  });
</script>

<div class="message" class:is-user={isUser} class:compact style:border-left-color={accentColor} style:background={roleBg}>
  {#if !hideMessageHeader}
    <div class="message-header">
      <span class="role-icon" style:background={accentColor} style:color={accentForeground}>{roleIcon}</span>
      <span class="role-label" style:color={accentColor}>{roleLabel}</span>
      <MessageSourceActions {message} {session} {allowMutations} />
      <div class="header-meta">
        {#if tokenSummary}<span class="message-tokens">{tokenSummary}</span>{/if}
        {#if turnSummary}<span class="turn-summary" class:slow={turnSummary.slow} class:running={turnSummary.running}>{turnSummary.text}</span>{/if}
        <span class="timestamp">{formatTimestamp(message.timestamp)}</span>
        {#if offMainModel}<span class="message-model" title={offMainModel}>{offMainModel}</span>{/if}
      </div>
    </div>
  {/if}
  <div class="message-body">
    {#if promptCollapsed && showText}
      <div class="text-content markdown" {@attach searchBlock(promptSearchKey)}
        use:highlightCodeFences={{ content: promptPreview }} use:loadAssetImages={promptPreview}>
        {@html renderMarkdown(promptPreview, { renderUnknownXmlBlocksAsPreformatted: ui.renderUnknownXmlBlocksAsPreformatted })}
      </div>
      {#if promptAttachmentText}
        <div class="prompt-attachments markdown" use:loadAssetImages={promptAttachmentText}>
          {@html renderMarkdown(promptAttachmentText, { renderUnknownXmlBlocksAsPreformatted: ui.renderUnknownXmlBlocksAsPreformatted })}
        </div>
      {/if}
    {/if}
    <!-- A collapsed prompt renders only its preview, attachments, and the
      disclosure toggle; every parsed segment stays folded behind it. -->
    {#if !promptCollapsed}
    {#each segments as segment, segmentIndex}
      <!-- `sourceIndex` recovers the segment's index in the source message so
        search keys stay stable when `eventSegments` carries a subset. -->
      {@const sourceIndex = eventSegments === undefined ? segmentIndex : eventSegmentStart + segmentIndex}
      {@const searchKey = activeSearchOrdinal === undefined || segment.type === "tool"
        ? undefined : blockKey(activeSearchOrdinal, segment.type, sourceIndex)}
      {#if segment.type === "thinking"}
        {#if inSessionSearch.isBlockEffectivelyVisible("thinking")}
          <ThinkingBlock content={segment.content} {searchKey} />
        {/if}
      {:else if segment.type === "tool"}
        <!-- Structured and legacy tool calls are rendered after prose. -->
      {:else if segment.type === "code"}
        {@const codeLabel = segment.label?.trim().toLowerCase()}
        {@const language = segment.label?.trim() ?? ""}
        {#if inSessionSearch.isBlockEffectivelyVisible("code")}
          {#if codeLabel === "mermaid" && !hasSearchQuery}
            <MermaidBlock content={segment.content} />
          {:else}
            <CodeBlock content={segment.content} language={segment.label} {searchKey} />
          {/if}
        {:else}
          {@const expanded = expandedCodeBlocks.has(sourceIndex)}
          {@const toggleLabel = codeFenceToggleLabel(language, expanded)}
          <div class="code-fence-block">
            <Button
              class="code-fence-toggle"
              size="sm"
              surface="soft"
              ariaExpanded={expanded}
              label={toggleLabel}
              title={toggleLabel}
              onclick={() => toggleCodeBlock(sourceIndex)}
            >
              {#snippet trailing()}
                {#if expanded}
                  <ChevronDownIcon size="14" strokeWidth="2" aria-hidden="true" />
                {:else}
                  <ChevronRightIcon size="14" strokeWidth="2" aria-hidden="true" />
                {/if}
              {/snippet}
            </Button>
            {#if expanded}
              {#if codeLabel === "mermaid"}
                <MermaidBlock content={segment.content} />
              {:else}
                <CodeBlock
                  content={segment.content}
                  language={segment.label}
                />
              {/if}
            {/if}
          </div>
        {/if}
      {:else if segment.type === "skill"}
        {#if showText}<SkillBlock content={segment.content} name={segment.label} {searchKey} />{/if}
      {:else}
        {#if showText}
          <div class="text-content markdown" {@attach searchBlock(searchKey)}
            use:highlightCodeFences={{ content: segment.content }} use:loadAssetImages={segment.content}>
            {@html renderMarkdown(segment.content, { renderUnknownXmlBlocksAsPreformatted: ui.renderUnknownXmlBlocksAsPreformatted })}
          </div>
        {/if}
      {/if}
    {/each}
    {/if}
    {#if promptTruncated && showText}
      <Button
        class="prompt-toggle"
        size="sm"
        surface="soft"
        ariaExpanded={promptExpanded}
        label={promptExpanded ? m.message_content_show_less() : m.message_content_show_full_prompt()}
        title={promptExpanded ? m.message_content_show_less() : m.message_content_show_full_prompt()}
        onclick={() => turnCollapse.setUserPromptExpanded(message.id, !promptExpanded)}
      >
        {#snippet trailing()}
          {#if promptExpanded}
            <ChevronDownIcon size="14" strokeWidth="2" aria-hidden="true" />
          {:else}
            <ChevronRightIcon size="14" strokeWidth="2" aria-hidden="true" />
          {/if}
        {/snippet}
      </Button>
    {/if}
    <!-- Tool segments and structured calls render through the trailing tool
      block in standalone mode; event rows render their own tool events, so
      a segment subset must not re-emit them here. A collapsed prompt folds
      them behind the disclosure too. -->
    {#if !promptCollapsed && eventSegments === undefined && inSessionSearch.isBlockEffectivelyVisible("tool")}
      {@const turn = turnByMessage.get(message.id)}
      {@const structuredCalls = message.tool_calls ?? []}
      {#if structuredCalls.length === 1}
        {@const soloCall = structuredCalls[0]!}
        <ToolBlock toolCall={soloCall} content="" label={displayToolName(soloCall)}
          durationLabel={soloDurationLabel(
            callByToolUseID.get(soloCall.tool_use_id ?? ""),
            turn,
            message,
          )}
          isRunning={isRunningTurn(message)}
          searchScope={activeSearchOrdinal === undefined ? undefined : { ordinal: activeSearchOrdinal, callIdx: 0 }} />
      {:else if structuredCalls.length >= 2}
        <ParallelGroup toolCalls={structuredCalls} callTimingByID={callByToolUseID}
          isRunning={isRunningTurn(message)} searchOrdinal={activeSearchOrdinal} />
      {:else}
        {#each segments.filter((s) => s.type === "tool") as seg, segIdx (`${message.id}-${segIdx}`)}
          <ToolBlock content={seg.content} label={seg.label} toolCall={seg.toolCall}
            searchScope={activeSearchOrdinal === undefined ? undefined : { ordinal: activeSearchOrdinal, callIdx: `seg${segIdx}` }} />
        {/each}
      {/if}
    {/if}
  </div>
</div>

<style>
  .message { border-left: 4px solid; padding: 14px 20px; border-radius: 0 var(--radius-md) var(--radius-md) 0; }
  .message-header { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
  .role-icon {
    width: 22px; height: 22px; border-radius: 50%; display: flex; align-items: center;
    justify-content: center; font-size: 11px; font-weight: 700; color: white; flex-shrink: 0; line-height: 1;
  }
  .role-label { font-size: 13px; font-weight: 600; letter-spacing: 0.01em; }
  .timestamp { font-size: 12px; color: var(--text-muted); }
  .header-meta { margin-left: auto; display: flex; align-items: center; gap: 8px; min-width: 0; }
  .message-tokens { font-size: 10px; color: var(--text-muted); font-family: var(--font-mono); white-space: nowrap; }
  .message-model {
    font-size: 10px; color: var(--text-muted); padding: 1px 4px; border-radius: 3px;
    background: var(--bg-tertiary); white-space: nowrap; flex-shrink: 0; opacity: 0.8;
  }
  .turn-summary {
    font-family: var(--font-mono); font-size: 10px; color: var(--text-muted);
    background: color-mix(in srgb, var(--text-primary) 4%, transparent); padding: 2px 8px;
    border-radius: var(--radius-sm); border: 1px solid color-mix(in srgb, var(--text-primary) 4%, transparent);
    white-space: nowrap; flex-shrink: 0;
  }
  .turn-summary.slow { color: var(--slow-fg); background: var(--slow-bg); border-color: var(--slow-ring); }
  .turn-summary.running { color: var(--running-fg); background: var(--running-bg); border-color: var(--running-ring); animation: duration-pulse 1.6s ease-in-out infinite; }
  /* The extracted source actions hide their pin/fork buttons until hover;
   * the reveal trigger still covers the whole message row. */
  .message:hover :global(.kit-copy-btn),
  .message:hover :global(.pin-btn) { opacity: 1; }
  .text-content { font-size: 14px; line-height: 1.7; color: var(--text-primary); word-wrap: break-word; }
  .code-fence-block {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  :global(.code-fence-toggle) {
    align-self: flex-start;
    max-width: 100%;
  }

  .message-body { display: flex; flex-direction: column; gap: 8px; }
  .prompt-attachments {
    font-size: 13px;
    color: var(--text-secondary);
    border-left: 2px solid var(--border-muted);
    padding-left: 10px;
  }
  .prompt-attachments :global(img) { max-width: 240px; display: block; }
  :global(.prompt-toggle) { align-self: flex-start; max-width: 100%; }
  .markdown :global(p) { margin: 0.5em 0; }
  .markdown :global(p:first-child) { margin-top: 0; }
  .markdown :global(p:last-child) { margin-bottom: 0; }
  .markdown :global(h1), .markdown :global(h2), .markdown :global(h3),
  .markdown :global(h4), .markdown :global(h5), .markdown :global(h6) { margin: 0.8em 0 0.4em; line-height: 1.3; font-weight: 600; }
  .markdown :global(h1) { font-size: 1.35em; }
  .markdown :global(h2) { font-size: 1.2em; }
  .markdown :global(h3) { font-size: 1.1em; }
  .markdown :global(h4), .markdown :global(h5), .markdown :global(h6) { font-size: 1em; }
  .markdown :global(a) { color: var(--accent-blue); text-decoration: none; }
  .markdown :global(a:hover) { text-decoration: underline; }
  .markdown :global(code) {
    font-family: var(--font-mono); font-size: 0.85em; background: var(--bg-inset);
    border: 1px solid var(--border-muted); border-radius: 4px; padding: 0.15em 0.4em;
  }
  .markdown :global(pre:not(.unknown-xml-block)) { background: var(--code-bg); color: var(--code-text); border-radius: var(--radius-md); padding: 12px 16px; overflow-x: auto; margin: 0.5em 0; }
  .markdown :global(pre code) { background: none; border: none; padding: 0; font-size: 13px; color: inherit; }
  .markdown :global(blockquote) { border-left: 3px solid var(--border-default); margin: 0.5em 0; padding: 0.3em 1em; color: var(--text-secondary); }
  .markdown :global(ul), .markdown :global(ol) { padding-left: 1.6em; margin: 0.5em 0; }
  .markdown :global(li) { margin: 0.2em 0; line-height: 1.65; }
  .markdown :global(hr) { border: none; border-top: 1px solid var(--border-muted); margin: 0.8em 0; }
  .markdown :global(table) { border-collapse: collapse; margin: 0.5em 0; width: auto; font-size: 13px; }
  .markdown :global(th), .markdown :global(td) { border: 1px solid var(--border-muted); padding: 6px 10px; text-align: left; }
  .markdown :global(th) { background: var(--bg-inset); font-weight: 600; }
  .markdown :global(img) { max-width: 100%; border-radius: var(--radius-sm); }
  .markdown :global(strong) { font-weight: 600; }
  .message.compact { padding: 9px 10px; }
  .compact .message-header { gap: 6px; margin-bottom: 6px; }
  .compact .role-icon { width: 18px; height: 18px; font-size: 10px; }
  .compact .role-label { font-size: 11px; }
  .compact .timestamp { font-size: 10px; }
  .compact .text-content { font-size: 12px; line-height: 1.55; }
</style>
