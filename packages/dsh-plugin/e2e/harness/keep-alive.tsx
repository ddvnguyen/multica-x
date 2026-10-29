import { createElement, useState } from "react";
import { createRoot } from "react-dom/client";
import { apply } from "../../src/client/index";
import { islandHost } from "../../src/client/island";
import { createSeatComponent } from "../../src/client/seat";
import { BOARD_ROUTE, FAMILY_ROOTS, type IslandRoute } from "../../src/client/navigation";
import { installBackend } from "./backend";

/**
 * M-130 harness: the keep-alive island in a fake dsh shell.
 *
 * The shell half of the change is two facts that only a real browser shows:
 * exactly one `main` cell is mounted at a time, and the island that was built
 * while nothing was mounted has to lay itself out correctly the moment a cell
 * claims it. jsdom cannot answer either — it has no layout, so a virtual list
 * measured at width 0 looks identical to one measured at width 900.
 *
 * `apply` is the production entry point, so the warm-up under test is the real
 * one: it runs at an idle window, detached, and the first rail click is what
 * attaches it. The backend is a stub in front of the `/multica` proxy path;
 * the board and the inbox render from it the way they render from the host.
 */

installBackend();

window.__islandHost = islandHost;

const ENTRIES: ReadonlyArray<{ id: string; label: string; route: IslandRoute }> = [
  { id: "multica", label: "Issues", route: BOARD_ROUTE },
  // The issue detail page is where the second modal-store dialog (the delete
  // confirm) is opened from (M-146).
  { id: "multica-issue", label: "Issue", route: { view: "issue", issueId: "MUL-1" } },
  { id: "multica-inbox", label: "Inbox", route: FAMILY_ROOTS.inbox },
  // The settings page is the one that made the host's missing height obvious
  // (M-143): its tabs scroll inside the page, so a collapsed host cut it off
  // mid-column instead of showing the first screenful.
  { id: "multica-settings", label: "Settings", route: FAMILY_ROOTS.settings },
];

/**
 * The shell's registrations, made the way `apply` makes them.
 *
 * `apply` is called for the warm-up alone; the cells below are the same
 * components it registers, mounted by a shell that keeps one of them alive.
 */
apply({
  slots: {
    inject: (_key: string, register: () => unknown) => register(),
    register: () => () => {},
  },
});

const seats = new Map(
  ENTRIES.map((entry) => [
    entry.id,
    createSeatComponent(createElement as never, {
      panelId: entry.id,
      route: entry.route,
    }),
  ]),
);

function Shell() {
  // No cell mounted at boot: the island warms up detached, and the first rail
  // click is what claims it. That is the click M-130 is about.
  const [selected, setSelected] = useState<string | null>(null);
  const Seat = selected === null ? null : seats.get(selected) ?? null;

  return (
    <div id="shell">
      <nav id="rail">
        {ENTRIES.map((entry) =>
          createElement(
            "button",
            {
              key: entry.id,
              type: "button",
              "data-testid": `rail-${entry.id}`,
              "aria-pressed": selected === entry.id,
              onClick: () => setSelected(entry.id),
            },
            entry.label,
          ),
        )}
      </nav>
      <main id="main">
        {Seat === null ? null : createElement(Seat as never, { key: selected })}
      </main>
    </div>
  );
}

const style = document.createElement("style");
style.textContent = `
  body { margin: 0; font-family: sans-serif; }
  #shell { display: flex; height: 100vh; }
  #rail { width: 160px; padding: 12px; display: flex; flex-direction: column; gap: 8px; }
  #main { flex: 1 1 auto; min-width: 0; min-height: 0; height: 100vh; }
  /* The shell's own overlay layer: the island's popups are portalled here,
     outside the panel, which is what makes "detach" a separate problem from
     "unmount" — the popup would survive its panel. */
  [data-shell-overlay] { position: absolute; inset: 0; z-index: 20; pointer-events: none; }
  [data-shell-overlay] > * { pointer-events: auto; }
`;
document.head.append(style);

const overlay = document.createElement("div");
overlay.dataset.shellOverlay = "";

const mountPoint = document.createElement("div");
document.body.append(mountPoint, overlay);
createRoot(mountPoint).render(createElement(Shell));
