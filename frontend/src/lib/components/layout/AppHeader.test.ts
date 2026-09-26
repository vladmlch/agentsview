// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from "vite-plus/test";
import { mount, tick, unmount } from "svelte";
const mocks = vi.hoisted(() => ({
  downloadExport: vi.fn().mockResolvedValue(undefined),
  getMarkdownExportUrl: vi.fn().mockReturnValue("/api/v1/sessions/sess-123/md"),
  copyToClipboard: vi.fn().mockResolvedValue(true),
}));

vi.mock("../../api/client.js", () => ({
  downloadExport: mocks.downloadExport,
  getMarkdownExportUrl: mocks.getMarkdownExportUrl,
}));

vi.mock("../../utils/clipboard.js", () => ({
  copyToClipboard: mocks.copyToClipboard,
}));

import { sessions } from "../../stores/sessions.svelte.js";
import { messages } from "../../stores/messages.svelte.js";
import { sync } from "../../stores/sync.svelte.js";
import { settings } from "../../stores/settings.svelte.js";
import { inSessionSearch } from "../../stores/inSessionSearch.svelte.js";
import { turnCollapse } from "../../stores/turn-collapse.svelte.js";
import { ui } from "../../stores/ui.svelte.js";
import { router } from "../../stores/router.svelte.js";
import { setLocale } from "../../i18n/index.js";
import { projectSessionScope } from "../../search/session-scope.js";
import type { AssistantTurnItem } from "../../utils/assistant-turns.js";
import type { Session } from "../../api/types.js";
import type { DbMessage as Message } from "../../api/generated/index.js";

// @ts-ignore
import AppHeader from "./AppHeader.svelte";

function testSession(overrides: Partial<Session> = {}): Session {
  return {
    compaction_count: 0,
    consecutive_failure_max: 0,
    edit_churn_count: 0,
    ended_with_role: "",
    final_failure_streak: 0,
    has_peak_context_tokens: false,
    has_total_output_tokens: false,
    mid_task_compaction_count: 0,
    outcome: "",
    outcome_confidence: "",
    secret_leak_count: 0,
    tool_failure_signal_count: 0,
    tool_retry_count: 0,
    id: "sess-123",
    project: "agentsview",
    machine: "test-machine",
    agent: "codex",
    first_message: "Synthetic test session",
    started_at: "2026-06-13T12:00:00Z",
    ended_at: "2026-06-13T12:05:00Z",
    message_count: 2,
    user_message_count: 1,
    total_output_tokens: 0,
    peak_context_tokens: 0,
    is_automated: false,
    created_at: "2026-06-13T12:00:00Z",
    ...overrides,
  };
}

