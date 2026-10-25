import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

/**
 * The island bundle.
 *
 * dsh fetches a plugin's client artifact as a classic script and takes the
 * module table it registers, so the output is one CommonJS body that
 * `scripts/wrap-client.mjs` wraps in `window.__ModuleLoader__.load({ id, factory })`.
 *
 * Everything is bundled — React and React DOM 19 included. That is the point:
 * the shell's module table would hand out React 18, and the fork must never be
 * downgraded. The only import resolved at runtime is the `require("react")` the
 * wrapper uses for *element creation* on the shell side of the seat.
 */
export default defineConfig({
  plugins: [react(), tailwindcss()],
  define: {
    "process.env.NODE_ENV": JSON.stringify("production"),
    "import.meta.env.MODE": JSON.stringify("production"),
  },
  build: {
    outDir: "lib",
    // tsc has already written the host half here.
    emptyOutDir: false,
    cssCodeSplit: false,
    sourcemap: false,
    target: "es2022",
    lib: {
      entry: "src/client/index.ts",
      formats: ["cjs"],
      fileName: () => "client.bundle.cjs",
    },
    rollupOptions: {
      output: {
        assetFileNames: "client.bundle[extname]",
        inlineDynamicImports: true,
      },
    },
  },
});
