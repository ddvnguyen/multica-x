import { expect, test } from "@playwright/test";
import { computedStyle, diffStyles, snapshotSurface } from "./parity/fingerprint";
import { PARITY_SURFACES } from "./parity/targets";

/**
 * M-148: the island renders the fork's stylesheet the way the document does.
 *
 * The reference page is the same compiled CSS applied at document level, which
 * is how upstream Multica web serves it and where the browser registers
 * Tailwind's `@property` rules itself. The island page is the production
 * boundary — shadow roots, popup seat, document-level shim. Same browser, same
 * build, same commit: any computed-style difference between them is the shadow
 * DOM defect this card fixes, not a version skew.
 *
 * jsdom has no layout and no `@property` registration, so this is a Chromium
 * suite; that is also the engine the shell ships in.
 */

const VIEWPORT = { width: 1440, height: 1300 } as const;

test.describe("computed styles", () => {
  for (const theme of ["light", "dark"] as const) {
    for (const surface of PARITY_SURFACES) {
      test(`${surface.id} matches the document rendering (${theme})`, async ({ browser }) => {
        const context = await browser.newContext({ viewport: VIEWPORT });
        try {
          const reference = await context.newPage();
          await reference.goto(`/parity.html?mode=reference&theme=${theme}`);
          const island = await context.newPage();
          await island.goto(`/parity.html?mode=island&theme=${theme}`);

          const expected = await snapshotSurface(reference, surface);
          const actual = await snapshotSurface(island, surface);

          expect(expected.missing, "reference targets").toEqual([]);
          expect(actual.missing, "island targets").toEqual([]);
          expect(diffStyles(actual, expected)).toEqual([]);
        } finally {
          await context.close();
        }
      });
    }
  }
});

/**
 * The gate has to be able to fail.
 *
 * With the shim switched off, the island page is what lane z showed: Tailwind's
 * `@property` rules sit in the shadow root, Chromium ignores them, and every
 * utility reading an unset `--tw-*` computes to nothing. If this ever passes,
 * the shim stopped mattering and the suite above stopped proving anything.
 */
test("without the shim the island loses Tailwind's @property values", async ({ browser }) => {
  const context = await browser.newContext({ viewport: VIEWPORT });
  try {
    const withoutShim = await context.newPage();
    await withoutShim.goto("/parity.html?mode=island&theme=light&shim=off");

    expect(await computedStyle(withoutShim, "board-card", "border-top-style")).toBe("none");
    expect(await computedStyle(withoutShim, "board-card", "box-shadow")).toBe("none");
    expect(await computedStyle(withoutShim, "fx-ring", "box-shadow")).toBe("none");

    const withShim = await context.newPage();
    await withShim.goto("/parity.html?mode=island&theme=light");

    expect(await computedStyle(withShim, "board-card", "border-top-style")).toBe("solid");
    expect(await computedStyle(withShim, "board-card", "box-shadow")).not.toBe("none");
    expect(await computedStyle(withShim, "fx-ring", "box-shadow")).not.toBe("none");
  } finally {
    await context.close();
  }
});
