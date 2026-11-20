"use client";

import { useMemo } from "react";
import {
  NavigationProvider,
  type LinkClickIntent,
  type NavigationAdapter,
} from "../navigation";
import { workspaceSlugForPath } from "@multica/core/paths";
import {
  getActiveTab,
  splitTabUrl,
  useActiveTabUrl,
  useTabStore,
} from "@multica/core/tabs";

/**
 * Desktop tab navigation adapter, re-exported from the shared tabs package
 * so desktop and web build on the same store. Identical behavior to the
 * original `apps/desktop/.../platform/navigation.tsx` provider; only the
 * module location changed.
 *
 * Host-owned interceptors (window overlay, multi-window handoff) stay
 * injectable so this module has no Electron imports:
 *
 * - `onTransitionPath` handles pre-workspace flows (`/workspaces/new`,
 *   `/onboarding`, `/invitations`, bare `/invite/…`) the way the overlay
 *   needs. Return true when handled.
 * - `handOffToWorkspaceWindow` hands a cross-workspace open to the window
 *   already showing that workspace. Return true when handled.
 * - `openWorkspaceInNewWindow` / `workspacesInOtherWindows` wire the
 *   multi-window workspace affordances; both optional on single-window
 *   hosts.
 * - `openIssueInNewWindow` / `parseIssueWindowPath` wire detached issue
 *   windows; both optional.
 * - `logout` handles the `/login` push.
 */

export interface DesktopNavigationHost {
  getAppUrl: () => string;
  onTransitionPath?: (path: string) => boolean;
  handOffToWorkspaceWindow?: (
    slug: string,
    path?: string,
    opts?: { navigate?: boolean },
  ) => boolean;
  openWorkspaceInNewWindow?: (slug: string) => void;
  workspacesInOtherWindows?: () => readonly string[];
  openIssueInNewWindow?: (path: string, title: string) => void;
  parseIssueWindowPath?: (path: string) => { path: string } | null;
  logout?: () => void;
  /** Workspace root path whose push resumes the group's view (no forced nav). */
  workspaceResumePath?: (slug: string) => string;
}

export function DesktopTabNavigationProvider({
  children,
  host,
}: {
  children: React.ReactNode;
  host: DesktopNavigationHost;
}) {
  const appUrl = host.getAppUrl();
  // The active session's url IS the location. Primitive subscription: this
  // only re-renders when the active url actually changes.
  const activeUrl = useActiveTabUrl();
  const location = useMemo(() => {
    const url = activeUrl ?? "/";
    const { pathname, suffix } = splitTabUrl(url);
    const hashIdx = suffix.indexOf("#");
    const search = hashIdx === -1 ? suffix : suffix.slice(0, hashIdx);
    const hash = hashIdx === -1 ? "" : suffix.slice(hashIdx);
    return { pathname, search, hash };
  }, [activeUrl]);

  const adapter: NavigationAdapter = useMemo(
    () => ({
      push: (path: string) => {
        if (path === "/login") {
          host.logout?.();
          return;
        }
        if (host.onTransitionPath?.(path)) return;
        const store = useTabStore.getState();
        const active = getActiveTab(store);
        if (active && active.url === path) return;
        if (tryRouteToOtherWorkspace(path, host)) return;
        if (tryRouteToPinnedNewTab(path)) return;
        store.navigateActiveSession(path);
      },
      replace: (path: string) => {
        if (host.onTransitionPath?.(path)) return;
        if (tryRouteToOtherWorkspace(path, host)) return;
        useTabStore.getState().navigateActiveSession(path, { replace: true });
      },
      back: () => {
        useTabStore.getState().goBack();
      },
      forward: () => {
        useTabStore.getState().goForward();
      },
      // The active tab's virtual history, same source the shell's back button
      // reads. A tab opened straight onto a destination sits at index 0 and
      // has nothing behind it.
      canGoBack: () => {
        const active = getActiveTab(useTabStore.getState());
        return (active?.history.index ?? 0) > 0;
      },
      pathname: location.pathname,
      searchParams: new URLSearchParams(location.search),
      // The tab's URL is the only place the fragment survives on desktop: the
      // renderer's own `window.location` is the packaged file:// page.
      hash: location.hash,
      openInNewTab: (
        path: string,
        title?: string,
        opts?: { activate?: boolean },
      ) => {
        // Cross-workspace "open in new tab" switches workspace and opens
        // the path there (focus follows the user), REGARDLESS of
        // `opts.activate`. Deliberate product exception to the
        // background-tab contract (decided with MUL-5860): a background tab
        // in a non-visible workspace's group gives zero feedback.
        const slug = workspaceSlugForPath(path);
        const store = useTabStore.getState();
        if (slug && slug !== store.activeWorkspaceSlug) {
          if (!host.handOffToWorkspaceWindow?.(slug, path)) {
            store.switchWorkspace(slug, path);
          }
          return;
        }
        store.openTab(path, title ?? "", { activate: opts?.activate });
      },
      openWorkspaceInNewWindow: host.openWorkspaceInNewWindow,
      openIssueInNewWindow: host.openIssueInNewWindow
        ? (path: string, title: string) => {
            if (!host.parseIssueWindowPath?.(path)) return;
            host.openIssueInNewWindow?.(path, title);
          }
        : undefined,
      workspacesInOtherWindows: host.workspacesInOtherWindows
        ? () => [...host.workspacesInOtherWindows!()]
        : undefined,
      getShareableUrl: (path: string) => `${appUrl}${path}`,
    }),
    [appUrl, host, location],
  );

  return <NavigationProvider value={adapter}>{children}</NavigationProvider>;
}

