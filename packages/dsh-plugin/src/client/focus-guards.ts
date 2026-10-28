/**
 * Keep a modal dialog's focus inside it, across the island's shadow boundary
 * (M-148).
 *
 * Base UI already traps focus: it renders focus guards around a modal's popup
 * and pulls focus back when one of them receives it. That check reads
 * `document.activeElement`, which inside a shadow tree is the host element — not
 * the guard — so the guards never fire here and Tab walks out of the dialog,
 * through the popup seat, into dsh's own document. The reader's next keystroke
 * then goes to the shell instead of the field they were in.
 *
 * Only modal layers render guards (a menu or a popover has none), so a guard is
 * itself the signal that this reconciliation belongs to a dialog. It re-uses
 * Base UI's own guards rather than adding a second trap: a guard that receives
 * focus sends it to the far end of the dialog it wraps.
 */

/** The focusable elements a dialog can cycle through. */
function tabbables(layer: HTMLElement): HTMLElement[] {
  const candidates = layer.querySelectorAll<HTMLElement>(
    "a[href], button, input, select, textarea, [tabindex]",
  );
  return [...candidates].filter(
    (element) =>
      !element.hasAttribute("data-base-ui-focus-guard") &&
      !element.hasAttribute("disabled") &&
      element.tabIndex >= 0 &&
      element.getClientRects().length > 0,
  );
}

/**
 * Reconcile focus guards inside `portalHost`, the container the island's
 * overlays render into.
 *
 * Idempotence is the caller's: `mountIsland` builds one portal container per
 * island, and unmounting drops it.
 */
export function restoreShadowFocusGuards(portalHost: HTMLElement): void {
  portalHost.addEventListener("focusin", (event) => {
    const target = event.target;
    if (!(target instanceof Element)) return;
    const guard = target.closest("[data-base-ui-focus-guard]");
    if (!(guard instanceof HTMLElement)) return;

    // A dialog's guards are siblings of its popup inside its own portal, so the
    // portal is the boundary the browser's tab order escaped.
    const layer = guard.parentElement;
    if (layer === null) return;
    const items = tabbables(layer);
    if (items.length === 0) return;

    const popup = layer.querySelector('[role="dialog"]');
    const beforePopup =
      popup !== null &&
      popup !== undefined &&
      (guard.compareDocumentPosition(popup) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;

    // The guard before the popup is the backward edge (focus is arriving from
    // before it), the one after is the forward edge.
    const destination = beforePopup ? items[items.length - 1] : items[0];
    destination?.focus();
  });
}
