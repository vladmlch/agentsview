// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it } from "vite-plus/test";
import { mount, tick, unmount } from "svelte";
import { setLocale } from "../../i18n/index.js";
import { turnCollapse } from "../../stores/turn-collapse.svelte.js";
import ThinkingBlock from "./ThinkingBlock.svelte";

const components: ReturnType<typeof mount>[] = [];
async function render(props: Record<string, unknown> = {}) {
  components.push(
    mount(ThinkingBlock, {
      target: document.body,
      props: { content: "plan first", ...props },
    }),
  );
  await tick();
}
async function click(selector: string) {
  const button = document.querySelector<HTMLButtonElement>(selector);
  expect(button).not.toBeNull();
  button!.click();
  await Promise.resolve();
  await tick();
}

beforeEach(() => {
  turnCollapse.activateSession(null);
  turnCollapse.activateSession("s1");
  setLocale("en");
});
afterEach(async () => {
  for (const component of components.splice(0)) await unmount(component);
  document.body.replaceChildren();
  turnCollapse.activateSession(null);
  setLocale("en");
});

describe("ThinkingBlock", () => {
  it("starts collapsed and toggles its content", async () => {
    await render();
    expect(document.querySelector(".thinking-content")).toBeNull();
    await click(".thinking-header");
    expect(document.querySelector(".thinking-content")?.textContent).toContain("plan first");
  });

  it("drives expansion from the turn-collapse store when keyed", async () => {
    await render({ collapseKey: "s1:11:thinking:0" });
    expect(document.querySelector(".thinking-content")).toBeNull();

    turnCollapse.setEventExpanded("s1:11:thinking:0", true);
    await tick();
    expect(document.querySelector(".thinking-content")).not.toBeNull();

    // Clicking collapses back through the store override.
    await click(".thinking-header");
    expect(document.querySelector(".thinking-content")).toBeNull();
    expect(turnCollapse.isEventExpanded("s1:11:thinking:0", true)).toBe(false);
  });

  it("honours the caller's default expansion for keyed blocks", async () => {
    await render({ collapseKey: "s1:11:thinking:0", defaultExpanded: true });
    expect(document.querySelector(".thinking-content")).not.toBeNull();
  });

  it("shows a one-line preview limited to 80 code points", async () => {
    const line = `${"z".repeat(80)}🙂`;
    await render({ content: `${line}\nmore detail` });
    expect(document.querySelector(".thinking-preview")?.textContent).toBe(`${"z".repeat(80)}…`);
  });
});
