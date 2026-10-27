import { defineConfig } from "@playwright/test";

/**
 * The M-116 harness suite.
 *
 * One spec (`e2e/island-overlays.spec.ts`) drives the island boundary in a
 * real browser — jsdom cannot reproduce shadow-DOM hit-testing. The dev
 * server is the harness vite config; nothing else must be running.
 */
export default defineConfig({
  testDir: "./e2e",
  timeout: 60_000,
  workers: 1,
  retries: 0,
  use: {
    baseURL: "http://127.0.0.1:4173",
    headless: true,
  },
  webServer: {
    command: "pnpm exec vite --config e2e/vite.harness.config.ts",
    url: "http://127.0.0.1:4173",
    reuseExistingServer: true,
    timeout: 60_000,
  },
});
