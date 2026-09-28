import { defineConfig, devices } from "@playwright/test";

// Captures the screenshots embedded in README.md / README_EN.md from the real
// monitor fixture server, so the images always show the current UI.
//
//   cd web/monitor-ui
//   bunx playwright test --config playwright.screenshots.config.js
//
// Output lands in ../../images (Chinese UI) and ../../images/en (English UI).
// The fixture server is the same one used by `bun run test:ui:real`; it seeds
// synthetic channels, models, and traces, so no real traffic is exposed.
//
// The fixture data is seeded relative to the current time, so a regenerated
// image is never byte-identical to the committed one: only commit the PNGs when
// the UI actually changed.
const chromiumPath = (process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE || "").trim();
const launchOptions = chromiumPath ? { executablePath: chromiumPath } : {};
const baseURL = process.env.MONITOR_REAL_BASE_URL || "http://127.0.0.1:4183";
const serverURL = new URL(baseURL);
const serverAddr = `${serverURL.hostname}:${serverURL.port || "80"}`;

export default defineConfig({
  testDir: "./tools/screenshots",
  testMatch: "**/*.capture.js",
  timeout: 60_000,
  fullyParallel: false,
  workers: 1,
  reporter: [["list"]],
  use: {
    baseURL,
    colorScheme: "dark",
  },
  webServer: {
    command: `MONITOR_REAL_ADDR=${serverAddr} go run ./test-fixtures/monitor_real_server.go`,
    url: baseURL,
    reuseExistingServer: false,
    timeout: 180_000,
  },
  projects: [
    {
      name: "readme-screenshots",
      use: {
        ...devices["Desktop Chrome"],
        // Declared after the preset on purpose: project-level `use` wins over
        // the top-level `use`, and the preset pins viewport/deviceScaleFactor,
        // so the README images are captured at 1280x720 with a 1x pixel ratio.
        viewport: { width: 1280, height: 720 },
        deviceScaleFactor: 1,
        channel: undefined,
        launchOptions,
      },
    },
  ],
});
