import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { islandPropertyRules } from "../scripts/island-property-rules.mjs";

/**
 * The M-116 harness dev server.
 *
 * Serves `harness/` — a fake dsh shell (app column + `[data-shell-overlay]`
 * layer + a blocking card) around the island boundary built from the
 * production `createPopupSeat`. Driven by `../island-overlays.spec.ts`;
 * not part of the shipped client bundle.
 */
export default defineConfig({
  root: fileURLToPath(new URL("./harness", import.meta.url)),
  plugins: [react(), tailwindcss(), islandPropertyRules()],
  server: {
    port: 4173,
    strictPort: true,
    host: "127.0.0.1",
  },
});
