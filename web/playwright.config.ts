import { defineConfig, devices } from "@playwright/test";

const reports = process.env.ASSAY_REPORTS ?? "../tmp/reports";

export default defineConfig({
  testDir: "./e2e",
  // The files share one instance and run in name order: setup comes first.
  workers: 1,
  fullyParallel: false,
  retries: 0,
  forbidOnly: true,
  timeout: 30_000,
  reporter: [["list"], ["json", { outputFile: `${reports}/e2e.json` }]],
  use: {
    baseURL: process.env.E2E_BASE_URL ?? "http://localhost:18080",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
