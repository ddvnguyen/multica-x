import { attachIsland, detachIsland } from "./island";
import type { IslandRoute } from "./navigation";

/**
 * The components the dsh shell renders for this plugin.
 *
 * All are hook-free on purpose. The shell's React 18 calls them, while every
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

/**
 * The seat the island currently lives in.
 *
 * dsh mounts exactly one `main` cell at a time, so "the seat that was attached
 * last" is "the seat on screen". That is the only node whose removal may
 * detach the island: the shell swaps cells in one commit, and the outgoing
 * seat's ref goes `null` *after* the incoming one has already claimed the
 * island — sweeping every seat that ever existed would tear down the island
 * that just moved.
 */
let activeSeat: HTMLElement | null = null;

/**
 * Ref callback for a seat host.
 *
 * React 18 invokes it with `null` on unmount without saying which node left, and
 * it detaches the ref *before* removing the node from the document — so the
 * sweep runs one microtask later, once the commit that dropped the node has
 * finished. A re-render re-attaches the same node (the ref identity is stable
 * per seat) and leaves it connected, which is what keeps a re-render from
 * detaching a live island.
 */
export function attachSeat(node: HTMLElement | null, route: IslandRoute): void {
  if (node !== null) {
    activeSeat = node;
    attachIsland(node, route);
    return;
  }
  queueMicrotask(() => {
    if (activeSeat !== null && !activeSeat.isConnected) {
      detachIsland();
      activeSeat = null;
    }
  });
}

export interface SeatOptions {
  /** Panel id this seat belongs to; it is the rail row and the `main` key. */
  panelId: string;
  /** The entry this seat opens, and re-opens on every re-attach. */
  route: IslandRoute;
}

const SEAT_STYLE = {
  display: "block",
  position: "relative",
  flex: "1 1 auto",
  minWidth: 0,
  minHeight: 0,
  height: "100%",
} as const;

/**
 * The main-column seat: an empty element the island owns.
 *
 * One per rail entry, so the element carries the entry it was mounted for and
 * the island opens at that entry rather than at the board every time.
 */
export function createSeatComponent(
  createElement: HostCreateElement,
  seat: SeatOptions,
) {
  // Created once per registration: a stable ref identity is what tells React
  // "same node" across re-renders instead of detach-then-attach.
  const ref = (node: HTMLElement | null): void => attachSeat(node, seat.route);
  return function MulticaSeat() {
    return createElement("div", {
      ref,
      "data-multica-panel": seat.panelId,
      style: SEAT_STYLE,
    });
  };
}

/** The six glyphs, one per entry, drawn so they read at rail size. */
export type RailGlyph = "issues" | "agents" | "skills" | "inbox" | "chat" | "settings";

/**
 * The sidebar rail glyph.
 *
 * dsh renders `sidebar.panellist` occupants inside its own button; the icon is
 * geometry only, sized from the owner props, so it inherits the shell's colour
 * and active state. The collapsed rail shows no labels, so the six entries are
 * told apart here.
 */
export function createRailIcon(createElement: HostCreateElement, glyph: RailGlyph) {
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
      ...glyphGeometry(createElement, glyph),
    );
  };
}

function glyphGeometry(
  createElement: HostCreateElement,
  glyph: RailGlyph,
): unknown[] {
  switch (glyph) {
    case "issues":
      // Board columns.
      return [
        createElement("rect", { key: "a", x: 3, y: 4, width: 5, height: 16, rx: 1.5 }),
        createElement("rect", { key: "b", x: 10, y: 4, width: 5, height: 10, rx: 1.5 }),
        createElement("rect", { key: "c", x: 17, y: 4, width: 4, height: 13, rx: 1.5 }),
      ];
    case "agents":
      // A small figure: head and shoulders.
      return [
        createElement("circle", { key: "a", cx: 12, cy: 8, r: 3.2 }),
        createElement("path", { key: "b", d: "M5.5 20a6.5 6.5 0 0 1 13 0" }),
      ];
    case "skills":
      // A spark.
      return [
        createElement("path", {
          key: "a",
          d: "M13 3 5.5 13.5H11l-1 7.5 8-11H12l1-7Z",
        }),
      ];
    case "inbox":
      // A tray.
      return [
        createElement("path", {
          key: "a",
          d: "M4 5h16v9h-4.5l-1.5 3h-4l-1.5-3H4Z",
        }),
        createElement("path", { key: "b", d: "M4 14v5h16v-5" }),
      ];
    case "chat":
      // A speech bubble.
      return [
        createElement("path", {
          key: "a",
          d: "M4.5 5.5h15v10h-10l-5 4.5Z",
        }),
      ];
    case "settings":
      // Upstream's own gear (lucide `Settings`, v1.0.1), so the rail row is the
      // page it opens rather than a second icon for the same idea.
      return [
        createElement("path", {
          key: "a",
          d: "M9.671 4.136a2.34 2.34 0 0 1 4.659 0 2.34 2.34 0 0 0 3.319 1.915 2.34 2.34 0 0 1 2.33 4.033 2.34 2.34 0 0 0 0 3.831 2.34 2.34 0 0 1-2.33 4.033 2.34 2.34 0 0 0-3.319 1.915 2.34 2.34 0 0 1-4.659 0 2.34 2.34 0 0 0-3.32-1.915 2.34 2.34 0 0 1-2.33-4.033 2.34 2.34 0 0 0 0-3.831A2.34 2.34 0 0 1 6.35 6.051a2.34 2.34 0 0 0 3.319-1.915",
        }),
        createElement("circle", { key: "b", cx: 12, cy: 12, r: 3 }),
      ];
  }
}