describe("AppHeader export actions", () => {
  let component: ReturnType<typeof mount> | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    sessions.activeSessionId = "sess-123";
    sessions.sessions = [testSession()];
    sync.serverVersion = null;
    settings.loaded = true;
    settings.readOnly = false;
    settings.error = null;
    ui.isMobileViewport = false;
    ui.sidebarOpen = true;
    ui.followLatest = false;
    router.route = "sessions";
    setLocale("en");
  });

  afterEach(() => {
    if (component) {
      unmount(component);
      component = undefined;
    }
    document.body.innerHTML = "";
    ui.isMobileViewport = false;
    ui.sidebarOpen = true;
    router.route = "sessions";
    settings.loaded = false;
    settings.readOnly = false;
    settings.error = null;
  });

  it("copies markdown export link from export menu", async () => {
    component = mount(AppHeader, { target: document.body });
    await tick();

    const exportButton = document.querySelector<HTMLButtonElement>(
      'button[aria-label="Export session"]',
    );
    expect(exportButton).not.toBeNull();

    exportButton!.click();
    await tick();

    const copyButton = Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find(
      (button) => button.textContent?.includes("Copy markdown export link"),
    );
    expect(copyButton).not.toBeNull();

    copyButton!.click();
    await tick();

    expect(mocks.getMarkdownExportUrl).toHaveBeenCalledWith("sess-123");
    expect(mocks.copyToClipboard).toHaveBeenCalledWith(
      "http://localhost:3000/api/v1/sessions/sess-123/md",
    );
  });

  it("copies active session source path from export menu", async () => {
    sessions.sessions = [
      testSession({
        file_path: "/tmp/agentsview/sessions/session-123.jsonl",
      }),
    ];

    component = mount(AppHeader, { target: document.body });
    await tick();

    const exportButton = document.querySelector<HTMLButtonElement>(
      'button[aria-label="Export session"]',
    );
    expect(exportButton).not.toBeNull();

    exportButton!.click();
    await tick();

    const copyPathButton = Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find(
      (button) => button.textContent?.includes("Copy source file path"),
    );
    expect(copyPathButton).toBeDefined();

    copyPathButton!.click();
    await tick();

    expect(mocks.copyToClipboard).toHaveBeenCalledWith(
      "/tmp/agentsview/sessions/session-123.jsonl",
    );
  });

  it("toggles follow latest from the session header", async () => {
    component = mount(AppHeader, { target: document.body });
    await tick();

    const followButton = document.querySelector<HTMLButtonElement>(
      'button[aria-label="Follow latest messages"]',
    );
    expect(followButton).not.toBeNull();
    expect(followButton!.classList.contains("active")).toBe(false);

    followButton!.click();
    await tick();

    expect(ui.followLatest).toBe(true);
    expect(followButton!.classList.contains("active")).toBe(true);

    followButton!.click();
    await tick();

    expect(ui.followLatest).toBe(false);
    expect(followButton!.classList.contains("active")).toBe(false);
  });

  it("keeps the sidebar toggle out of the desktop title bar", async () => {
    component = mount(AppHeader, { target: document.body });
    await tick();

    const sidebarButton = document.querySelector<HTMLButtonElement>(
      'button[aria-label="Toggle sidebar"]',
    );
    const shortcutsButton = document.querySelector<HTMLButtonElement>(
      'button[aria-label="Keyboard shortcuts"]',
    );

    expect(sidebarButton).toBeNull();
    expect(shortcutsButton).not.toBeNull();
    expect(shortcutsButton?.title).toBe("Keyboard shortcuts (?)");
  });

  it("keeps the mobile hamburger and its drawer behavior", async () => {
    ui.isMobileViewport = true;
    ui.sidebarOpen = true;

    component = mount(AppHeader, { target: document.body });
    await tick();

    const sidebarButton = document.querySelector<HTMLButtonElement>(
      'button[aria-label="Toggle sidebar"]',
    );
    expect(sidebarButton).not.toBeNull();
    expect(sidebarButton?.title).toBe("Toggle sidebar (b)");

    sidebarButton!.click();
    await tick();

    expect(ui.sidebarOpen).toBe(false);
  });

  it("opens the sessions drawer from another mobile route", async () => {
    ui.isMobileViewport = true;
    ui.sidebarOpen = false;
    router.route = "usage";

    component = mount(AppHeader, { target: document.body });
    await tick();

    const sidebarButton = document.querySelector<HTMLButtonElement>(
      'button[aria-label="Toggle sidebar"]',
    );

    expect(sidebarButton).not.toBeNull();
    expect(sidebarButton?.getAttribute("aria-controls")).toBe("session-sidebar");
    expect(sidebarButton?.getAttribute("aria-expanded")).toBe("false");

    sidebarButton!.click();
    await tick();

    expect(router.route).toBe("sessions");
    expect(ui.sidebarOpen).toBe(true);
  });

  it("renders every route as a primary-nav tab", async () => {
    component = mount(AppHeader, { target: document.body });
    await tick();

    const nav = document.querySelector('nav[aria-label="Primary navigation"]');
    expect(nav).not.toBeNull();
    // jsdom measures every width as 0, so TopBar renders the collapsed
    // dropdown; the measurement probe always carries the full tab row.
    const labels = Array.from(
      nav!.querySelectorAll<HTMLElement>(".kit-top-bar__probe .kit-top-bar__tab"),
    ).map((b) => b.textContent?.trim());
    for (const expected of [
      "Sessions",
      "Usage",
      "Activity",
      "Trends",
      "Recall",
      "Pinned",
      "Quality",
      "Trash",
      "Recent Edits",
      "Data",
    ]) {
      expect(labels).toContain(expected);
    }
    expect(labels).not.toContain("Token Usage");
  });

  it("distinguishes global sync from page refresh controls", async () => {
    component = mount(AppHeader, { target: document.body });
    await tick();

    const syncButton = document.querySelector<HTMLButtonElement>(
      'button[aria-label="Sync sessions"]',
    );

    expect(syncButton).not.toBeNull();
    expect(syncButton?.textContent?.trim()).toBe("Sync");
    expect(syncButton?.querySelector("svg.lucide-database-backup")).not.toBeNull();
  });

  it("labels read-only global refresh with the refresh action", async () => {
    sync.serverVersion = {
      api_version: 1,
      data_version: 1,
      insight_generation_available: false,
      version: "dev",
      commit: "unknown",
      build_date: "",
      read_only: true,
    };

    component = mount(AppHeader, { target: document.body });
    await tick();

    const refreshButton = document.querySelector<HTMLButtonElement>(
      'button[aria-label="Refresh data"]',
    );

    expect(refreshButton).not.toBeNull();
    expect(refreshButton?.textContent?.trim()).toBe("Refresh");
    expect(refreshButton?.querySelector("svg.lucide-database-backup")).not.toBeNull();
    expect(document.body.textContent).toContain("Recall");
  });

  it("keeps Recall available when settings report a read-only backend", async () => {
    sync.serverVersion = {
      api_version: 1,
      data_version: 1,
      insight_generation_available: false,
      version: "dev",
      commit: "unknown",
      build_date: "",
      read_only: false,
    };
    settings.readOnly = true;

    component = mount(AppHeader, { target: document.body });
    await tick();

    expect(document.body.textContent).toContain("Sessions");
    expect(document.body.textContent).toContain("Recall");
  });

  it("renders translated shell navigation when locale is Simplified Chinese", async () => {
    setLocale("zh-CN");

    component = mount(AppHeader, { target: document.body });
    await tick();

    expect(
      document.querySelector<HTMLButtonElement>('button[aria-label="同步会话"]'),
    ).not.toBeNull();
    expect(document.body.textContent).toContain("会话");
    expect(document.body.textContent).toContain("用量");
    expect(document.body.textContent).toContain("活动");
  });
});

