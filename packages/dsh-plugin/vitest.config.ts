import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { islandPropertyRules } from "./scripts/island-property-rules.mjs";

export default defineConfig({
  // The tailwind plugin is here because the island imports its stylesheet with
  // Vite's `?inline` query; without it the import resolves to nothing. The
  // property-rules plugin adds the `@property` export the same module carries
  // in the real build (M-148), so tests see the rules the island registers.
  plugins: [react(), tailwindcss(), islandPropertyRules()],
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
