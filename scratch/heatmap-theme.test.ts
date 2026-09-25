// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vite-plus/test";
import { mount, tick, unmount } from "svelte";
import { analytics } from "../../frontend/src/lib/stores/analytics.svelte.js";
import Heatmap from "../../frontend/src/lib/components/analytics/Heatmap.svelte";
import { setThemeMode, getThemeMode, cleanupTheme } from "@kenn-io/kit-ui";

const originalClientWidth = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientWidth");

describe("Heatmap theme reactivity", () => {
  afterEach(() => {
    cleanupTheme();
    setThemeMode("system");
    analytics.heatmap = null;
    document.body.innerHTML = "";
    if (originalClientWidth) {
      Object.defineProperty(HTMLElement.prototype, "clientWidth", originalClientWidth);
    }
  });

  it("updates cell fills when theme toggles", async () => {
    Object.defineProperty(HTMLElement.prototype, "clientWidth", {
      configurable: true,
      get: () => 600,
    });
    analytics.heatmap = {
      metric: "messages",
      entries_from: "2025-08-21",
      levels: { l1: 1, l2: 2, l3: 3, l4: 4 },
      entries: [
        { date: "2025-08-21", value: 3, level: 3 },
        { date: "2025-08-22", value: 3, level: 3 },
      ],
    };

    setThemeMode("light");
    const component = mount(Heatmap, { target: document.body });
    await tick();

    const cells = () =>
      Array.from(document.querySelectorAll<SVGElement>(".heatmap-cell")).map((c) =>
        c.getAttribute("fill"),
      );
    const lightFills = cells();
    console.log("light fills:", JSON.stringify(lightFills));

    setThemeMode("dark");
    await tick();
    await new Promise((r) => setTimeout(r, 0));
    await tick();
    const darkFills = cells();
    console.log("dark fills:", JSON.stringify(darkFills));

    expect(lightFills).not.toEqual(darkFills);
    unmount(component);
  });
});
