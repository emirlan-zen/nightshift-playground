import { defineConfig, devices } from "@playwright/test";

// E2E runs against the REAL Go control binary in dev mode (NIGHTSHIFT_DEV=1),
// which serves the embedded SPA and the stubbed API on one origin with seeded
// sample data — no systemd, no prod side effects. Tests drive user-visible
// behavior by URL + text/role, so they survive the control-page refactor.
const PORT = 8788;

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? "line" : "list",
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: "on-first-retry",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    // Build the binary (embeds the committed dist) and run it in dev mode with a
    // throwaway HOME so seed data is fresh and nothing prod is touched.
    command:
      "bash -c 'cd ../.. && go build -C control -o control-e2e . && NIGHTSHIFT_DEV=1 HOME=\"$(mktemp -d)\" LISTEN=127.0.0.1:8788 ./control/control-e2e'",
    url: `http://127.0.0.1:${PORT}/api/status`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
