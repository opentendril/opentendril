import { defineConfig, devices } from "@playwright/test";

// E2E suite for the Command Center SPA. Runs entirely against the built
// static bundle (`vite preview`) with the Go Stem mocked at the network
// layer (HTTP via page.route, /ws via page.routeWebSocket) — no Docker, no
// real Stem, so this runs the same in CI as it does locally.
const port = 4173;

export default defineConfig({
  testDir: "./tests",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? "dot" : "list",

  use: {
    baseURL: `http://127.0.0.1:${port}`,
    trace: "on-first-retry",
  },

  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],

  // Builds the bundle and serves it via `vite preview`, so `npm run test:e2e`
  // is a single, self-contained command in CI and locally alike.
  webServer: {
    command: `npm run build && npm run preview -- --port ${port} --strictPort`,
    url: `http://127.0.0.1:${port}`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
