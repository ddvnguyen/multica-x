import type { Page } from "@playwright/test";
import type { ParitySurface } from "./targets";

/**
 * Reading one surface back (M-148).
 *
 * The targets live in three different trees depending on where the overlay
 * went: the panel's shadow root, the popup seat's shadow root in the shell's
 * overlay layer, or — in the reference rendering — the document itself. The
 * snapshot therefore searches every root the page exposes instead of assuming
 * one, and reports anything it could not find rather than silently skipping it.
 */

export interface SurfaceSnapshot {
  /** Computed values, keyed by target test id then property name. */
  styles: Record<string, Record<string, string>>;
  /** Targets that were not in the page at all. */
  missing: string[];
}

/** Open the surface, when it is an overlay the fixtures render on demand. */
export async function openSurface(page: Page, surface: ParitySurface): Promise<void> {
  if (surface.trigger === undefined) return;
  await page.getByTestId(surface.trigger).click();
}

/** Wait until every target of a surface is in the page. */
export async function waitForSurface(page: Page, surface: ParitySurface): Promise<void> {
  await page.waitForFunction((testIds: string[]) => {
    const roots: (Document | ShadowRoot)[] = [document];
    for (const host of document.querySelectorAll<HTMLElement>("[data-parity-island]")) {
      if (host.shadowRoot !== null) roots.push(host.shadowRoot);
    }
    for (const seat of document.querySelectorAll<HTMLElement>("[data-multica-popup-seat]")) {
      if (seat.shadowRoot !== null) roots.push(seat.shadowRoot);
    }
    return testIds.every((testId) =>
      roots.some((root) => root.querySelector(`[data-testid="${testId}"]`) !== null),
    );
  }, surface.targets.map((target) => target.testId));
}

export async function snapshotSurface(
  page: Page,
  surface: ParitySurface,
): Promise<SurfaceSnapshot> {
  await openSurface(page, surface);
  await waitForSurface(page, surface);
  // The overlays animate in from their own transform; the properties under
  // test do not depend on that frame, but measuring a settled popup keeps the
  // two renderings measuring the same thing.
  await page.waitForTimeout(250);

  return page.evaluate((targets: Array<{ testId: string; properties: string[] }>) => {
    const roots: (Document | ShadowRoot)[] = [document];
    for (const host of document.querySelectorAll<HTMLElement>("[data-parity-island]")) {
      if (host.shadowRoot !== null) roots.push(host.shadowRoot);
    }
    for (const seat of document.querySelectorAll<HTMLElement>("[data-multica-popup-seat]")) {
      if (seat.shadowRoot !== null) roots.push(seat.shadowRoot);
    }

    const styles: Record<string, Record<string, string>> = {};
    const missing: string[] = [];
    for (const target of targets) {
      const element = roots
        .map((root) => root.querySelector(`[data-testid="${target.testId}"]`))
        .find((candidate) => candidate !== null);
      if (element === undefined || element === null) {
        missing.push(target.testId);
        continue;
      }
      const computed = getComputedStyle(element);
      const values: Record<string, string> = {};
      for (const property of target.properties) {
        values[property] = computed.getPropertyValue(property);
      }
      styles[target.testId] = values;
    }
    return { styles, missing };
  }, surface.targets.map((target) => ({ testId: target.testId, properties: [...target.properties] })));
}

/**
 * The differences that matter, one line each.
 *
 * Values are compared exactly: both renderings are the same browser reading the
 * same stylesheet, so a difference in computed style is the defect the harness
 * is looking for, not noise to be ironed out with a tolerance.
 */
export function diffStyles(actual: SurfaceSnapshot, expected: SurfaceSnapshot): string[] {
  const differences: string[] = [];
  for (const [testId, properties] of Object.entries(expected.styles)) {
    const islandValues = actual.styles[testId];
    if (islandValues === undefined) {
      differences.push(`${testId}: not rendered in the island`);
      continue;
    }
    for (const [property, expectedValue] of Object.entries(properties)) {
      const actualValue = islandValues[property];
      if (actualValue !== expectedValue) {
        differences.push(`${testId} ${property}: island "${actualValue}" ≠ reference "${expectedValue}"`);
      }
    }
  }
  return differences;
}

/** The one computed value a regression test reads. */
export async function computedStyle(
  page: Page,
  testId: string,
  property: string,
): Promise<string | null> {
  return page.evaluate(
    ({ testId: id, property: name }) => {
      const roots: (Document | ShadowRoot)[] = [document];
      for (const host of document.querySelectorAll<HTMLElement>("[data-parity-island]")) {
        if (host.shadowRoot !== null) roots.push(host.shadowRoot);
      }
      for (const seat of document.querySelectorAll<HTMLElement>("[data-multica-popup-seat]")) {
        if (seat.shadowRoot !== null) roots.push(seat.shadowRoot);
      }
      const element = roots
        .map((root) => root.querySelector(`[data-testid="${id}"]`))
        .find((candidate) => candidate !== null);
      if (element === undefined || element === null) return null;
      return getComputedStyle(element).getPropertyValue(name);
    },
    { testId, property },
  );
}
