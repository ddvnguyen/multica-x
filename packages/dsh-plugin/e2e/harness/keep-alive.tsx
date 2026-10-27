import { createElement, useState } from "react";
import { createRoot } from "react-dom/client";
import { apply } from "../../src/client/index";
import { islandHost } from "../../src/client/island";
import { createSeatComponent } from "../../src/client/seat";
import { BOARD_ROUTE, FAMILY_ROOTS, type IslandRoute } from "../../src/client/navigation";

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

const WORKSPACE = {
  id: "ws-1",
  slug: "demo",
  name: "Demo",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const ISSUE_TITLES = [
  "Rail click stays warm",
  "Detached DOM sizing",
  "Overlays close on leave",
];

function issue(index: number) {
  return {
    id: `11111111-1111-4111-8111-11111111111${index}`,
    workspace_id: "ws-1",
    number: index,
    identifier: `MUL-${index}`,
    title: ISSUE_TITLES[index - 1],
    description: null,
    status: "todo",
    priority: "medium",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "user-1",
    parent_issue_id: null,
    project_id: null,
    position: index * 1000,
    stage: null,
    start_date: null,
    due_date: null,
    metadata: {},
    properties: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

/** One row of the workspace's status catalog: the board's columns. */
function status(key: string, name: string, category: string, position: number) {
  return {
    id: `status-${key}`,
    workspace_id: "ws-1",
    key,
    name,
    description: "",
    category,
    color: "#6b7280",
    icon: null,
    is_system: true,
    position,
    archived_at: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

function inboxItem(index: number) {  return {
    id: `inbox-${index}`,
    workspace_id: "ws-1",
    recipient_type: "member",
    recipient_id: "user-1",
    type: "new_comment",
    severity: "info",
    issue_id: issue(index).id,
    title: `Inbox row ${index}`,
    body: null,
    read: false,
    archived: false,
    created_at: "2026-01-01T00:00:00Z",
  };
}

/** Every `/multica` call the island makes, so the spec can count the boots. */
declare global {
  interface Window {
    __requests?: string[];
    /** The keep-alive host, connected or not — a detached node has no selector. */
    __islandHost?: () => HTMLElement | null;
  }
}

window.__islandHost = islandHost;

function installBackend(): void {
  const requests: string[] = [];
  window.__requests = requests;

  const json = (body: unknown): Response =>
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { "content-type": "application/json" },
    });

  window.fetch = async (
    input: RequestInfo | URL,
    init?: RequestInit,
  ): Promise<Response> => {
    const url = typeof input === "string" ? input : input.toString();
    requests.push(url);
    const body = (): Record<string, unknown> => {
      try {
        return JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
      } catch {
        return {};
      }
    };

    if (url.startsWith("/multica/config")) {
      return json({ wsPath: "/multica/ws", workspaceSlug: "", problem: null });
    }
    if (url.startsWith("/multica/api/workspaces")) return json([WORKSPACE]);
    if (url.startsWith("/multica/api/me")) {
      return json({ id: "user-1", name: "Ada", email: "ada@example.test" });
    }
    if (url.startsWith("/multica/api/inbox")) {
      return json([inboxItem(1), inboxItem(2), inboxItem(3)]);
    }
    if (url.startsWith("/multica/api/issue-view-preferences")) {
      return json({
        scope_type: "workspace",
        scope_id: null,
        prefs: { hidden: [], order: [] },
        updated_at: "",
      });
    }
    if (url.startsWith("/multica/api/issue-statuses")) {
      return json({
        statuses: [
          status("todo", "Todo", "unstarted", 0),
          status("in_progress", "In Progress", "started", 1),
          status("in_review", "In Review", "started", 2),
          status("done", "Done", "done", 3),
          status("cancelled", "Cancelled", "closed", 4),
        ],
        categories: ["unstarted", "started", "done", "closed"],
        total: 5,
      });
    }
    if (url.startsWith("/multica/api/properties")) {
      return json({ properties: [], total: 0 });
    }
    if (url.startsWith("/multica/api/issue-workflows")) {
      return json({ workflows: [], total: 0 });
    }
    if (url.startsWith("/multica/api/labels")) {
      return json({ labels: [], total: 0 });
    }
    // The board is the table surface: one cursor page per column, addressed by
    // the group key the descriptor named.
    if (url.startsWith("/multica/api/issues/table/groups")) {
      return json({
        query_fingerprint: "fp",
        total: 3,
        groups: [
          { key: "todo", value: { kind: "status", status: "todo" }, count: 3 },
        ],
        next_cursor: null,
      });
    }
    if (url.startsWith("/multica/api/issues/table/rows")) {
      const key = String(body().group_key ?? "");
      const mine = key === "" || key.includes("todo") || key.includes("unstarted");
      const rows = mine
        ? [1, 2, 3].map((index) => ({ issue: issue(index), direct_child_count: 0 }))
        : [];
      return json({
        query_fingerprint: "fp",
        group_key: body().group_key ?? null,
        parent_id: null,
        total: rows.length,
        rows,
        branch_total: rows.length,
        next_cursor: null,
      });
    }
    if (url.startsWith("/multica/api/issues/table/facets")) {
      // Status totals are server-authoritative: the board trusts this facet to
      // decide a column is non-empty, so it has to agree with the rows.
      return json({
        query_fingerprint: "fp",
        total: 3,
        facets: [{ kind: "status", values: [{ key: "todo", count: 3 }] }],
      });
    }
    if (url.startsWith("/multica/api/issues")) {
      const issues = [issue(1), issue(2), issue(3)];
      return json({ issues, total: issues.length, comments: [] });
    }
    if (url.startsWith("/multica/api/config") || url.startsWith("/multica/api/onboarding")) {
      return json({});
    }
    // Everything else the fork reads on mount is a list.
    return json([]);
  };
}

installBackend();

const ENTRIES: ReadonlyArray<{ id: string; label: string; route: IslandRoute }> = [
  { id: "multica", label: "Issues", route: BOARD_ROUTE },
  { id: "multica-inbox", label: "Inbox", route: FAMILY_ROOTS.inbox },
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
