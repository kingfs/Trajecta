import { mkdirSync } from "node:fs";
import { dirname, resolve } from "node:path";

import { expect, test } from "@playwright/test";

// Screenshots embedded in README.md (zh-CN) and README_EN.md (en).
// See playwright.screenshots.config.js for how to run this.
//
// The fixture server assigns UUID trace ids, so the trace-detail page resolves
// its target from the list API rather than hardcoding an id.
async function newestTracePath(page) {
  const response = await page.request.get("/api/traces?page_size=1");
  if (!response.ok()) {
    throw new Error(`trace list API returned ${response.status()}`);
  }
  const body = await response.json();
  const id = body?.items?.[0]?.id;
  if (!id) {
    throw new Error("the fixture server seeded no traces");
  }
  return `/traces/${encodeURIComponent(id)}`;
}

const captures = [
  { path: "/overview", name: "monitor-overview" },
  { path: "/traces", name: "monitor-traces" },
  { name: "monitor-trace-detail", resolvePath: newestTracePath },
  { path: "/providers", name: "monitor-providers" },
];

const languages = [
  { value: "zh-CN", dir: "" },
  { value: "en", dir: "en" },
];

// images/ lives at the repository root; derive it from the Playwright config
// path so the command works from any working directory.
function imagesRoot(testInfo) {
  return resolve(dirname(testInfo.config.configFile), "../../images");
}

for (const language of languages) {
  test.describe(`README screenshots (${language.value})`, () => {
    test.beforeEach(async ({ page }) => {
      await page.addInitScript((value) => {
        window.localStorage.setItem("trajecta.monitor.language", value);
        // Pin the theme so captures do not depend on the runner's OS setting.
        window.localStorage.setItem("trajecta.monitor.theme", "dark");
      }, language.value);
    });

    for (const capture of captures) {
      test(`capture ${capture.name}`, async ({ page }, testInfo) => {
        const path = capture.resolvePath ? await capture.resolvePath(page) : capture.path;
        await page.goto(path);
        // The monitor keeps a live connection open, so `networkidle` never
        // settles. Wait for the app shell and the page heading instead, then
        // give the page's own data one render tick.
        await page.waitForSelector("nav", { timeout: 30_000 });
        await page.waitForSelector("h1", { timeout: 30_000 });
        await page.evaluate(() => document.fonts.ready);
        await page.waitForTimeout(1500);

        // Never ship a screenshot of an error state.
        const text = await page.evaluate(() => document.body.innerText);
        expect(text, `${capture.name} rendered a load error`).not.toMatch(/Unable to load|无法加载/);
        expect(text.length, `${capture.name} rendered an empty page`).toBeGreaterThan(150);

        const dir = resolve(imagesRoot(testInfo), language.dir);
        mkdirSync(dir, { recursive: true });
        await page.screenshot({
          path: resolve(dir, `${capture.name}.png`),
          fullPage: false,
        });
      });
    }
  });
}
