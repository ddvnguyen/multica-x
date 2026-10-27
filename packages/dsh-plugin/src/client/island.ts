import { createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { IslandBoot } from "./island-app";
import { BOARD_ROUTE, islandRouteKey, type IslandRoute } from "./navigation";
import { ISLAND_CSS, createPopupSeat } from "./popup-seat";

/**
 * The React 19 island.
 *
 * dsh's shell is React 18 and this package ships its own React 19, so the two
 * trees must not meet: the shell renders exactly one element here — a host
 * `div` — and this module owns everything below it. That includes the slider
 * between them, a shadow root, which is what keeps Tailwind's preflight and
 * utility classes out of the shell's document.
 *
 * `:root` is rewritten to `:host` (`popup-seat.ts`) because inside a shadow
 * tree `:root` matches nothing: the fork's design tokens would otherwise be
 * injected and never land.
 *
 * One island per document. The shell keeps only the selected `main` cell
 * mounted, so a per-cell island boots a whole app on every rail click: config
 * fetch, workspace resolution, ~30 queries, a WebSocket, and a fresh 292 KB
 * stylesheet. Instead the island is built once and its host element is moved
 * between seats; leaving a panel detaches the host and closes whatever
 * overlays were open, and re-entering moves it back and sets the route. The
 * React tree, the query cache and the connection survive the move.
 */

/** Everything the shell needs to tear one island down again. */
interface IslandHandle {
  root: Root;
  theme: MutationObserver | null;
  /** The popup seat in the shell's overlay layer, when the shell has one. */
  popupSeat: HTMLElement | null;
  /** Where portals render, so a later render can address the same container. */
  portalHost: HTMLElement;
  /**
   * The route the last seat asked for. In-island navigation does not move it,
   * so a re-render that re-attaches the same seat does not reset the page.
   */
  route: IslandRoute;
}

export interface MountIslandOptions {
  /** Which place to open first. The board unless a link named one issue. */
  initialRoute?: IslandRoute;
}

const handles = new WeakMap<HTMLElement, IslandHandle>();

/** The host element of the document's single keep-alive island, once built. */
let keepAlive: HTMLElement | null = null;

/** Is the shell currently dark? dsh exposes both a class and a data attribute. */
export function shellPrefersDark(doc: Document): boolean {
  const root = doc.documentElement;
  return (
    root.classList.contains("dark") ||
    root.dataset.theme === "dark" ||
    (root.dataset.theme === undefined &&
      typeof window !== "undefined" &&
      window.matchMedia?.("(prefers-color-scheme: dark)").matches === true)
  );
}

export function mountIsland(host: HTMLElement, options: MountIslandOptions = {}): void {
  if (handles.has(host)) return;
  const doc = host.ownerDocument;
  const shadow = host.shadowRoot ?? host.attachShadow({ mode: "open" });
  shadow.replaceChildren();

  const style = doc.createElement("style");
  style.dataset.multicaStyles = "";
  style.textContent = ISLAND_CSS;

  const surface = doc.createElement("div");
  surface.className = "multica-island-surface";

  const rootElement = doc.createElement("div");
  rootElement.className = "multica-island-root";
  rootElement.dataset.multicaRoot = "";

  const popupSeat = createPopupSeat(doc);
  const portalHost =
    popupSeat?.portalHost ?? createInSurfacePortalHost(surface);

  surface.append(rootElement);
  shadow.append(style, surface);

  const syncTheme = (): void => {
    const dark = shellPrefersDark(doc);
    surface.classList.toggle("dark", dark);
    // The popup seat lives outside this shadow root, so it must follow the
    // shell's theme itself: popups are children of the portal host, and the
    // `dark` class on that container is what the fork's utilities match on.
    portalHost.classList.toggle("dark", dark);
  };
  syncTheme();

  // The shell switches theme on the document element; follow it so the panel
  // does not jump to light in a dark shell.
  const theme =
    typeof MutationObserver === "undefined"
      ? null
      : new MutationObserver(syncTheme);
  theme?.observe(doc.documentElement, {
    attributes: true,
    attributeFilter: ["class", "data-theme"],
  });

  // dsh owns document-level shortcuts. Keyboard events are composed, so a
  // keystroke typed here would otherwise reach them and trigger shell actions
  // mid-edit; stopping them at the island boundary keeps the shell's bindings
  // to the shell's own surfaces.
  for (const type of ["keydown", "keyup", "keypress"] as const) {
    rootElement.addEventListener(type, (event) => event.stopPropagation());
  }

  const route = options.initialRoute ?? BOARD_ROUTE;
  const root = createRoot(rootElement);
  root.render(createElement(IslandBoot, { portalHost, route }));
  handles.set(host, {
    root,
    theme,
    popupSeat: popupSeat?.seat ?? null,
    portalHost,
    route,
  });
}

/**
 * Build the document's island, detached, and return its host.
 *
 * Called at plugin start so the first rail click already finds a warm island,
 * and again (as a no-op) by `attachIsland` when the first click got there
 * first. The host is deliberately not in the document: nothing shows until a
 * seat claims it.
 */
export function startIsland(
  doc: Document,
  route: IslandRoute = BOARD_ROUTE,
): HTMLElement {
  if (keepAlive !== null && handles.has(keepAlive)) return keepAlive;
  const host = doc.createElement("div");
  host.dataset.multicaHost = "";
  mountIsland(host, { initialRoute: route });
  keepAlive = host;
  return host;
}

/**
 * Put the island into a seat and open the seat's entry.
 *
 * Moving the host keeps the React root attached to the same element, so the
 * tree is re-parented rather than rebuilt. The route only re-renders when the
 * seat asks for a different entry, which is why a re-render of the same seat
 * does not throw the reader back to the entry's root.
 */
export function attachIsland(seat: HTMLElement, route: IslandRoute): void {
  const host = startIsland(seat.ownerDocument, route);
  if (host.parentElement !== seat) seat.appendChild(host);

  const handle = handles.get(host);
  if (handle === undefined) return;
  if (islandRouteKey(handle.route) === islandRouteKey(route)) return;

  handle.route = route;
  handle.root.render(
    createElement(IslandBoot, { portalHost: handle.portalHost, route }),
  );
}

/**
 * Take the island out of the document without tearing it down.
 *
 * Overlays are closed first: a popup rendered through the portal container is
 * not a child of the panel, so it would otherwise stay on screen over the
 * panel that replaced this one — and would reappear on the way back.
 */
export function detachIsland(): void {
  const host = keepAlive;
  if (host === null) return;
  dismissOverlays(host);
  host.remove();
}

/** The keep-alive host, for tests and diagnostics. */
export function islandHost(): HTMLElement | null {
  return keepAlive;
}

/** True while the keep-alive island is in the document. */
export function isIslandAttached(): boolean {
  return keepAlive?.isConnected === true;
}

/**
 * Close whatever the island has open in a portal.
 *
 * Base UI dismisses a floating element on a `pointerdown` outside it, listened
 * for on the document; the island's popups are portalled outside the panel, so
 * detaching the panel is not enough to close them. One synthetic press on the
 * body is that outside press. It is only dispatched when the portal container
 * actually holds something, so a detach with nothing open cannot reach the
 * shell's own dismiss layers.
 */
function dismissOverlays(host: HTMLElement): void {
  const handle = handles.get(host);
  if (handle === undefined || handle.portalHost.childElementCount === 0) return;
  const doc = host.ownerDocument;
  const win = doc.defaultView;
  const Press = win?.PointerEvent ?? win?.MouseEvent;
  if (Press === undefined) return;
  doc.body?.dispatchEvent(
    new Press("pointerdown", {
      bubbles: true,
      composed: true,
      button: 0,
      buttons: 1,
    }),
  );
}

export function unmountIsland(host: HTMLElement): void {
  const handle = handles.get(host);
  if (handle === undefined) return;
  handles.delete(host);
  if (keepAlive === host) keepAlive = null;
  handle.theme?.disconnect();
  handle.root.unmount();
  // The seat sits in the shell's own overlay layer, outside this host's
  // shadow root — nothing else would ever remove it.
  handle.popupSeat?.remove();
  host.shadowRoot?.replaceChildren();
}

/**
 * Tear the keep-alive island down. Test teardown only: the shell never needs
 * this, because a page reload is the only thing that ends an island's life.
 */
export function resetIsland(): void {
  if (keepAlive !== null) unmountIsland(keepAlive);
  keepAlive = null;
}

/** The fallback portal container: inside the surface, as it was. */
function createInSurfacePortalHost(surface: HTMLElement): HTMLElement {
  const portalHost = surface.ownerDocument.createElement("div");
  portalHost.className = "multica-island-portals";
  portalHost.dataset.multicaPortals = "";
  surface.append(portalHost);
  return portalHost;
}

/** True while an island is mounted on this element. For tests and diagnostics. */
export function isIslandMounted(host: HTMLElement): boolean {
  return handles.has(host);
}
