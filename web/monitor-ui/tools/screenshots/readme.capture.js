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

// `themes` is per capture: the console has two themes and the READMEs show the
// overview in both, because "the light theme is not a white sheet" is a claim
// that needs a picture. The rest stay in the dark theme, which is the one the
// console opens in.
const captures = [
  { path: "/overview", name: "monitor-overview", themes: ["dark", "light"] },
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
    for (const capture of captures) {
      for (const theme of capture.themes || ["dark"]) {
        test(`capture ${capture.name} (${theme})`, async ({ page }, testInfo) => {
          await page.addInitScript(
            ({ language, theme }) => {
              window.localStorage.setItem("trajecta.monitor.language", language);
              // Pin the theme so captures do not depend on the runner's OS
              // setting, or on which capture ran first in the worker.
              window.localStorage.setItem("trajecta.monitor.theme", theme);
            },
            { language: language.value, theme },
          );
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

          // A screenshot of the "light" theme that was captured in the dark one
          // is the failure mode this file can actually produce, so the theme is
          // asserted rather than trusted: the class the palette keys off, and
          // the canvas it resolved to.
          const applied = await page.evaluate(() => ({
            dark: document.documentElement.classList.contains("dark"),
            canvas: getComputedStyle(document.documentElement).getPropertyValue("--bg-canvas").trim(),
          }));
          expect(applied.dark, `${capture.name} captured the wrong theme`).toBe(theme === "dark");
          expect(applied.canvas, "the capture has no canvas colour").not.toBe("");

          const dir = resolve(imagesRoot(testInfo), language.dir);
          mkdirSync(dir, { recursive: true });
          const suffix = theme === "dark" ? "" : `-${theme}`;
          await page.screenshot({
            path: resolve(dir, `${capture.name}${suffix}.png`),
            fullPage: false,
          });
        });
      }
    }
  });
}
