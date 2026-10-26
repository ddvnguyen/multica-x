import type { NavigationAdapter } from "../fork";

/**
 * The island's own navigation, across the five places the sidebar menu opens.
 *
 * dsh gives a plugin one `main` cell per rail row and no router of its own, so
 * the island keeps a path-shaped adapter rather than teaching the views about a
 * second router: `push` parses the path the same way the web app's routes would
 * and swaps which page is mounted.
 *
 * Q8 — hide, do not delete. The fork still ships GitHub and Slack integrations,
 * billing, autopilots and squads; this panel simply never exposes them. There
 * is no dashboard chrome here (no sidebar, no command palette), and a
 * navigation to anything outside the five families below is refused rather than
 * mounted, so a stray link in a shared component cannot open a surface the epic
 * asked to keep out of dsh. The code stays in the fork.
 */
export const HIDDEN_AREAS = [
  "integrations/github",
  "integrations/slack",
  "billing",
  "autopilots",
  "squads",
] as const;

/** The five families of page, each named after its path segment. */
export const ISLAND_FAMILIES = [
  "issues",
  "agents",
  "skills",
  "inbox",
  "chat",
] as const;

export type IslandFamily = (typeof ISLAND_FAMILIES)[number];

/** A route plus the query it was addressed with, when it had one. */
type WithQuery<Route> = Route & { query?: string };

/**
 * One place the island renders.
 *
 * Detail views are their family's single-segment depth (`agents/<id>`); a
 * family without one (`inbox`, `chat`) addresses its state through the query,
 * which is why every member may carry one.
 */
export type IslandRoute =
  | WithQuery<{ view: "board" }>
  | WithQuery<{ view: "issue"; issueId: string }>
  | WithQuery<{ view: "agents" }>
  | WithQuery<{ view: "agent"; agentId: string }>
  | WithQuery<{ view: "skills" }>
  | WithQuery<{ view: "skill"; skillId: string }>
  | WithQuery<{ view: "inbox" }>
  | WithQuery<{ view: "chat" }>;

export const BOARD_ROUTE: IslandRoute = { view: "board" };

/** The route that opens each family: one per rail entry. */
export const FAMILY_ROOTS: Record<IslandFamily, IslandRoute> = {
  issues: BOARD_ROUTE,
  agents: { view: "agents" },
  skills: { view: "skills" },
  inbox: { view: "inbox" },
  chat: { view: "chat" },
};

function isFamily(segment: string): segment is IslandFamily {
  return (ISLAND_FAMILIES as readonly string[]).includes(segment);
}

/** Query text without the leading `?` or a trailing fragment; `""` is none. */
function normalizeQuery(raw: string): string {
  const hash = raw.indexOf("#");
  const query = hash === -1 ? raw : raw.slice(0, hash);
  return query.startsWith("?") ? query.slice(1) : query;
}

/**
 * Read an island route out of a workspace path.
 *
 * `/w/issues` and `/w/issues/MUL-12` are the board and one issue; `/w/agents`,
 * `/w/skills`, `/w/inbox` and `/w/chat` are the other four entries, with
 * `/w/agents/<id>` and `/w/skills/<id>` as their detail depth. `null` means the
 * path is not one of the places this panel owns.
 */
export function parseIslandRoute(path: string): IslandRoute | null {
  const hash = path.indexOf("#");
  const withoutHash = hash === -1 ? path : path.slice(0, hash);
  const mark = withoutHash.indexOf("?");
  const pathname = mark === -1 ? withoutHash : withoutHash.slice(0, mark);
  const query = normalizeQuery(mark === -1 ? "" : withoutHash.slice(mark + 1));

  const parts = pathname.split("/").filter((part) => part !== "");
  const at = parts.findIndex(isFamily);
  if (at === -1) return null;
  const route = parseFamilyPath(parts[at] as IslandFamily, parts.slice(at + 1));
  if (route === null) return null;
  return query === "" ? route : { ...route, query };
}

