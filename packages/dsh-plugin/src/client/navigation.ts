import type { NavigationAdapter } from "../fork";

/**
 * The island's own two-place navigation.
 *
 * The fork's pages navigate by path, so the island keeps a path-shaped adapter
 * rather than teaching the views about a second router: `push` parses the path
 * the same way the web app's routes would and swaps which page is mounted.
 *
 * Q8 — hide, do not delete. The fork still ships GitHub and Slack integrations,
 * billing, autopilots and squads; this panel simply never exposes them. There
 * is no dashboard chrome here (no sidebar, no command palette), and a
 * navigation to anything outside the two places below is refused rather than
 * mounted, so a stray link in a shared component cannot open a surface the
 * epic asked to keep out of dsh. The code stays in the fork.
 */
export const HIDDEN_AREAS = [
  "integrations/github",
  "integrations/slack",
  "billing",
  "autopilots",
  "squads",
] as const;

export type IslandRoute =
  | { view: "board" }
  | { view: "issue"; issueId: string };

export const BOARD_ROUTE: IslandRoute = { view: "board" };

/**
 * Read an island route out of a workspace path.
 *
 * `/w/issues` is the board; `/w/issues/MUL-12` is one issue. `null` means the
 * path is not one of the two places this panel owns.
 */
export function parseIslandRoute(path: string): IslandRoute | null {
  const pathname = path.split("?")[0]?.split("#")[0] ?? "";
  const parts = pathname.split("/").filter((part) => part !== "");
  const issuesAt = parts.indexOf("issues");
  if (issuesAt === -1) return null;
  const rest = parts.slice(issuesAt + 1);
  if (rest.length === 0) return BOARD_ROUTE;
  if (rest.length === 1 && rest[0] !== undefined) {
    return { view: "issue", issueId: decodeURIComponent(rest[0]) };
  }
  return null;
}

/** The path an island route renders as, under the workspace's own prefix. */
export function islandPath(slug: string, route: IslandRoute): string {
  const base = `/${slug}/issues`;
  return route.view === "board" ? base : `${base}/${encodeURIComponent(route.issueId)}`;
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
  const refuse = (path: string): void => {
    onRefused?.(path);
  };
  const go = (path: string): void => {
    const next = parseIslandRoute(path);
    if (next === null) {
      refuse(path);
      return;
    }
    onChange(next);
  };
  return {
    push: go,
    replace: go,
    back: () => onChange(BOARD_ROUTE),
    pathname: islandPath(slug, route),
    searchParams: new URLSearchParams(),
    hash: "",
    // A copied link would address this dsh page, which has no Multica route,
    // so there is nothing useful to hand out. The issue's own Multica URL is a
    // web-app concept and is deliberately not invented here.
    getShareableUrl: () => "",
    canGoBack: () => route.view === "issue",
  };
}
