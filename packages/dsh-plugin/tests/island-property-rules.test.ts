// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  PROPERTY_RULES_EXPORT,
  extractPropertyRules,
  inlineCssSource,
} from "../scripts/island-property-rules.mjs";

/**
 * The build-time half of M-148.
 *
 * The shim is only as good as what the plugin extracts, so the extraction is
 * pinned on its own: what counts as a registration, what does not, and the two
 * shapes the plugin refuses to ship.
 */

describe("extractPropertyRules", () => {
  it("keeps every registration, in source order", () => {
    const css = [
      ".a{color:red}",
      "@property --tw-border-style{syntax:\"*\";inherits:false;initial-value:solid}",
      "@property --tw-shadow{syntax:\"*\";inherits:false;initial-value:0 0 #0000}",
      ".b{border-style:var(--tw-border-style)}",
    ].join("");

    expect(extractPropertyRules(css).split("\n")).toEqual([
      '@property --tw-border-style{syntax:"*";inherits:false;initial-value:solid}',
      '@property --tw-shadow{syntax:"*";inherits:false;initial-value:0 0 #0000}',
    ]);
  });

  it("re-emits each rule verbatim", () => {
    // A value is not this function's business: `0 0 #0000` and a quoted syntax
    // have to arrive at the browser exactly as Tailwind wrote them.
    const rule = '@property --tw-ring{ syntax: "*"; inherits: false; initial-value: 0 0 #0000 }';
    expect(extractPropertyRules(rule)).toBe(rule);
  });

  it("ignores comments, at-rules that are not registrations, and nested look-alikes", () => {
    const css = [
      "/* @property --tw-commented{syntax:\"*\";inherits:false;initial-value:0} */",
      "@property tw-unprefixed{x:y}",
      "@property --tw-animation-delay{syntax:\"*\";inherits:false;initial-value:0s}",
      "@media (min-width:1px){.x{color:red}}",
    ].join("\n");

    expect(extractPropertyRules(css)).toBe(
      '@property --tw-animation-delay{syntax:"*";inherits:false;initial-value:0s}',
    );
  });

  it("finds nothing in a stylesheet without registrations", () => {
    expect(extractPropertyRules(".a{color:red}")).toBe("");
  });
});

describe("inlineCssSource", () => {
  it("decodes a Vite `?inline` module", () => {
    const css = '.a::before{content:"\\201C"}';
    expect(inlineCssSource(`export default ${JSON.stringify(css)}`)).toBe(css);
  });

  it("returns null for a module that is not inline CSS", () => {
    expect(inlineCssSource('export const css = ".a{}"')).toBeNull();
    expect(inlineCssSource("export default\n")).toBeNull();
  });
});

describe("PROPERTY_RULES_EXPORT", () => {
  it("is the name the island imports", () => {
    expect(PROPERTY_RULES_EXPORT).toBe("propertyRules");
  });
});
