/**
 * The document-level half of the island's stylesheet (M-148).
 *
 * Tailwind v4 gives every `--tw-*` variable its initial value through an
 * `@property` rule, and Chromium does not register those rules from a shadow
 * tree's stylesheet. Both of the island's shadow roots therefore run without
 * them: `border` resolves `--tw-border-style` to nothing and draws no border,
 * every `shadow-*`/`ring-*`/`inset-ring-*` list is invalid and computes to
 * `none`, and the utilities built on the same mechanism (`translate-*`,
 * `rotate-*`, `blur-*`, the gradient stops) lose their defaults too — while the
 * identical markup renders correctly in a document. `@layer properties` does
 * not help: that is the old-browser fallback, and the browser is new enough to
 * know `@property`, so the fallback stays inert and the registration is what is
 * missing.
 *
 * Registration is document-wide, which is what makes the fix small: one sheet
 * added by the shell's document serves the panel, the popup seat, and any
 * shadow tree in between. The rules only give names their initial values, so a
 * shell with its own Tailwind registers the same names with the same values and
 * sees no difference.
 *
 * The rules are extracted from the compiled stylesheet at build time
 * (`scripts/island-property-rules.mjs`) and arrive here as
 * `ISLAND_PROPERTY_RULES`.
 */

/** Documents this module has already served, so a second start is a no-op. */
const installed = new WeakSet<Document>();

/**
 * Register the island's `@property` rules on a document, once.
 *
 * Returns whether this call installed anything, which is what the tests read;
 * a document is never served twice, so booting, warming and a rail click cannot
 * stack copies of the sheet.
 */
export function installPropertyRules(doc: Document, rules: string): boolean {
  if (installed.has(doc) || rules.trim() === "") return false;
  installed.add(doc);

  const sheet = constructedSheet(doc, rules);
  if (sheet !== null) {
    doc.adoptedStyleSheets = [...doc.adoptedStyleSheets, sheet];
    return true;
  }

  // No constructable stylesheets, or a rule this browser will not construct: a
  // plain document stylesheet registers the same names for the same trees.
  const style = doc.createElement("style");
  style.dataset.multicaProperties = "";
  style.textContent = rules;
  (doc.head ?? doc.documentElement).append(style);
  return true;
}

/** A stylesheet for `rules`, or `null` when this document cannot construct one. */
function constructedSheet(doc: Document, rules: string): CSSStyleSheet | null {
  const Constructable = (globalThis as { CSSStyleSheet?: typeof CSSStyleSheet }).CSSStyleSheet;
  if (typeof Constructable !== "function" || !Array.isArray(doc.adoptedStyleSheets)) {
    return null;
  }
  try {
    const sheet = new Constructable();
    sheet.replaceSync(rules);
    return sheet;
  } catch {
    return null;
  }
}
