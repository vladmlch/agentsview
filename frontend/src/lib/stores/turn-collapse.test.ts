// @vitest-environment jsdom
import { describe, expect, it, vi } from "vite-plus/test";
import { TurnCollapseStore } from "./turn-collapse.svelte.js";

// Keys follow assistant-turns.ts: turns are `${sessionId}:turn:${firstId}`
// and events are `${sessionId}:${messageId}:${kind}:${index}`. Tool-section
// keys are opaque to the store.
const TURN_A = "s1:turn:11";
const TURN_B = "s1:turn:20";
const EVENT_A = "s1:12:thinking:0";
const EVENT_B = "s1:15:tool-rollup:0";
const TOOL_A = "s1:13:tool:2:output";
const TOOL_B = "s1:14:tool:0:history";

function setup() {
  const store = new TurnCollapseStore();
  store.activateSession("s1");
  return store;
}

describe("TurnCollapseStore", () => {
  describe("default resolution", () => {
    it("returns the caller's default for turns, events, and tool sections", () => {
      const store = setup();
      expect(store.isTurnExpanded(TURN_A, true)).toBe(true);
      expect(store.isTurnExpanded(TURN_A, false)).toBe(false);
      expect(store.isEventExpanded(EVENT_A, true)).toBe(true);
      expect(store.isEventExpanded(EVENT_A, false)).toBe(false);
      expect(store.isToolSectionExpanded(TOOL_A, true)).toBe(true);
      expect(store.isToolSectionExpanded(TOOL_A, false)).toBe(false);
    });

    it("defaults user prompt disclosures to collapsed", () => {
      const store = setup();
      expect(store.isUserPromptExpanded(5)).toBe(false);
    });
  });

  describe("manual overrides", () => {
    it("lets manual turn state win over the caller's default in both directions", () => {
      const store = setup();
      store.setTurnExpanded(TURN_A, false);
      store.setTurnExpanded(TURN_B, true);

      expect(store.isTurnExpanded(TURN_A, true)).toBe(false);
      expect(store.isTurnExpanded(TURN_B, false)).toBe(true);
    });

    it("lets manual event and tool-section state win over the caller's default", () => {
      const store = setup();
      store.setEventExpanded(EVENT_A, false);
      store.setEventExpanded(EVENT_B, true);
      store.setToolSectionExpanded(TOOL_A, false);
      store.setToolSectionExpanded(TOOL_B, true);

      expect(store.isEventExpanded(EVENT_A, true)).toBe(false);
      expect(store.isEventExpanded(EVENT_B, false)).toBe(true);
      expect(store.isToolSectionExpanded(TOOL_A, true)).toBe(false);
      expect(store.isToolSectionExpanded(TOOL_B, false)).toBe(true);
    });

    it("tracks user prompt disclosures per message", () => {
      const store = setup();
      store.setUserPromptExpanded(5, true);

      expect(store.isUserPromptExpanded(5)).toBe(true);
      expect(store.isUserPromptExpanded(6)).toBe(false);

      store.setUserPromptExpanded(5, false);
      expect(store.isUserPromptExpanded(5)).toBe(false);
    });
  });

  describe("bulk baseline", () => {
    it("applies collapse-all to turn, event, and tool keys that were never loaded", () => {
      const store = setup();
      store.collapseAll();

      expect(store.isTurnExpanded("s1:turn:999", true)).toBe(false);
      expect(store.isEventExpanded("s1:999:message:0", true)).toBe(false);
      expect(store.isToolSectionExpanded("s1:999:tool:0:output", true)).toBe(false);
    });

    it("applies expand-all to turn, event, and tool keys that were never loaded", () => {
      const store = setup();
      store.expandAll();

      expect(store.isTurnExpanded("s1:turn:999", false)).toBe(true);
      expect(store.isEventExpanded("s1:999:message:0", false)).toBe(true);
      expect(store.isToolSectionExpanded("s1:999:tool:0:output", false)).toBe(true);
    });

    it("keeps manual exceptions made after a bulk action", () => {
      const store = setup();
      store.collapseAll();
      store.setTurnExpanded(TURN_A, true);
      store.setEventExpanded(EVENT_A, true);
      store.setToolSectionExpanded(TOOL_A, true);

      expect(store.isTurnExpanded(TURN_A, false)).toBe(true);
      expect(store.isEventExpanded(EVENT_A, false)).toBe(true);
      expect(store.isToolSectionExpanded(TOOL_A, false)).toBe(true);
      expect(store.isTurnExpanded(TURN_B, false)).toBe(false);
      expect(store.isEventExpanded(EVENT_B, false)).toBe(false);
      expect(store.isToolSectionExpanded(TOOL_B, false)).toBe(false);
    });

    it("lets a manual event override win over the bulk baseline until the next bulk call", () => {
      const store = setup();
      store.collapseAll();
      store.setEventExpanded(EVENT_A, true);
      expect(store.isEventExpanded(EVENT_A, true)).toBe(true);

      store.collapseAll();
      expect(store.isEventExpanded(EVENT_A, true)).toBe(false);
    });

    it("clears prior exceptions when the next bulk action runs", () => {
      const store = setup();
      store.expandAll();
      store.setTurnExpanded(TURN_A, false);
      store.setEventExpanded(EVENT_A, false);
      store.setToolSectionExpanded(TOOL_A, false);
      expect(store.isTurnExpanded(TURN_A, true)).toBe(false);

      store.expandAll();
      expect(store.isTurnExpanded(TURN_A, false)).toBe(true);
      expect(store.isEventExpanded(EVENT_A, false)).toBe(true);
      expect(store.isToolSectionExpanded(TOOL_A, false)).toBe(true);

      store.setTurnExpanded(TURN_A, true);
      store.collapseAll();
      expect(store.isTurnExpanded(TURN_A, true)).toBe(false);
    });

    it("never changes user prompt disclosures", () => {
      const store = setup();
      store.setUserPromptExpanded(5, true);

      store.collapseAll();
      expect(store.isUserPromptExpanded(5)).toBe(true);
      expect(store.isUserPromptExpanded(6)).toBe(false);

      store.expandAll();
      expect(store.isUserPromptExpanded(5)).toBe(true);
      expect(store.isUserPromptExpanded(6)).toBe(false);
    });

    it("exposes the current bulk baseline direction", () => {
      const store = setup();
      expect(store.bulkBaseline).toBeNull();

      store.collapseAll();
      expect(store.bulkBaseline).toBe("collapse");

      store.expandAll();
      expect(store.bulkBaseline).toBe("expand");

      store.activateSession("s2");
      expect(store.bulkBaseline).toBeNull();
    });
  });

  describe("session scope", () => {
    it("clears bulk baseline, overrides, and prompt disclosures on session change", () => {
      const store = setup();
      store.collapseAll();
      store.setTurnExpanded(TURN_A, true);
      store.setEventExpanded(EVENT_A, true);
      store.setToolSectionExpanded(TOOL_A, true);
      store.setUserPromptExpanded(5, true);

      store.activateSession("s2");

      expect(store.isTurnExpanded(TURN_A, true)).toBe(true);
      expect(store.isEventExpanded(EVENT_A, true)).toBe(true);
      expect(store.isToolSectionExpanded(TOOL_A, true)).toBe(true);
      expect(store.isUserPromptExpanded(5)).toBe(false);
      // The collapse baseline is gone too, so defaults win again.
      expect(store.isTurnExpanded("s2:turn:1", true)).toBe(true);
    });

    it("clears state when the session is cleared", () => {
      const store = setup();
      store.setTurnExpanded(TURN_A, false);
      store.setUserPromptExpanded(5, true);

      store.activateSession(null);

      expect(store.isTurnExpanded(TURN_A, true)).toBe(true);
      expect(store.isUserPromptExpanded(5)).toBe(false);
    });

    it("keeps state when the same session is activated again", () => {
      const store = setup();
      store.collapseAll();
      store.setTurnExpanded(TURN_A, true);

      store.activateSession("s1");

      expect(store.isTurnExpanded(TURN_A, false)).toBe(true);
      expect(store.isTurnExpanded(TURN_B, true)).toBe(false);
    });

    it("clears stray writes made while no session is active", () => {
      const store = new TurnCollapseStore();
      store.collapseAll();
      store.setTurnExpanded(TURN_A, false);
      store.setUserPromptExpanded(5, true);

      store.activateSession(null);

      expect(store.isTurnExpanded(TURN_A, true)).toBe(true);
      expect(store.isUserPromptExpanded(5)).toBe(false);
      expect(store.bulkBaseline).toBeNull();
    });
  });

  describe("key migration", () => {
    it("carries a turn's manual state to the re-anchored key", () => {
      const store = setup();
      store.setTurnExpanded(TURN_A, true);

      store.migrateTurnKey(TURN_A, "s1:turn:7");

      expect(store.isTurnExpanded("s1:turn:7", false)).toBe(true);
      expect(store.isTurnExpanded(TURN_A, false)).toBe(false);
    });

    it("carries a collapsed override across a key change", () => {
      const store = setup();
      store.expandAll();
      store.setTurnExpanded(TURN_A, false);

      store.migrateTurnKey(TURN_A, "s1:turn:7");

      expect(store.isTurnExpanded("s1:turn:7", true)).toBe(false);
      // The migrated key keeps acting like a manual exception under the
      // baseline until the next bulk call clears it.
      expect(store.isTurnExpanded(TURN_B, true)).toBe(true);
    });

    it("is a no-op when the old key has no manual state", () => {
      const store = setup();
      store.migrateTurnKey(TURN_A, "s1:turn:7");

      expect(store.isTurnExpanded("s1:turn:7", true)).toBe(true);
      expect(store.isTurnExpanded("s1:turn:7", false)).toBe(false);
    });

    it("is a no-op when old and new keys are identical", () => {
      const store = setup();
      store.setTurnExpanded(TURN_A, false);

      store.migrateTurnKey(TURN_A, TURN_A);

      expect(store.isTurnExpanded(TURN_A, true)).toBe(false);
    });

    it("overwrites an existing override on the new key", () => {
      const store = setup();
      store.setTurnExpanded(TURN_A, true);
      store.setTurnExpanded("s1:turn:7", false);

      // Last-writer-wins: the migrated state reflects the user's latest
      // intent for the same logical turn.
      store.migrateTurnKey(TURN_A, "s1:turn:7");

      expect(store.isTurnExpanded("s1:turn:7", false)).toBe(true);
      expect(store.isTurnExpanded(TURN_A, false)).toBe(false);
    });
  });

  it("persists nothing to local storage", () => {
    const setItem = vi.spyOn(localStorage, "setItem");
    try {
      const store = setup();
      store.collapseAll();
      store.setTurnExpanded(TURN_A, true);
      store.setEventExpanded(EVENT_A, true);
      store.setToolSectionExpanded(TOOL_A, true);
      store.setUserPromptExpanded(5, true);
      store.migrateTurnKey(TURN_A, "s1:turn:7");
      store.activateSession("s2");

      expect(setItem).not.toHaveBeenCalled();
    } finally {
      setItem.mockRestore();
    }
  });
});