function headerMessage(
  ordinal: number,
  role: Message["role"],
  content: string,
  overrides: Partial<Message> = {},
): Message {
  return {
    id: 2000 + ordinal,
    session_id: "sess-123",
    ordinal,
    role,
    content,
    content_length: content.length,
    timestamp: `2026-06-13T12:00:${String(ordinal).padStart(2, "0")}Z`,
    has_thinking: false,
    thinking_text: "",
    has_tool_use: false,
    model: "",
    context_tokens: 0,
    output_tokens: 0,
    has_context_tokens: false,
    has_output_tokens: false,
    is_system: false,
    is_sidechain: false,
    ...overrides,
  } as Message;
}

describe("AppHeader transcript controls", () => {
  let component: ReturnType<typeof mount> | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    sessions.activeSessionId = "sess-123";
    sessions.sessions = [testSession()];
    sync.serverVersion = null;
    settings.loaded = true;
    settings.readOnly = false;
    settings.error = null;
    ui.isMobileViewport = false;
    ui.sidebarOpen = true;
    ui.followLatest = false;
    router.route = "sessions";
    setLocale("en");

    messages.clear();
    messages.sessionId = "sess-123";
    messages.loading = false;
    messages.hasOlder = false;
    messages.messages = [
      headerMessage(0, "user", "ask"),
      headerMessage(1, "assistant", "working"),
      headerMessage(2, "assistant", "done"),
    ];
    ui.setTranscriptMode("normal");
    ui.setAutoCollapseAssistantTurns(true);
    ui.showAllBlocks();
    ui.sortNewestFirst = false;
    inSessionSearch.close();
    inSessionSearch.clearQuery();
    turnCollapse.activateSession("sess-123");
  });

  afterEach(() => {
    if (component) {
      unmount(component);
      component = undefined;
    }
    document.body.innerHTML = "";
    inSessionSearch.close();
    inSessionSearch.clearQuery();
    turnCollapse.activateSession(null);
    messages.clear();
    ui.setTranscriptMode("normal");
    ui.setAutoCollapseAssistantTurns(true);
    ui.showAllBlocks();
    ui.isMobileViewport = false;
    ui.sidebarOpen = true;
    router.route = "sessions";
    settings.loaded = false;
  });

  function bulkButton() {
    return document.querySelector<HTMLButtonElement>(".pill-bulk");
  }

  it("toggles session-wide expand/collapse and labels it from row state", async () => {
    component = mount(AppHeader, { target: document.body });
    await tick();

    // Collapsed-by-default turns make the toggle offer expansion.
    expect(bulkButton()).not.toBeNull();
    expect(bulkButton()!.getAttribute("aria-label")).toBe("Expand all");

    bulkButton()!.click();
    await tick();
    expect(turnCollapse.bulkBaseline).toBe("expand");
    expect(bulkButton()!.getAttribute("aria-label")).toBe("Collapse all");

    // A single manually collapsed event flips the label back — the label
    // is derived from every row's effective expansion, not the baseline.
    turnCollapse.setEventExpanded("sess-123:2001:message:0", false);
    await tick();
    expect(bulkButton()!.getAttribute("aria-label")).toBe("Expand all");

    bulkButton()!.click();
    await tick();
    expect(turnCollapse.bulkBaseline).toBe("expand");
    expect(bulkButton()!.getAttribute("aria-label")).toBe("Collapse all");

    bulkButton()!.click();
    await tick();
    expect(turnCollapse.bulkBaseline).toBe("collapse");
    expect(bulkButton()!.getAttribute("aria-label")).toBe("Expand all");
  });

  it("flips the bulk label when a nested tool output drawer folds", async () => {
    messages.messages = [
      headerMessage(0, "user", "ask"),
      headerMessage(1, "assistant", "working", {
        has_tool_use: true,
        tool_calls: [
          {
            category: "",
            tool_name: "Read",
            tool_use_id: "t1",
            result_content: "done",
          },
        ],
      }),
      headerMessage(2, "assistant", "done"),
    ];
    component = mount(AppHeader, { target: document.body });
    await tick();

    bulkButton()!.click();
    await tick();
    expect(bulkButton()!.getAttribute("aria-label")).toBe("Collapse all");

    // Folding one tool block's output drawer — a nested disclosure the
    // label used to ignore — must bring "Expand all" back.
    const scope = projectSessionScope({
      messages: messages.messages,
      sessionId: "sess-123",
    });
    const turn = scope.items.find(
      (item): item is AssistantTurnItem => item.kind === "assistant-turn",
    )!;
    const toolEvent = turn.events.find((event) => event.kind === "tool")!;
    turnCollapse.setToolSectionExpanded(`${toolEvent.key}:output`, false);
    await tick();
    expect(bulkButton()!.getAttribute("aria-label")).toBe("Expand all");
  });

  it("hides the bulk toggle in focused mode", async () => {
    ui.setTranscriptMode("focused");
    component = mount(AppHeader, { target: document.body });
    await tick();

    expect(bulkButton()).toBeNull();
    const focusedPill = document.querySelector<HTMLButtonElement>(
      'button[aria-label="Focused transcript mode"]',
    );
    expect(focusedPill?.classList.contains("active")).toBe(true);
  });

  it("checks search-revealed block types and reports manual hides", async () => {
    messages.messages = [
      headerMessage(0, "user", "ask"),
      headerMessage(1, "assistant", "[Thinking]\nneedle\n[/Thinking]\nworking", {
        has_thinking: true,
      }),
      headerMessage(2, "assistant", "done"),
    ];
    ui.setBlockVisible("thinking", false);
    // Flush between the saved-filter change and opening search, the way a
    // real hide-then-open sequence would land in separate tasks.
    await tick();
    inSessionSearch.isOpen = true;
    inSessionSearch.query = "needle";
    inSessionSearch.debouncedQuery = "needle";

    component = mount(AppHeader, { target: document.body });
    await tick();

    // The hidden type that owns a match is temporarily revealed, and the
    // checkbox displays the effective state.
    expect(inSessionSearch.revealedBlockTypes.has("thinking")).toBe(true);

    const funnel = document.querySelector<HTMLButtonElement>(
      'button[aria-label="Filter block types"]',
    );
    expect(funnel).not.toBeNull();
    funnel!.click();
    await tick();

    const items = () =>
      Array.from(document.querySelectorAll<HTMLButtonElement>(".block-filter-item"));
    const thinkingItem = items().find((button) => button.textContent?.includes("Thinking"));
    expect(thinkingItem).toBeDefined();
    expect(thinkingItem!.classList.contains("active")).toBe(true);

    // Hiding the revealed type suppresses its temporary reveal without
    // changing the saved filter semantics elsewhere.
    thinkingItem!.click();
    await tick();
    expect(inSessionSearch.revealedBlockTypes.has("thinking")).toBe(false);
    expect(ui.isBlockVisible("thinking")).toBe(false);
    expect(thinkingItem!.classList.contains("active")).toBe(false);

    // Show all restores every type and lifts the suppression.
    const showAll = document.querySelector<HTMLButtonElement>(".block-filter-reset");
    expect(showAll).not.toBeNull();
    showAll!.click();
    await tick();
    expect(ui.hasBlockFilters).toBe(false);
    expect(inSessionSearch.isBlockEffectivelyVisible("thinking")).toBe(true);
    for (const item of items()) {
      expect(item.classList.contains("active")).toBe(true);
    }
  });
});
