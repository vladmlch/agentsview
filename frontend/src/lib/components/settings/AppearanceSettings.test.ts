import { cleanup, fireEvent, render, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { tick } from "svelte";

import AppearanceSettings from "./AppearanceSettings.svelte";
import MessageContent from "../content/MessageContent.svelte";
import { SettingsService } from "../../api/generated/index";
import type { DbMessage as Message } from "../../api/generated/index.js";
import { inSessionSearch } from "../../stores/inSessionSearch.svelte.js";
import { messages } from "../../stores/messages.svelte.js";
import { sessions } from "../../stores/sessions.svelte.js";
import { settings } from "../../stores/settings.svelte.js";
import { sync } from "../../stores/sync.svelte.js";
import { turnCollapse } from "../../stores/turn-collapse.svelte.js";
import { ui } from "../../stores/ui.svelte.js";

vi.mock("../../api/generated/index", async (importOriginal) => {
  const orig = await importOriginal<typeof import("../../api/generated/index")>();
  return {
    ...orig,
    SettingsService: {
      putApiV1Settings: vi.fn(),
    },
  };
});

const settingsService = SettingsService as unknown as {
  putApiV1Settings: ReturnType<typeof vi.fn>;
};
const originalIsDesktop = sync.isDesktop;
const AUTO_COLLAPSE_TURNS_KEY = "agentsview-auto-collapse-turns";

function searchMessage(
  ordinal: number,
  role: Message["role"],
  content: string,
  overrides: Partial<Message> = {},
): Message {
  return {
    id: 9000 + ordinal,
    session_id: "appearance-search",
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
    ...overrides,
  } as Message;
}

describe("AppearanceSettings", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    settingsService.putApiV1Settings.mockResolvedValue({
      agent_dirs: {},
      chart_palette: "agentsview",
      github_configured: false,
      host: "127.0.0.1",
      port: 8080,
      read_only: false,
      require_auth: false,
      terminal: { mode: "auto" },
    });
    settings.chartPalette = "agentsview";
    settings.readOnly = false;
    settings.loaded = true;
    settings.saving = false;
    Object.defineProperty(sync, "isDesktop", {
      value: false,
      writable: true,
      configurable: true,
    });
    inSessionSearch.close();
    inSessionSearch.clearQuery();
    messages.clear();
    sessions.activeSessionId = null;
    ui.showAllBlocks();
    ui.setAutoCollapseAssistantTurns(true);
    turnCollapse.activateSession(null);
  });

  afterEach(() => {
    ui.setZoomLevel(100);
    ui.renderUnknownXmlBlocksAsPreformatted = false;
    if (ui.highContrast) ui.toggleHighContrast();
    inSessionSearch.close();
    inSessionSearch.clearQuery();
    messages.clear();
    sessions.activeSessionId = null;
    ui.showAllBlocks();
    ui.setAutoCollapseAssistantTurns(true);
    turnCollapse.activateSession(null);
    settings.chartPalette = "agentsview";
    settings.readOnly = false;
    settings.loaded = false;
    Object.defineProperty(sync, "isDesktop", {
      value: originalIsDesktop,
      writable: true,
      configurable: true,
    });
    cleanup();
  });

  it.each([false, true])("shares one Zoom selector with desktop=%s", async (isDesktop) => {
    Object.defineProperty(sync, "isDesktop", {
      value: isDesktop,
      writable: true,
      configurable: true,
    });
    ui.setZoomLevel(120);
    const { getByTitle, getByRole, getAllByRole, queryByText } = render(AppearanceSettings);
    expect(getByTitle("Zoom").textContent).toContain("120%");
    expect(getByRole("button", { name: "Zoom 120%" })).toBeTruthy();
    expect(queryByText("Text size")).toBeNull();
    expect(queryByText("Desktop zoom")).toBeNull();
    await fireEvent.click(getByTitle("Zoom"));
    expect(getAllByRole("option").map((option) => option.textContent?.trim())).toEqual([
      "67%",
      "75%",
      "80%",
      "90%",
      "100%",
      "110%",
      "120%",
      "125%",
      "130%",
      "150%",
      "175%",
      "200%",
    ]);
    expect(getByRole("option", { name: "120%" }).getAttribute("aria-selected")).toBe("true");
    await fireEvent.mouseDown(getByRole("option", { name: "150%" }));
    await waitFor(() => expect(getByTitle("Zoom").textContent).toContain("150%"));
    expect(ui.zoomLevel).toBe(150);
    expect(localStorage.getItem("agentsview-zoom-level")).toBe("150");
    ui.zoomOut();
    await waitFor(() => expect(getByTitle("Zoom").textContent).toContain("130%"));
    ui.resetZoom();
    await waitFor(() => expect(getByTitle("Zoom").textContent).toContain("100%"));
  });

  it("keeps local Zoom available when server settings are read-only", async () => {
    settings.readOnly = true;
    const { getByRole, getByTitle } = render(AppearanceSettings);

    expect((getByRole("button", { name: "Zoom 100%" }) as HTMLButtonElement).disabled).toBe(false);
    await fireEvent.click(getByTitle("Zoom"));
    await fireEvent.mouseDown(getByRole("option", { name: "120%" }));
    await waitFor(() => expect(getByTitle("Zoom").textContent).toContain("120%"));
    expect(ui.zoomLevel).toBe(120);
    expect(settingsService.putApiV1Settings).not.toHaveBeenCalled();
  });

  it("keeps Zoom local while another setting saves", async () => {
    const paletteResponse = {
      agent_dirs: {},
      chart_palette: "matplotlib" as const,
      github_configured: false,
      host: "127.0.0.1",
      port: 8080,
      read_only: false,
      require_auth: false,
      terminal: { mode: "auto" as const },
    };
    let finishPalette!: (value: typeof paletteResponse) => void;
    settingsService.putApiV1Settings.mockReturnValueOnce(
      new Promise((resolve) => {
        finishPalette = resolve;
      }),
    );

    const paletteSave = settings.save({ chart_palette: "matplotlib" });
    const { getByRole, getByTitle } = render(AppearanceSettings);
    await fireEvent.click(getByTitle("Zoom"));
    await fireEvent.mouseDown(getByRole("option", { name: "120%" }));

    await waitFor(() => expect(getByTitle("Zoom").textContent).toContain("120%"));
    expect(ui.zoomLevel).toBe(120);
    expect(settingsService.putApiV1Settings).toHaveBeenCalledTimes(1);

    finishPalette(paletteResponse);
    await paletteSave;
    expect(settingsService.putApiV1Settings).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem("agentsview-zoom-level")).toBe("120");
    await waitFor(() => expect(settings.saving).toBe(false));
  });

  it("rejects an unlisted percentage and shows localized empty text", async () => {
    const { getByTitle, getByRole, getByText, queryAllByRole } = render(AppearanceSettings);
    await fireEvent.click(getByTitle("Zoom"));
    const input = getByRole("combobox", { name: "Zoom" });
    await fireEvent.input(input, { target: { value: "133" } });
    expect(getByText("No matches")).toBeTruthy();
    expect(queryAllByRole("option")).toHaveLength(0);
    await fireEvent.keyDown(input, { key: "Enter" });
    expect(ui.zoomLevel).toBe(100);
  });

  it("toggles rendering unknown XML blocks as preformatted text", async () => {
    const { getByRole } = render(AppearanceSettings);
    const checkbox = getByRole("checkbox", {
      name: "Render unknown XML blocks as preformatted text",
    });

    expect((checkbox as HTMLInputElement).checked).toBe(false);
    await fireEvent.click(checkbox);
    expect(ui.renderUnknownXmlBlocksAsPreformatted).toBe(true);
    expect((checkbox as HTMLInputElement).checked).toBe(true);
  });

  it("toggles high contrast", async () => {
    const { getByRole } = render(AppearanceSettings);
    expect(ui.highContrast).toBe(false);
    await fireEvent.click(getByRole("button", { name: "Off" }));
    expect(ui.highContrast).toBe(true);
  });

  it("saves and confirms the selected chart palette", async () => {
    settingsService.putApiV1Settings.mockResolvedValue({
      agent_dirs: {},
      chart_palette: "matplotlib",
      github_configured: false,
      host: "127.0.0.1",
      port: 8080,
      read_only: false,
      require_auth: false,
      terminal: { mode: "auto" },
    });
    const { getByRole } = render(AppearanceSettings);

    await fireEvent.click(getByRole("radio", { name: "Matplotlib" }));

    expect(settingsService.putApiV1Settings).toHaveBeenCalledWith({
      chart_palette: "matplotlib",
    });
    expect(getByRole("radio", { name: "Matplotlib" }).getAttribute("aria-checked")).toBe("true");
  });

  it("disables chart palette controls in read-only mode", () => {
    settings.readOnly = true;
    const { getByRole } = render(AppearanceSettings);

    expect((getByRole("radio", { name: "Agentsview" }) as HTMLButtonElement).disabled).toBe(true);
    expect((getByRole("radio", { name: "Matplotlib" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("persists the assistant-turn collapse preference locally", async () => {
    const { getByRole } = render(AppearanceSettings);
    const checkbox = getByRole("checkbox", {
      name: "Collapse assistant turns by default",
    }) as HTMLInputElement;

    expect(checkbox.checked).toBe(true);
    expect(ui.autoCollapseAssistantTurns).toBe(true);

    await fireEvent.click(checkbox);

    expect(checkbox.checked).toBe(false);
    expect(ui.autoCollapseAssistantTurns).toBe(false);
    await waitFor(() => expect(localStorage.getItem(AUTO_COLLAPSE_TURNS_KEY)).toBe("false"));

    await fireEvent.click(checkbox);
    expect(ui.autoCollapseAssistantTurns).toBe(true);
    await waitFor(() => expect(localStorage.getItem(AUTO_COLLAPSE_TURNS_KEY)).toBe("true"));

    // The preference is browser-local; it never posts to server settings.
    expect(settingsService.putApiV1Settings).not.toHaveBeenCalled();
  });

  it("suppresses a search-revealed block type when its filter hides during search", async () => {
    const thinkingMessage = searchMessage(
      1,
      "assistant",
      "[Thinking]\nneedle\n[/Thinking]\nworking",
      { has_thinking: true },
    );
    messages.sessionId = "appearance-search";
    messages.hasOlder = false;
    messages.loading = false;
    messages.messages = [
      searchMessage(0, "user", "ask"),
      thinkingMessage,
      searchMessage(2, "assistant", "done"),
    ];
    ui.setBlockVisible("thinking", false);
    await tick();
    inSessionSearch.isOpen = true;
    inSessionSearch.query = "needle";
    inSessionSearch.debouncedQuery = "needle";

    const { getByRole } = render(AppearanceSettings);
    render(MessageContent, { props: { message: thinkingMessage } });
    await waitFor(() => expect(inSessionSearch.revealedBlockTypes.has("thinking")).toBe(true));

    // The hidden type is temporarily revealed: content mounts and the
    // checkbox reports effective visibility rather than the saved filter.
    expect(document.querySelector(".thinking-block")).not.toBeNull();
    const thinkingBox = getByRole("checkbox", {
      name: "Thinking blocks",
    }) as HTMLInputElement;
    expect(thinkingBox.checked).toBe(true);

    await fireEvent.click(thinkingBox);

    // The manual hide suppresses the temporary reveal immediately while the
    // saved filter was already hidden and stays hidden.
    await waitFor(() => expect(document.querySelector(".thinking-block")).toBeNull());
    expect(inSessionSearch.revealedBlockTypes.has("thinking")).toBe(false);
    expect(inSessionSearch.isBlockEffectivelyVisible("thinking")).toBe(false);
    expect(ui.isBlockVisible("thinking")).toBe(false);
    expect(thinkingBox.checked).toBe(false);

    // Closing the find view keeps the saved (hidden) choice, not the reveal.
    inSessionSearch.close();
    await tick();
    expect(inSessionSearch.isBlockEffectivelyVisible("thinking")).toBe(false);
    expect(ui.isBlockVisible("thinking")).toBe(false);
    expect(thinkingBox.checked).toBe(false);
  });
});