function parseFamilyPath(
  family: IslandFamily,
  rest: string[],
): IslandRoute | null {
  if (rest.length > 1) return null;
  const id = rest[0];
  switch (family) {
    case "issues":
      if (id === undefined) return BOARD_ROUTE;
      return { view: "issue", issueId: decodeURIComponent(id) };
    case "agents":
      if (id === undefined) return { view: "agents" };
      // `agents/new` is the creation flow, which this panel does not render;
      // refusing it is the same scoping decision as the hidden areas above.
      if (id === "new") return null;
      return { view: "agent", agentId: decodeURIComponent(id) };
    case "skills":
      if (id === undefined) return { view: "skills" };
      return { view: "skill", skillId: decodeURIComponent(id) };
    case "inbox":
      return id === undefined ? { view: "inbox" } : null;
    case "chat":
      return id === undefined ? { view: "chat" } : null;
  }
}

/** The path an island route renders as, without its query. */
export function islandPathname(slug: string, route: IslandRoute): string {
  const base = `/${slug}`;
  switch (route.view) {
    case "board":
      return `${base}/issues`;
    case "issue":
      return `${base}/issues/${encodeURIComponent(route.issueId)}`;
    case "agents":
      return `${base}/agents`;
    case "agent":
      return `${base}/agents/${encodeURIComponent(route.agentId)}`;
    case "skills":
      return `${base}/skills`;
    case "skill":
      return `${base}/skills/${encodeURIComponent(route.skillId)}`;
    case "inbox":
      return `${base}/inbox`;
    case "chat":
      return `${base}/chat`;
  }
}

/** The path an island route renders as, query included. */
export function islandPath(slug: string, route: IslandRoute): string {
  const pathname = islandPathname(slug, route);
  return route.query === undefined || route.query === ""
    ? pathname
    : `${pathname}?${route.query}`;
}

/**
 * Where a detail view returns to, or `null` for a family root.
 *
 * The island has no history stack: `back` is the detail page's own parent, and
 * a family root reports that there is nothing behind it.
 */
export function islandParent(route: IslandRoute): IslandRoute | null {
  switch (route.view) {
    case "issue":
      return BOARD_ROUTE;
    case "agent":
      return FAMILY_ROOTS.agents;
    case "skill":
      return FAMILY_ROOTS.skills;
    default:
      return null;
  }
}

/** Stable identity of a route, for remount boundaries and test assertions. */
export function islandRouteKey(route: IslandRoute): string {
  switch (route.view) {
    case "issue":
      return `issue:${route.issueId}`;
    case "agent":
      return `agent:${route.agentId}`;
    case "skill":
      return `skill:${route.skillId}`;
    default:
      return route.view;
  }
}

export interface IslandNavigationOptions {
  slug: string;
  route: IslandRoute;
  onChange: (route: IslandRoute) => void;
  /** Reports a refused destination; used for one diagnostic, never for UI. */
  onRefused?: (path: string) => void;
}

export function createIslandNavigation({
  slug,
  route,
  onChange,
  onRefused,
}: IslandNavigationOptions): NavigationAdapter {
  const go = (path: string): void => {
    const next = parseIslandRoute(path);
    if (next === null) {
      onRefused?.(path);
      return;
    }
    onChange(next);
  };
  const parent = islandParent(route);
  return {
    push: go,
    replace: go,
    back: () => {
      if (parent !== null) onChange(parent);
    },
    pathname: islandPathname(slug, route),
    searchParams: new URLSearchParams(route.query ?? ""),
    hash: "",
    // A copied link would address this dsh page, which has no Multica route,
    // so there is nothing useful to hand out. The issue's own Multica URL is a
    // web-app concept and is deliberately not invented here.
    getShareableUrl: () => "",
    canGoBack: () => parent !== null,
  };
}
