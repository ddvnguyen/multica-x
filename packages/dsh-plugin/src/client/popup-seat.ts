import islandCss from "./island.css?inline";

/**
 * The island's popup seat: the container overlays portal into.
 *
 * dsh's shell paints every overlay it owns into `[data-shell-overlay]`
 * (absolute, z-index:20, `pointer-events:none` with its own children forced
 * back to `auto`). The panel is app-layer: an island popup rendered inside
 * the panel's shadow root can never win those pixels, because stacking is
 * decided per stacking context and the whole panel sits below the overlay
 * layer — the popup renders, styled, but loses every click to whatever the
 * shell parks there. The fix belongs at this boundary: the island's overlays
 * join the same layer, in a zero-size seat whose shadow root carries the
 * island's stylesheet, so popups stay styled and stack *with* shell overlays
 * instead of under them.
 *
 * The seat is `pointer-events:none` like the layer around it, and the portal
 * container inside re-enables hit-testing — the same pattern the shell uses
 * for its overlay children. Without that, the seat would swallow every popup
 * click one layer deeper than the bug it exists to fix.
 */

/**
 * Rewrite `:root` to `:host`.
 *
 * Inside a shadow tree `:root` matches nothing: the fork's design tokens
 * would otherwise be injected and never land.
 */
export function shadowStyles(css: string): string {
  return css.replace(/:root\b/g, ":host");
}

/** The island stylesheet, rewritten for a shadow tree. */
export const ISLAND_CSS = shadowStyles(islandCss);

/**
 * Create the popup seat in the shell's overlay layer.
 *
 * Returns `null` when the shell exposes no overlay layer (unit tests, older
 * shells): the caller then keeps the portal container inside the surface —
 * the pre-overlay behaviour, which still works wherever nothing covers the
 * panel.
 */
export function createPopupSeat(
  doc: Document,
): { seat: HTMLElement; portalHost: HTMLElement } | null {
  const layer = doc.querySelector("[data-shell-overlay]");
  if (layer === null) return null;

  const seat = doc.createElement("div");
  seat.dataset.multicaPopupSeat = "";
  Object.assign(seat.style, {
    position: "absolute",
    top: "0",
    left: "0",
    width: "0",
    height: "0",
    zIndex: "60",
    pointerEvents: "none",
  });

  // A second copy of the island's stylesheet: the seat lives outside the
  // panel's shadow root, and a popup that left this shadow tree would arrive
  // unstyled — the exact defect the portal container exists to prevent.
  const shadow = seat.attachShadow({ mode: "open" });
  const style = doc.createElement("style");
  style.dataset.multicaStyles = "";
  style.textContent = ISLAND_CSS;

  const portalHost = doc.createElement("div");
  portalHost.className = "multica-island-portals";
  portalHost.dataset.multicaPortals = "";

  shadow.append(style, portalHost);
  layer.append(seat);
  return { seat, portalHost };
}
