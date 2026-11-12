import islandCss, * as islandStyles from "./island.css?inline";

/**
 * The island's popup seat: the container overlays portal into.
 *
 * dsh's shell paints every overlay it owns into `[data-shell-overlay]`
 * (absolute, z-index:20, `pointer-events:none` with its own children forced
 * back to `auto`). The panel is app-layer: an island popup rendered inside the
 * panel's shadow root can never win those pixels, because stacking is decided
 * per stacking context and the whole panel sits below the overlay layer — the
 * popup renders, styled, but loses every click to whatever the shell parks
 * there.
 *
 * So the island always owns a seat, and the seat always lives outside the
 * panel's shadow root:
 *
 * - in the shell's overlay layer when there is one, which is where the shell's
 *   own popups render and what the seat exists to join;
 * - otherwise in a seat of its own on `document.body` (`position: fixed`,
 *   `inset: 0`, `pointer-events: none`), which keeps popups above the panel
 *   and — the part that is a bug, not a preference — keeps every popup in the
 *   light DOM.
 *
 * That last point is why there is no in-panel fallback any more (M-148). Base
 * UI dismisses a popover on a press outside it, and to decide "outside" it
 * walks up from the pressed element to the document, stopping at a shadow
 * boundary (`getParentNode` treats a shadow root as terminal). Once it finds a
 * `[data-base-ui-inert]` marker — which it creates itself for any modal layer —
 * outside that subtree, it concludes the press landed on content injected after
 * the popup rendered and ignores it. With the popups inside the panel's shadow
 * root, the walk stopped at the panel's surface and every inert marker on the
 * page was "outside" it: Runtime stayed open when Model was clicked, and the
 * press only dismissed when it landed on the shell's own body.
 *
 * Each seat holds a shadow root carrying the island's stylesheet, so popups stay
 * styled wherever they render. The seat is `pointer-events:none` like the layer
 * around it, and the portal container inside re-enables hit-testing — the same
 * pattern the shell uses for its overlay children. Without that, the seat would
 * swallow every popup click one layer deeper than the bug it exists to fix.
 */

/** The overlay layer dsh mounts its own overlays into. */
const SHELL_LAYER = "[data-shell-overlay]";

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
 * The `@property` rules the compiled stylesheet carries, in source order.
 *
 * Extracted at build time from the text above
 * (`scripts/island-property-rules.mjs`), because the shadow roots cannot
 * register them: Chromium ignores `@property` inside a shadow tree, which is
 * what leaves `border`, `shadow-*`, `ring-*` and the rest computing to nothing
 * in the island. `installPropertyRules` puts them on the document instead
 * (M-148).
 */
export const ISLAND_PROPERTY_RULES = readPropertyRules();

/**
 * The second export of the stylesheet module, or a loud failure when the build
 * plugin was not registered.
 *
 * It cannot be a typed named import: TypeScript resolves Vite's `*?inline`
 * declaration before any narrower pattern, so `import { propertyRules }` is a
 * type error even though the build emits it. An empty shim is the bug this
 * exists to fix, so a missing export throws instead of silently rendering
 * borders and shadows as `none` again.
 */
function readPropertyRules(): string {
  const { propertyRules: rules } = islandStyles as unknown as { propertyRules?: string };
  if (typeof rules !== "string" || rules === "") {
    throw new Error(
      "island.css?inline carries no @property rules: register scripts/island-property-rules.mjs in this Vite config",
    );
  }
  return rules;
}

/** The island's popup seat. */
export interface PopupSeat {
  /** Where overlays portal. One element for the island's whole life. */
  portalHost: HTMLElement;
  /** The zero-size box holding the portal host, for tests and teardown. */
  element: HTMLElement;
  /** Remove the seat and stop waiting for a layer. */
  dispose(): void;
}

/** One seat element, with its own copy of the island's stylesheet. */
function buildSeat(doc: Document, inShellLayer: boolean): { seat: HTMLElement; root: ShadowRoot } {
  const seat = doc.createElement("div");
  seat.dataset.multicaPopupSeat = "";
  Object.assign(
    seat.style,
    inShellLayer
      ? {
          // Inside the layer: the layer is already the viewport-sized box, so a
          // zero-size seat at its origin positions popups in viewport space.
          position: "absolute",
          top: "0",
          left: "0",
          width: "0",
          height: "0",
          zIndex: "60",
          pointerEvents: "none",
        }
      : {
          // Without a layer: cover the viewport so popup coordinates (computed
          // against the viewport) still line up with their triggers.
          position: "fixed",
          top: "0",
          right: "0",
          bottom: "0",
          left: "0",
          zIndex: "60",
          pointerEvents: "none",
        },
  );

  // A copy of the island's stylesheet: the seat lives outside the panel's
  // shadow root, and a popup that left this shadow tree would arrive unstyled —
  // the exact defect the portal container exists to prevent.
  const root = seat.attachShadow({ mode: "open" });
  const style = doc.createElement("style");
  style.dataset.multicaStyles = "";
  style.textContent = ISLAND_CSS;
  root.append(style);
  return { seat, root };
}

/**
 * Build the island's popup seat.
 *
 * A shell that mounts its chrome after the plugin booted has no
 * `[data-shell-overlay]` yet — the island is warmed up from a plugin hook, so
 * the layer usually appears a moment later. The seat therefore starts on the
 * document and moves into the layer the moment it shows up, carrying the same
 * portal container, so open popups (and React's portals) follow it instead of
 * being rebuilt.
 */
export function createPopupSeat(doc: Document): PopupSeat {
  const portalHost = doc.createElement("div");
  portalHost.className = "multica-island-portals";
  portalHost.dataset.multicaPortals = "";

  let seat: HTMLElement | null = null;
  let observer: MutationObserver | null = null;
  let settled = false;

  const place = (layer: HTMLElement | null): void => {
    const next = buildSeat(doc, layer !== null);
    next.root.append(portalHost);
    (layer ?? doc.body ?? doc.documentElement).append(next.seat);
    seat?.remove();
    seat = next.seat;
    if (layer === null) return;
    // In the layer: nothing left to wait for. Held by the shell's stacking
    // context from here on.
    settled = true;
    observer?.disconnect();
    observer = null;
  };

  place(doc.querySelector<HTMLElement>(SHELL_LAYER));

  if (!settled && typeof MutationObserver !== "undefined") {
    const root = doc.body ?? doc.documentElement;
    observer = new MutationObserver(() => {
      if (settled) return;
      const layer = doc.querySelector<HTMLElement>(SHELL_LAYER);
      if (layer !== null) place(layer);
    });
    observer.observe(root, { childList: true, subtree: true });
  }

  return {
    portalHost,
    get element() {
      if (seat === null) throw new Error("popup seat has been disposed");
      return seat;
    },
    dispose() {
      settled = true;
      observer?.disconnect();
      observer = null;
      seat?.remove();
      seat = null;
      portalHost.remove();
    },
  };
}
