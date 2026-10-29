/**
 * Build-time half of the island's `@property` fix (M-148).
 *
 * Tailwind v4 gives every `--tw-*` custom property its initial value with an
 * `@property` rule. Chromium does not register those rules from a stylesheet
 * inside a shadow tree, so both of the island's shadow roots lose them: every
 * utility that reads an unset variable — `border` through
 * `--tw-border-style`, `shadow-*` and `ring-*` through their shadow lists,
 * `translate-*`, `blur-*`, the gradient stops — computes to nothing there
 * while the same markup renders correctly in a document.
 *
 * The rules therefore have to be registered on the document, and this plugin is
 * where they come from: it reads what Tailwind actually emitted and appends the
 * extracted rules to the compiled `?inline` CSS module as a second export.
 * Nothing is hand-kept, so a Tailwind upgrade cannot leave the shim behind.
 *
 * Registered by `vite.config.ts`, `vitest.config.ts` and
 * `e2e/vite.harness.config.ts`: every context that imports `island.css?inline`
 * needs the export to exist.
 */

/** The export every consumer imports alongside the stylesheet's default. */
export const PROPERTY_RULES_EXPORT = "propertyRules";

/**
 * Every top-level `@property` rule in a stylesheet, in source order.
 *
 * `@property` blocks cannot nest, so `[^{}]*` is the whole body; a `@property`
 * mentioning something that is not a custom property is not a registration and
 * is skipped. Comments are stripped first so a commented-out rule cannot land
 * in the shim.
 */
export function extractPropertyRules(css) {
  const withoutComments = css.replace(/\/\*[\s\S]*?\*\//g, "");
  // The rules are re-emitted verbatim: rewriting whitespace inside a value
  // would be a silent change to what the browser registers.
  const rules = withoutComments.match(/@property\s+--[A-Za-z0-9_-]+\s*\{[^{}]*\}/g) ?? [];
  return rules.join("\n");
}

/**
 * The CSS text of a Vite `?inline` module: `export default "<css>"`.
 *
 * Returns `null` when the module does not have that shape, so the plugin can
 * fail loudly instead of shipping an island with an empty shim.
 */
export function inlineCssSource(code) {
  const match = /export\s+default\s+("(?:[^"\\]|\\.)*")\s*;?\s*$/.exec(code.trim());
  if (match === null) return null;
  try {
    return JSON.parse(match[1]);
  } catch {
    return null;
  }
}

/**
 * The plugin.
 *
 * `enforce: "post"` so it sees the stylesheet after Tailwind has compiled it —
 * that is the text the shadow roots actually receive.
 */
export function islandPropertyRules() {
  return {
    name: "multica-island-property-rules",
    enforce: "post",
    transform(code, id) {
      const [file, query] = id.split("?");
      if (!file.endsWith("/island.css") || query !== "inline") return null;
      const css = inlineCssSource(code);
      if (css === null) {
        throw new Error("island.css?inline is not an inline CSS module");
      }
      const rules = extractPropertyRules(css);
      if (rules === "") {
        // An empty shim is the bug this plugin exists to fix, so a build that
        // finds no rules must not succeed.
        throw new Error("island.css?inline carries no @property rules");
      }
      return {
        code: `${code.trim()}\nexport const ${PROPERTY_RULES_EXPORT} = ${JSON.stringify(rules)};\n`,
        map: null,
      };
    },
  };
}
