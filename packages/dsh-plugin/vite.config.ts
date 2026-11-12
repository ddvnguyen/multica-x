import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { islandPropertyRules } from "./scripts/island-property-rules.mjs";
import { collect } from "../../scripts/mmx-version.mjs";

/**
 * The build stamp (M-155): `x.x.x-yyMMMdd.HH.mm`, one string per build.
 *
 * CI passes `MMX_BUILD_JSON` — the payload `scripts/mmx-version.mjs
 * --from-git` printed for the exact commit it asserts — so one SHA always
 * builds one string no matter how fresh the turbo cache is. A local build
 * stamps its own wall clock, which is what "build time" means off CI.
 */
const mmxBuild = process.env.MMX_BUILD_JSON
  ? JSON.parse(process.env.MMX_BUILD_JSON)
  : collect();

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
  plugins: [react(), tailwindcss(), islandPropertyRules()],
  define: {
    "process.env.NODE_ENV": JSON.stringify("production"),
    "import.meta.env.MODE": JSON.stringify("production"),
    // The injected build identity (M-155); replaced in every bundled module,
    // including @multica/views' compiled output, which probes it with typeof.
    __MMX_BUILD__: JSON.stringify(mmxBuild),
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
