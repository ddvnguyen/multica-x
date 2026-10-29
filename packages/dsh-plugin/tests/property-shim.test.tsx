import { describe, expect, it, vi } from "vitest";
import { ISLAND_CSS, ISLAND_PROPERTY_RULES } from "../src/client/popup-seat";
import { installPropertyRules } from "../src/client/property-shim";

/**
 * M-148: the island's `@property` rules have to reach the document.
 *
 * The extracted list is checked against the stylesheet it came from — a shim
 * that has drifted from the CSS is the same bug one Tailwind upgrade later —
 * and the registration itself is checked on documents it has never seen, since
 * only a fresh document can be installed into twice.
 */

/** A document with no registration yet; jsdom supports these without a window. */
function freshDocument(): Document {
  return document.implementation.createHTMLDocument("property-shim");
}

/**
 * What a stylesheet registers, whitespace-normalised.
 *
 * Tailwind minifies in a build and pretty-prints in the test and harness dev
 * servers, so the comparison is about the registrations, not their layout.
 */
function registrations(css: string): string[] {
  return (css.match(/@property\s+--[A-Za-z0-9_-]+\s*\{[^{}]*\}/g) ?? []).map((rule) =>
    rule.replace(/\s+/g, " "),
  );
}

/** The shim's own registrations, whitespace-normalised the same way. */
function shimRegistrations(): string[] {
  return registrations(ISLAND_PROPERTY_RULES);
}

describe("ISLAND_PROPERTY_RULES", () => {
  it("is every registration the stylesheet carries, in order", () => {
    expect(ISLAND_PROPERTY_RULES).not.toBe("");
    expect(shimRegistrations()).toEqual(registrations(ISLAND_CSS));
  });

  it("carries the registrations the island's utilities read", () => {
    // The three that make the visible defects: no border, no shadow, no ring.
    for (const name of ["--tw-border-style", "--tw-shadow", "--tw-ring-shadow"]) {
      const rule = new RegExp(`^@property\\s+${name}\\s*\\{`);
      expect(shimRegistrations().some((registration) => rule.test(registration))).toBe(true);
    }
  });
});

describe("installPropertyRules", () => {
  it("adds a constructed sheet to the document once", () => {
    const doc = freshDocument();
    const adopted: unknown[] = [];
    Object.defineProperty(doc, "adoptedStyleSheets", {
      configurable: true,
      value: adopted,
      writable: true,
    });
    const replacements: string[] = [];
    class FakeSheet {
      replaceSync(rules: string): void {
        replacements.push(rules);
      }
    }
    vi.stubGlobal("CSSStyleSheet", FakeSheet);

    expect(installPropertyRules(doc, "")).toBe(false);
    expect(installPropertyRules(doc, ISLAND_PROPERTY_RULES)).toBe(true);
    // A second start — the warm-up and the first rail click both call it — is
    // a no-op, so the document never stacks copies of the sheet.
    expect(installPropertyRules(doc, ISLAND_PROPERTY_RULES)).toBe(false);

    expect(replacements).toEqual([ISLAND_PROPERTY_RULES]);
    expect(doc.adoptedStyleSheets).toHaveLength(1);
    // The rules are document-level, not shadow-level: nothing is written into a
    // tree, so a shell with its own Tailwind sees the same registrations.
    expect(doc.head.querySelector("style[data-multica-properties]")).toBeNull();
    vi.unstubAllGlobals();
  });

  it("falls back to a document stylesheet where sheets cannot be constructed", () => {
    const doc = freshDocument();

    expect(installPropertyRules(doc, ISLAND_PROPERTY_RULES)).toBe(true);
    expect(installPropertyRules(doc, ISLAND_PROPERTY_RULES)).toBe(false);

    const styles = doc.querySelectorAll("style[data-multica-properties]");
    expect(styles).toHaveLength(1);
    expect(styles[0]?.textContent).toBe(ISLAND_PROPERTY_RULES);
  });

  it("ignores an empty shim rather than registering nothing and claiming success", () => {
    const doc = freshDocument();
    expect(installPropertyRules(doc, "  ")).toBe(false);
    expect(doc.querySelector("style[data-multica-properties]")).toBeNull();
  });
});
