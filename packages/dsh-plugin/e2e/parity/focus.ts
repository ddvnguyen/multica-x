import type { Page } from "@playwright/test";

/**
 * The island's focus and key boundaries, read from the page (M-148).
 *
 * Base UI traps focus and dsh listens for shortcuts on the document, but both
 * sit on the far side of a shadow boundary, so `page.evaluate` has to walk the
 * shadow roots the way the browser does before it can answer either question.
 */

export interface FocusReport {
  /** Tag of the deepest focused element, or `null` for none. */
  tag: string | null;
  /** Its `data-testid`, when it or an ancestor has one. */
  testId: string | null;
  /**
   * Whether the focused element is inside the open dialog's own layer.
   *
   * The dialog's focus trap keeps focus in the dialog's document subtree, which
   * includes the focus guards Base UI renders around the popup — those live in
   * the same shadow root as the dialog and are part of the trap, not an escape
   * back into the panel.
   */
  insideDialog: boolean;
}

export async function focusReport(page: Page): Promise<FocusReport> {
  return page.evaluate(() => {
    const roots: (Document | ShadowRoot)[] = [document];
    for (const host of document.querySelectorAll<HTMLElement>("[data-parity-island]")) {
      if (host.shadowRoot !== null) roots.push(host.shadowRoot);
    }
    for (const seat of document.querySelectorAll<HTMLElement>("[data-multica-popup-seat]")) {
      if (seat.shadowRoot !== null) roots.push(seat.shadowRoot);
    }

    let active: Element | null = document.activeElement;
    while (active !== null && active.shadowRoot?.activeElement != null) {
      active = active.shadowRoot.activeElement;
    }
    const dialog = roots
      .map((root) => root.querySelector('[data-testid="dialog-content"]'))
      .find((candidate) => candidate !== null);
    const labelled = active?.closest<HTMLElement>("[data-testid]") ?? null;
    const dialogRoot = dialog?.getRootNode() ?? null;
    return {
      tag: active?.tagName.toLowerCase() ?? null,
      testId: labelled?.dataset.testid ?? null,
      insideDialog:
        dialog !== null &&
        dialog !== undefined &&
        active !== null &&
        dialogRoot === active.getRootNode() &&
        dialogRoot.contains(active),
    };
  });
}

/** The keys the shell's own document-level handler has seen. */
export async function shellKeys(page: Page): Promise<string[]> {
  return page.evaluate(() => (window as unknown as { __shellKeys?: string[] }).__shellKeys ?? []);
}

/** Forget keystrokes recorded so far, so a test only reads its own typing. */
export async function clearShellKeys(page: Page): Promise<void> {
  await page.evaluate(() => {
    const keys = (window as unknown as { __shellKeys?: string[] }).__shellKeys;
    keys?.splice(0, keys.length);
  });
}
