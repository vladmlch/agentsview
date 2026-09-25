<!-- ABOUTME: Copy, pin, and fork controls for one source message. -->
<!-- ABOUTME: Shared by standalone message headers and assistant-turn event rows. -->
<script lang="ts">
  import type { Session } from "../../api/types.js";
  import type { DbMessage as Message } from "../../api/generated/index.js";
  import { copyToClipboard } from "../../utils/clipboard.js";
  import { formatMessageForCopy } from "../../utils/copy-message.js";
  import { isRemoteConnection } from "../../api/runtime.js";
  import {
    SessionsService,
    type ResumeRequest,
    type ResumeResponse,
  } from "../../api/generated/index";
  import { pins } from "../../stores/pins.svelte.js";
  import { sessions } from "../../stores/sessions.svelte.js";
  import { sync } from "../../stores/sync.svelte.js";
  import { CopyButton } from "@kenn-io/kit-ui";
  import { CirclePlayIcon, PinIcon } from "../../icons.js";
  import { m } from "../../i18n/index.js";

  interface Props {
    message: Message;
    session?: Session | null;
    allowMutations?: boolean;
  }

  let { message, session, allowMutations = true }: Props = $props();

  let copied = $state(false);
  let pinned = $derived(pins.isPinned(message.id));
  let pinFeedback = $state("");
  let forkFeedback = $state("");
  let copyTimer: ReturnType<typeof setTimeout>;
  let pinTimer: ReturnType<typeof setTimeout>;
  let forkTimer: ReturnType<typeof setTimeout>;

  let owningSession = $derived(
    session !== undefined
      ? session
      : (sessions.sessions.find((s) => s.id === message.session_id) ??
        sessions.activeSession),
  );
  let canForkFromMessage = $derived(
    allowMutations &&
      owningSession?.agent === "claude" &&
      !(owningSession?.id ?? "").includes("~") &&
      !(sync.readOnly && isRemoteConnection()),
  );

  async function handleCopy() {
    const ok = await copyToClipboard(formatMessageForCopy(message));
    if (ok) {
      clearTimeout(copyTimer);
      copied = true;
      copyTimer = setTimeout(() => {
        copied = false;
      }, 1500);
    }
  }

  async function handleTogglePin() {
    const wasPinned = pinned;
    try {
      await pins.togglePin(message.session_id, message.id, message.ordinal);
      clearTimeout(pinTimer);
      pinFeedback = wasPinned
        ? m.message_content_unpinned()
        : m.message_content_pinned();
      pinTimer = setTimeout(() => {
        pinFeedback = "";
      }, 1500);
    } catch {
      /* Preserve the existing non-blocking pin interaction. */
    }
  }

  async function handleForkFromHere() {
    if (!canForkFromMessage) return;
    clearTimeout(forkTimer);
    try {
      const resp = (await SessionsService.postApiV1SessionsByIdResume(
        { id: message.session_id },
        {
          ...(sync.readOnly && !isRemoteConnection() ? { command_only: true } : {}),
          from_ordinal: message.ordinal,
          fork_session: true,
        } satisfies ResumeRequest,
      )) as ResumeResponse;
      if (resp.launched) {
        forkFeedback = m.session_breadcrumb_resumed_in({
          target: resp.terminal ?? "terminal",
        });
        forkTimer = setTimeout(() => {
          forkFeedback = "";
        }, 2000);
        return;
      }
      if (resp.command) {
        const ok = await copyToClipboard(resp.command);
        forkFeedback = ok
          ? m.session_breadcrumb_command_copied()
          : m.session_breadcrumb_failed();
        forkTimer = setTimeout(() => {
          forkFeedback = "";
        }, 2000);
        return;
      }
    } catch {
      /* Show the existing failure feedback below. */
    }
    forkFeedback = m.session_breadcrumb_failed();
    forkTimer = setTimeout(() => {
      forkFeedback = "";
    }, 2000);
  }
</script>

<CopyButton
  revealOnHover
  {copied}
  ariaLabel={m.message_content_copy_message()}
  copiedAriaLabel={m.message_content_copied_message()}
  title={m.message_content_copy_message()}
  copiedTitle={m.message_content_copied()}
  onclick={handleCopy}
/>
{#if allowMutations}
  <button
    type="button"
    class="pin-btn"
    class:pinned
    title={pinned ? m.message_content_unpin_message() : m.message_content_pin_message()}
    onclick={handleTogglePin}
  >
    <PinIcon size="14" strokeWidth="1.8" aria-hidden="true" />
  </button>
{/if}
{#if canForkFromMessage}
  <button
    type="button"
    class="pin-btn fork-btn"
    title={m.session_breadcrumb_resume_session()}
    aria-label={m.session_breadcrumb_resume_session()}
    onclick={handleForkFromHere}
  >
    <CirclePlayIcon size="14" strokeWidth="1.8" aria-hidden="true" />
  </button>
{/if}
{#if pinFeedback}<span class="pin-feedback">{pinFeedback}</span>{/if}
{#if forkFeedback}<span class="fork-feedback">{forkFeedback}</span>{/if}

<style>
  .pin-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 26px;
    height: 26px;
    border: none;
    border-radius: var(--radius-sm, 4px);
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    opacity: 0;
    transition:
      opacity 0.15s,
      background 0.15s,
      color 0.15s;
    flex-shrink: 0;
  }
  .pin-btn:focus-visible,
  .pin-btn.pinned {
    opacity: 1;
  }
  @media (hover: none) {
    .pin-btn {
      opacity: 1;
    }
  }
  .pin-btn:hover {
    background: var(--bg-surface-hover);
    color: var(--text-secondary);
  }
  .pin-btn.pinned {
    color: var(--accent-blue);
  }
  .pin-btn:active {
    transform: var(--press-transform);
  }
  .pin-feedback,
  .fork-feedback {
    font-size: 11px;
    color: var(--text-muted);
    animation: fade-in-out 1.5s ease-in-out;
  }
  @keyframes fade-in-out {
    0% {
      opacity: 0;
    }
    15% {
      opacity: 1;
    }
    75% {
      opacity: 1;
    }
    100% {
      opacity: 0;
    }
  }
</style>
