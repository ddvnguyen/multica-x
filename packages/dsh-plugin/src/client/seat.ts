import { isIslandMounted, mountIsland, unmountIsland } from "./island";

/**
 * The two components the dsh shell renders for this plugin.
 *
 * Both are hook-free on purpose. The shell's React 18 calls them, while every
 * hook in this package belongs to the bundled React 19, and a React 18 render
 * that reached a React 19 hook would find no dispatcher. Element creation is
 * therefore delegated to the shell's own `createElement`, which keeps the
 * elements the shell receives branded for the React that renders them.
 */

/** `createElement` from whichever React owns the host tree. */
export type HostCreateElement = (
  type: unknown,
  props?: unknown,
  ...children: unknown[]
) => unknown;

/** Every seat currently mounted, so a detached one can be torn down. */
const seats = new Set<HTMLElement>();

const SEAT_STYLE = {
  display: "block",
  position: "relative",
  flex: "1 1 auto",
  minWidth: 0,
  minHeight: 0,
  height: "100%",
} as const;

/**
 * Ref callback for a seat host.
 *
 * React 18 invokes it with `null` on unmount without saying which node left, so
 * removal is by connection state: a registered seat that is no longer in the
 * document is exactly the one that unmounted.
 */
export function attachSeat(node: HTMLElement | null): void {
  if (node !== null) {
    seats.add(node);
    mountIsland(node);
    return;
  }
  for (const host of [...seats]) {
    if (!host.isConnected && isIslandMounted(host)) {
      unmountIsland(host);
      seats.delete(host);
    }
  }
}

/** The main-column seat: an empty element the island owns. */
export function createSeatComponent(createElement: HostCreateElement) {
  return function MulticaSeat() {
    return createElement("div", {
      ref: attachSeat,
      "data-multica-panel": "multica",
      style: SEAT_STYLE,
    });
  };
}

/**
 * The sidebar rail glyph.
 *
 * dsh renders `sidebar.panellist` occupants inside its own button; the icon is
 * geometry only, sized from the owner props, so it inherits the shell's colour
 * and active state.
 */
export function createRailIcon(createElement: HostCreateElement) {
  return function MulticaRailIcon({ size = 16 }: { size?: number }) {
    return createElement(
      "svg",
      {
        width: size,
        height: size,
        viewBox: "0 0 24 24",
        fill: "none",
        stroke: "currentColor",
        strokeWidth: 1.6,
        strokeLinecap: "round",
        strokeLinejoin: "round",
        "aria-hidden": "true",
        focusable: "false",
      },
      createElement("rect", { key: "a", x: 3, y: 4, width: 5, height: 16, rx: 1.5 }),
      createElement("rect", { key: "b", x: 10, y: 4, width: 5, height: 10, rx: 1.5 }),
      createElement("rect", { key: "c", x: 17, y: 4, width: 4, height: 13, rx: 1.5 }),
    );
  };
}
