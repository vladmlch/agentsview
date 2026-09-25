// @vitest-environment jsdom
import { mount, unmount, tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it } from "vite-plus/test";
import { setLocale } from "../../i18n/index.js";
import { turnCollapse } from "../../stores/turn-collapse.svelte.js";

// @ts-ignore
import SkillBlock from "./SkillBlock.svelte";

describe("SkillBlock", () => {
  let component: ReturnType<typeof mount> | undefined;

  afterEach(() => {
    if (component) unmount(component);
    component = undefined;
    document.body.innerHTML = "";
    setLocale("en");
  });

  it("localizes the label fallback", async () => {
    setLocale("zh-CN");

    component = mount(SkillBlock, {
      target: document.body,
      props: { content: "Use the project guidance." },
    });
    await tick();

    expect(document.querySelector(".skill-label")?.textContent).toBe("技能：未知");
  });
});

describe("SkillBlock turn-scoped collapse", () => {
  let component: ReturnType<typeof mount> | undefined;

  beforeEach(() => {
    turnCollapse.activateSession(null);
    turnCollapse.activateSession("s1");
  });
  afterEach(() => {
    if (component) unmount(component);
    component = undefined;
    document.body.innerHTML = "";
    turnCollapse.activateSession(null);
    setLocale("en");
  });

  it("drives expansion from the store when keyed", async () => {
    component = mount(SkillBlock, {
      target: document.body,
      props: { content: "Use the guide.", collapseKey: "s1:11:skill:0" },
    });
    await tick();
    expect(document.querySelector(".skill-content")).toBeNull();

    turnCollapse.setEventExpanded("s1:11:skill:0", true);
    await tick();
    expect(document.querySelector(".skill-content")).not.toBeNull();
  });
});
