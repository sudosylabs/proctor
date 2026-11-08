import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/browser",
  outputDir: process.env.PROCTOR_TEST_REPORT_DIR
    ? `${process.env.PROCTOR_TEST_REPORT_DIR}/browser/results`
    : "./node_modules/.tmp/playwright",
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.PROCTOR_TEST_REPORT_DIR
    ? [
        ["list"],
        ["junit", { outputFile: `${process.env.PROCTOR_TEST_REPORT_DIR}/browser/junit.xml` }],
        ["html", { outputFolder: `${process.env.PROCTOR_TEST_REPORT_DIR}/browser/html`, open: "never" }],
      ]
    : "list",
  use: {
    baseURL: "http://127.0.0.1:5173",
    trace: "retain-on-failure",
  },
  projects: [
    { name: "chromium", use: { browserName: "chromium" } },
    // Logic-only tests run once; tests whose behaviour depends on the engine
    // carry @cross-browser and also run in Firefox and WebKit.
    { name: "firefox", use: { browserName: "firefox" }, grep: /@cross-browser/ },
    { name: "webkit", use: { browserName: "webkit" }, grep: /@cross-browser/ },
  ],
  webServer: {
    command: "npm run dev",
    url: "http://127.0.0.1:5173",
    reuseExistingServer: !process.env.CI,
  },
});