/**
 * Intercept pushes that change workspace. Returns `true` if the navigation
 * was delegated to the tab store (caller should NOT proceed).
 */
function tryRouteToOtherWorkspace(
  path: string,
  host: Pick<
    DesktopNavigationHost,
    "handOffToWorkspaceWindow" | "workspaceResumePath"
  >,
): boolean {
  const slug = workspaceSlugForPath(path);
  const store = useTabStore.getState();
  if (!slug || slug === store.activeWorkspaceSlug) return false;
  // Pushing the workspace root resumes the group's current view instead of
  // forcing the default route — the caller asked for the workspace, not a page.
  const resume = host.workspaceResumePath
    ? path === host.workspaceResumePath(slug)
    : false;
  const openPath = resume ? undefined : path;
  if (!host.handOffToWorkspaceWindow?.(slug, openPath)) {
    store.switchWorkspace(slug, openPath);
  }
  return true;
}

/**
 * Intercept pushes originating in a pinned tab and force them into a new
 * tab, so the pinned tab stays parked on its path.
 */
function tryRouteToPinnedNewTab(path: string): boolean {
  const store = useTabStore.getState();
  const active = getActiveTab(store);
  if (!active?.pinned) return false;

  // Same pathname → allowed in the pinned tab (search / hash / same-page
  // state like filters, drawers, form state). Any pathname change → new tab.
  const currentPathname = splitTabUrl(active.url).pathname;
  const newPathname = splitTabUrl(path).pathname;
  if (currentPathname === newPathname) return false;

  store.openTab(path, "", { activate: true });
  return true;
}

/**
 * Execute a content link (the `multica:navigate` event fired by shared
 * content — comments, chat, issue descriptions) with desktop semantics:
 * modifier-clicks open background tabs, cross-workspace links hand off.
 */
export function routeContentLinkPath(
  path: string,
  disposition: LinkClickIntent = "push",
  host?: Pick<DesktopNavigationHost, "handOffToWorkspaceWindow">,
): void {
  const store = useTabStore.getState();
  if (disposition === "background-tab" || disposition === "foreground-tab") {
    const slug = workspaceSlugForPath(path);
    if (slug && slug !== store.activeWorkspaceSlug) {
      if (!host?.handOffToWorkspaceWindow?.(slug, path)) {
        store.switchWorkspace(slug, path);
      }
      return;
    }
    store.openTab(path, "", {
      activate: disposition === "foreground-tab",
    });
    return;
  }
  if (tryRouteToOtherWorkspace(path, { handOffToWorkspaceWindow: host?.handOffToWorkspaceWindow })) return;
  if (tryRouteToPinnedNewTab(path)) return;
  store.navigateActiveSession(path);
}

/**
 * A desktop deep link explicitly targets its URL, including a new comment.
 * Hands off to the window already showing the workspace when there is one;
 * otherwise switches workspace (or foreground-opens the issue in place) and
 * then applies the address through the session's history flow so a reused
 * tab's view also updates.
 */
export function routeIssueDeepLinkPath(
  path: string,
  host?: Pick<DesktopNavigationHost, "handOffToWorkspaceWindow">,
): void {
  const store = useTabStore.getState();
  const slug = workspaceSlugForPath(path);
  if (!slug) return;
  if (slug !== store.activeWorkspaceSlug) {
    if (host?.handOffToWorkspaceWindow?.(slug, path, { navigate: true })) return;
    store.switchWorkspace(slug, path);
  } else {
    store.openTab(path, "", { activate: true });
  }
  // Ordinary opens restore a reused tab's view. A deep link must also apply
  // this address after deduplication, through the session's history flow.
  useTabStore.getState().navigateActiveSession(path);
}
