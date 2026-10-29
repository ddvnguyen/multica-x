import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { islandPropertyRules } from "./scripts/island-property-rules.mjs";
import { collect } from "../../scripts/mmx-version.mjs";

// Deterministic build stamp for tests (M-155): the island reads the same
// injected __MMX_BUILD__ the real bundle carries, with fixed inputs so an
// assertion never depends on when the suite ran.
const mmxBuild = collect({
  now: new Date("2026-01-01T00:00:00Z"),
  sha: "testsha",
});

export default defineConfig({
  // The tailwind plugin is here because the island imports its stylesheet with
  // Vite's `?inline` query; without it the import resolves to nothing. The
  // property-rules plugin adds the `@property` export the same module carries
  // in the real build (M-148), so tests see the rules the island registers.
  plugins: [react(), tailwindcss(), islandPropertyRules()],
  define: {
    __MMX_BUILD__: JSON.stringify(mmxBuild),
  },
  test: {
    environment: "jsdom",
    globals: true,
    // The island's `?inline` stylesheet has to be the real compiled text: the
    // `@property` rules the shim registers are extracted from it (M-148), and
    // vitest stubs CSS to an empty module by default.
    css: true,
    include: ["tests/**/*.test.{ts,tsx}"],
    setupFiles: ["./tests/setup.ts"],
  },
});
