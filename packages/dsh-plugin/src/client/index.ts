import { createElement as bundledCreateElement } from "react";
import type { DshClientContext } from "../dsh";
import { createRailIcon, createSeatComponent, type HostCreateElement } from "./seat";

/**
 * Browser face of the Multica panel.
 *
 * Two additive registrations, no shadowing:
 *
 * - `sidebar.panellist` (a `list`) gets a rail row. The sidebar owns the button
 *   and its selection state, so the row is the shell's own rail entry, and the
 *   panel id is what makes selecting it mount the matching `main` cell.
 * - `main` (a `keyed` slot) gets one cell under the key `multica`: the seat the
 *   React 19 island mounts into. `priority: -100` is the additive convention the
 *   shipped panels use; nothing shipped is replaced.
 */
export const MULTICA_PANEL_ID = "multica";

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
  const createElement = hostCreateElement ?? (bundledCreateElement as HostCreateElement);
  const seat = createSeatComponent(createElement);
  const railIcon = createRailIcon(createElement);

  ctx.slots.inject("sidebar.panellist", () =>
    ctx.slots.register(
      {
        name: "sidebar.panellist",
        id: MULTICA_PANEL_ID,
        order: 30,
        label: "Multica",
      },
      railIcon,
    ),
  );

  ctx.slots.inject("main", () =>
    ctx.slots.register(
      {
        name: "main",
        key: MULTICA_PANEL_ID,
        priority: -100,
      },
      seat,
    ),
  );
}
