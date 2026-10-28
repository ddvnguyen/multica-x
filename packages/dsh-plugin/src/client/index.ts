import { createElement as bundledCreateElement } from "react";
import type { DshClientContext } from "../dsh";
import { startIsland } from "./island";
import type { IslandRoute } from "./navigation";
import { FAMILY_ROOTS } from "./navigation";
import {
  createRailIcon,
  createSeatComponent,
  type HostCreateElement,
  type RailGlyph,
} from "./seat";
import { startUnreadPoll } from "./unread-store";

/**
 * Browser face of the Multica panel.
 *
 * One rail row per Multica view, additive and without shadowing:
 *
 * - `sidebar.panellist` (a `list`) renders one sidebar button per registration
 *   and addresses the matching `main` cell by list id. Each entry below is one
 *   of those rows; the sidebar owns the button, the label and the selection
 *   state, which is why the menu is five rows rather than a dropdown the plugin
 *   would have to position inside someone else's column. The list has no
 *   grouping API, so the grouping is expressed by adjacent order (30–34) and a
 *   shared glyph family, and each label carries the product name because the
 *   collapsed rail shows only a glyph and a tooltip.
 * - `main` (a `keyed` slot) gets one cell per entry: the seat the React 19
 *   island mounts into, opening at that entry's own view. `priority: -100` is
 *   the additive convention the shipped panels use; nothing shipped is replaced.
 *
 * The sixth row is Settings: the fork's own page, with the surfaces this panel
 * does not render hidden and one MOCT group added. It is a rail row and not a
 * route inside another row because a settings page is a destination a reader
 * returns to, and the shell's rail is where destinations live.
 */
export interface MulticaEntry {
  /** `sidebar.panellist` list id, and the `main` cell key it addresses. */
  id: string;
  /** Rail row label: the button's accessible name and its tooltip. */
  label: string;
  /** Where the island opens when this row is selected. */
  route: IslandRoute;
  /** Glyph drawn inside the shell's rail button. */
  glyph: RailGlyph;
}

/** The six entries, in the order the Owner named them. */
export const MULTICA_ENTRIES: readonly MulticaEntry[] = [
  {
    id: "multica",
    label: "Multica Issues",
    route: FAMILY_ROOTS.issues,
    glyph: "issues",
  },
  {
    id: "multica-agents",
    label: "Multica Agents",
    route: FAMILY_ROOTS.agents,
    glyph: "agents",
  },
  {
    id: "multica-skills",
    label: "Multica Skills",
    route: FAMILY_ROOTS.skills,
    glyph: "skills",
  },
  {
    id: "multica-inbox",
    label: "Multica Inbox",
    route: FAMILY_ROOTS.inbox,
    glyph: "inbox",
  },
  {
    id: "multica-chat",
    label: "Multica Chat",
    route: FAMILY_ROOTS.chat,
    glyph: "chat",
  },
  {
    id: "multica-settings",
    label: "Multica Settings",
    route: FAMILY_ROOTS.settings,
    glyph: "settings",
  },
];

/** The entry the panel opened with; kept as the first row's id. */
export const MULTICA_PANEL_ID = "multica";

/** First rail row's order; the entries follow in sequence. */
const FIRST_ORDER = 30;

/** dsh resolves `inject` before calling `apply`. */
export const inject = ["slots"];

let hostCreateElement: HostCreateElement | null = null;

/**
 * Hand this module the shell's `createElement`.
 *
 * The bundle wrapper calls it with `require("react")` from the shell's module
 * table before `apply` runs. Without it the plugin assumes a React 19 host and
 * uses its own bundled `createElement`, which is what the unit tests do.
 */
export function configureHostRuntime(createElement: HostCreateElement | null): void {
  hostCreateElement = createElement;
}

export function apply(ctx: DshClientContext): void {
  // The rail dot's baseline count. The browser half has no teardown path —
  // `apply` returns void and the slot registrations below are unregistered by
  // the shell, not by this module — so the poller lives as long as the page;
  // the store's own guard makes a repeated `apply` share it rather than start
  // a second timer.
  startUnreadPoll();

  const createElement = hostCreateElement ?? (bundledCreateElement as HostCreateElement);

  MULTICA_ENTRIES.forEach((entry, index) => {
    const railIcon = createRailIcon(createElement, entry.glyph);
    const seat = createSeatComponent(createElement, {
      panelId: entry.id,
      route: entry.route,
    });

    ctx.slots.inject("sidebar.panellist", () =>
      ctx.slots.register(
        {
          name: "sidebar.panellist",
          id: entry.id,
          order: FIRST_ORDER + index,
          label: entry.label,
        },
        railIcon,
      ),
    );

    ctx.slots.inject("main", () =>
      ctx.slots.register(
        {
          name: "main",
          key: entry.id,
          priority: -100,
        },
        seat,
      ),
    );
  });

  warmIsland();
}

/** One warm-up per page: `apply` runs once, and a retry would only duplicate it. */
let warming = false;

/**
 * Build the island ahead of the first rail click.
 *
 * The shell keeps only the selected `main` cell mounted, so the island is not
 * created until something claims a seat — and that claim is the click that used
 * to pay for the whole boot: config, workspace, ~30 queries, a WebSocket and
 * 292 KB of stylesheet. `apply` runs during shell boot, so the build waits for
 * an idle window instead of competing with the shell's first paint. A browser
 * without an idle scheduler falls back to a short timer; the island is created
 * detached either way, and `attachIsland` picks up whatever is already there.
 */
function warmIsland(): void {
  if (warming || typeof document === "undefined") return;
  warming = true;
  const idle = (globalThis as { requestIdleCallback?: (fn: () => void, options?: { timeout: number }) => unknown })
    .requestIdleCallback;
  if (typeof idle === "function") {
    idle(() => startIsland(document), { timeout: 2000 });
    return;
  }
  setTimeout(() => startIsland(document), 200);
}
