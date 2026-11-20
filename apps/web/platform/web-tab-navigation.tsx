"use client";

import { Suspense, useEffect, useMemo, useRef } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import {
  NavigationProvider,
  type LinkClickIntent,
  type NavigationAdapter,
} from "@multica/views/navigation";
import { workspaceSlugForPath } from "@multica/core/paths";
import { getActiveTab, useTabStore } from "@multica/core/tabs";
import { canGoBackInApp } from "./in-app-history";

/**
 * Web tab router sync: project the active tab session onto Next.js's router.
 *
 * The active tab session is the location. Next.js owns its router, so the
 * sync is bidirectional and effect-driven:
 *
 * - store → router: when the active session changes, `router.replace` to
 *   the session URL (replace, never push — the browser history is not the
 *   session history; per-tab back/forward is the store's virtual stack).
 * - router → store: when the URL changes outside the sync (link click,
 *   address bar, back button, `router.push` from shared views), record it on
 *   the active session via `navigateActiveSession` so the tab keeps its
 *   identity, virtual history, and memento.
 *
 * Transition paths (`/login`, `/onboarding`, `/workspaces/new`, `/invite/…`)
 * are not workspace tab routes: the sync parks while they render and resumes
 * when a workspace route returns.
 *
 * Mounted once per dashboard layout alongside the tab strip, inside the
 * Suspense boundary the App Router requires for `useSearchParams`.
 */

export interface WebTabSyncOptions {
  /** Workspace slugs the user can currently access (workspace list query). */
  validSlugs: readonly string[];
  /** Resolve the launch workspace (deep link, last-workspace cookie). */
  launchSlug?: string | null;
}

function transitionPath(pathname: string): boolean {
  return (
    pathname === "/login" ||
    pathname === "/onboarding" ||
    pathname.startsWith("/workspaces/new") ||
    pathname === "/workspaces/new" ||
    pathname.startsWith("/invite/")
  );
}

function fullUrl(pathname: string, search: string, hash: string): string {
  return `${pathname}${search ? `?${search}` : ""}${hash}`;
}

/**
 * The session URL the sync (or the adapter) last projected onto the Next
 * router and Next.js has not committed yet. While set, the router → store
 * effect ignores the stale address-bar URL instead of recording it as an
 * external navigation that would revert the projection.
 */
/** Last address-bar URL the sync rendered with (module state for adapter writes). */
let lastRenderedUrl: string | null = null;

const pendingProjection: {
  /** Session URL projected onto the router, not yet committed. */
  current: string | null;
  /** Address-bar URL the projection was made against (stale while in flight). */
  from: string | null;
} = { current: null, from: null };

function projectUrl(url: string, from: string): void {
  pendingProjection.current = url;
  pendingProjection.from = from;
}

function clearProjection(): void {
  pendingProjection.current = null;
  pendingProjection.from = null;
}

/** Adapter write: claim `target` as the in-flight projection. */
function markProjection(target: string): void {
  pendingProjection.current = target;
  pendingProjection.from = lastRenderedUrl;
}

/** Test-only: reset the in-flight projection between cases. */
export function resetPendingProjectionForTests(): void {
  clearProjection();
}

/**
 * Keep the active tab session and the Next.js router in sync. Mount once,
 * inside the workspace layout, next to the tab strip.
 */
function useWebTabSyncInner(options: WebTabSyncOptions): void {
  const { validSlugs, launchSlug } = options;
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const search = searchParams.toString();
  const hash = typeof window === "undefined" ? "" : window.location.hash;
  const url = fullUrl(pathname, search, hash);

  const activeUrl = useTabStore((s) => {
    const group = s.activeWorkspaceSlug
      ? s.byWorkspace[s.activeWorkspaceSlug]
      : undefined;
    return group?.tabs.find((t) => t.id === group.activeTabId)?.url ?? null;
  });

  lastRenderedUrl = url;

  // First paint: validate persisted tabs against the workspace list and pick
  // the workspace to show. Slugs the user lost access to are dropped. The
  // current URL wins over the persisted active workspace: a reload, deep
  // link, or new browser tab must open what the address bar says, not the
  // tab another browser tab persisted last.
  useEffect(() => {
    if (validSlugs.length === 0) return;
    if (transitionPath(pathname)) return;
    const store = useTabStore.getState();
    store.validateWorkspaceSlugs(new Set(validSlugs));
    const current = useTabStore.getState();
    const urlSlug = workspaceSlugForPath(url);
    const target =
      (urlSlug && validSlugs.includes(urlSlug) ? urlSlug : null) ??
      (launchSlug && validSlugs.includes(launchSlug) ? launchSlug : null) ??
      (current.activeWorkspaceSlug &&
      validSlugs.includes(current.activeWorkspaceSlug)
        ? current.activeWorkspaceSlug
        : validSlugs[0]);
    if (target && target !== current.activeWorkspaceSlug) {
      current.switchWorkspace(target, urlSlug === target ? url : undefined);
    } else if (target && !current.byWorkspace[target]) {
      current.switchWorkspace(target, urlSlug === target ? url : undefined);
    }
  }, [validSlugs, launchSlug, url, pathname]);

  // store → router: project the active session URL. A ref holding the last
  // rendered session distinguishes a store change (project it) from an
  // external URL change (the router → store effect below owns that commit).
  // The projection target is recorded so the router → store effect does not
  // read the stale address-bar URL and revert it before Next.js commits.
  // Module state (not a ref): the adapter's push/replace write the session
  // without rendering the sync first, so they must be able to mark the same
  // projection the sync's router → store effect consults.
  const prevActiveRef = useRef<string | null | undefined>(undefined);
  useEffect(() => {
    const prev = prevActiveRef.current;
    prevActiveRef.current = activeUrl;
    const projectedRef = pendingProjection;
    // First run this mount: the seed effect above owns initialization.
    if (prev === undefined) return;
    if (!activeUrl) return;
    if (transitionPath(pathname)) return;
    if (activeUrl === url) {
      // The router caught up (or the navigation came from outside the sync):
      // clear a satisfied projection so a later external URL is not ignored.
      if (projectedRef.current === activeUrl) clearProjection();
      return;
    }
    // The URL changed but the session didn't → external navigation; the
    // router → store effect owns this commit.
    if (activeUrl === prev) return;
    // A projection claimed by the adapter (`push`, `replace`, an activating
    // `openInNewTab`) is still ours to finish: those writes only move the
    // session, so the router has to follow here. Skipping them left every
    // in-app click with a tab that never opened the page it named (M-413).
    // `projectUrl` below re-records the projection, so the router → store
    // effect keeps ignoring the stale address-bar URL until Next commits.
    // Sessions never navigate out of their workspace; cross-workspace moves
    // go through switchWorkspace (adapter push) instead.
    if (workspaceSlugForPath(activeUrl) !== workspaceSlugForPath(pathname)) {
      return;
    }
    projectUrl(activeUrl, url);
    router.replace(activeUrl);
  }, [activeUrl, pathname, url, router]);

  // router → store: record navigations the sync did not start. Skipped while
  // a projection is in flight — unless the address bar shows a third URL,
  // which supersedes the projection (external navigation won the race).
  useEffect(() => {
    if (transitionPath(pathname)) return;
    if (activeUrl === url) {
      clearProjection();
      return;
    }
    if (pendingProjection.current !== null) {
      // The address bar still shows the pre-projection URL: Next.js has not
      // committed yet. Ignore it instead of recording it as an external
      // navigation that would revert the projection.
      if (url === pendingProjection.from) return;
      // A third URL supersedes the projection (external navigation won).
      if (url !== pendingProjection.current) clearProjection();
      else return;
    }
    const store = useTabStore.getState();
    const slug = workspaceSlugForPath(url);
    if (!slug) return;
    if (!validSlugs.includes(slug)) return;
    if (slug !== store.activeWorkspaceSlug) {
      store.switchWorkspace(slug, url);
      return;
    }
    store.navigateActiveSession(url);
  }, [url, pathname, activeUrl, validSlugs]);
}

export function useWebTabSync(options: WebTabSyncOptions): void {
  return useWebTabSyncInner(options);
}

/**
 * Tab strip + router sync for one workspace layout. Mount inside the
 * workspace route (where `useSearchParams` has its Suspense boundary).
 */
export function WebTabSync(props: WebTabSyncOptions): null {
  useWebTabSyncInner(props);
  return null;
}

/**
 * Navigation adapter for the tabbed web shell. Same contract the shared
 * views use on desktop, re-implemented over Next.js primitives:
 *
 * - in-workspace push/replace write through the tab session (the sync effect
 *   projects them onto the Next router with replace);
 * - cross-workspace push switches the workspace group (focus follows the
 *   user — a background tab in a non-visible group would give zero feedback);
 * - back/forward/canGoBack read the active session's virtual history;
 * - `openInNewTab` writes through the store so modifier-clicks and
 *   "open in new tab" CTAs open product tabs; cross-workspace opens also
 *   switch workspace for the same focus reason;
 * - pin-originated pushes force a new tab, matching the desktop adapter.
 *
 * Browser history chords stay native: back/forward map to the session stack
 * (what the shell's back button reads), so the browser's own back button —
 * which pops the real history the sync wrote with replace — may need an
 * extra press to leave a long tab session.
 */
export function WebTabNavigationProvider({
  children,
}: {
  children: React.ReactNode;
}) {
  const router = useRouter();
  const activeUrl = useTabStore((s) => {
    const group = s.activeWorkspaceSlug
      ? s.byWorkspace[s.activeWorkspaceSlug]
      : undefined;
    return group?.tabs.find((t) => t.id === group.activeTabId)?.url ?? null;
  });

  // The browser back button pops real history, which the sync only ever
  // extends with replace — so a bare `router.back()` can trap the user
  // inside one tab session. Prefer the session stack when it has somewhere
  // to go; fall back to the browser (and its in-app guard) at index 0.
  const back = useMemo(
    () => () => {
      const before = getActiveTab(useTabStore.getState());
      if (before && before.history.index > 0) {
        useTabStore.getState().goBack();
        return;
      }
      if (canGoBackInApp()) router.back();
    },
    [router],
  );

  const adapter: NavigationAdapter = useMemo(() => {
    const location = splitLocation(activeUrl ?? "/");
    return {
      push: (path: string) => {
        const store = useTabStore.getState();
        if (tryRouteToPinnedNewTab(path)) return;
        const slug = workspaceSlugForPath(path);
        if (slug && slug !== store.activeWorkspaceSlug) {
          markProjection(path);
          store.switchWorkspace(slug, path);
          return;
        }
        markProjection(path);
        store.navigateActiveSession(path);
      },
      replace: (path: string) => {
        const store = useTabStore.getState();
        const slug = workspaceSlugForPath(path);
        if (slug && slug !== store.activeWorkspaceSlug) {
          markProjection(path);
          store.switchWorkspace(slug, path);
          return;
        }
        markProjection(path);
        store.navigateActiveSession(path, { replace: true });
      },
      back,
      forward: () => {
        useTabStore.getState().goForward();
      },
      canGoBack: () => {
        const active = getActiveTab(useTabStore.getState());
        if ((active?.history.index ?? 0) > 0) return true;
        return canGoBackInApp();
      },
      pathname: location.pathname,
      searchParams: new URLSearchParams(location.search),
      hash: location.hash,
      openInNewTab: (
        path: string,
        title?: string,
        opts?: { activate?: boolean },
      ) => {
        const store = useTabStore.getState();
        const slug = workspaceSlugForPath(path);
        // Background tabs never project; only an activating open moves the
        // router, and only the sync (or the activating write) may claim it.
        if (slug && slug !== store.activeWorkspaceSlug) {
          markProjection(path);
          store.switchWorkspace(slug, path);
          return;
        }
        if (opts?.activate) markProjection(path);
        store.openTab(path, title ?? "", { activate: opts?.activate });
      },
      getShareableUrl: (path: string) =>
        typeof window === "undefined" ? path : window.location.origin + path,
      openInDesktop: (path: string) => {
        const url = window.location.origin + path;
        window.location.href = `multica://open/issue?url=${encodeURIComponent(url)}`;
      },
      prefetch: (path: string) => {
        router.prefetch(path);
      },
    };
  }, [activeUrl, back, router]);

  return (
    <NavigationProvider value={adapter}>
      <Suspense>{children}</Suspense>
    </NavigationProvider>
  );
}

function splitLocation(url: string): {
  pathname: string;
  search: string;
  hash: string;
} {
  const hashIdx = url.indexOf("#");
  const hash = hashIdx === -1 ? "" : url.slice(hashIdx);
  const withoutHash = hashIdx === -1 ? url : url.slice(0, hashIdx);
  const searchIdx = withoutHash.indexOf("?");
  if (searchIdx === -1) return { pathname: withoutHash, search: "", hash };
  return {
    pathname: withoutHash.slice(0, searchIdx),
    search: withoutHash.slice(searchIdx),
    hash,
  };
}

/**
 * Pushes originating in a pinned tab become `openInNewTab` so the pinned tab
 * stays parked on its path. Mirrors the desktop adapter's pinned-tab rule
 * (same-pathname search/hash changes stay in place).
 */
function tryRouteToPinnedNewTab(path: string): boolean {
  const store = useTabStore.getState();
  const active = getActiveTab(store);
  if (!active?.pinned) return false;
  const currentPathname = splitLocation(active.url).pathname;
  const newPathname = splitLocation(path).pathname;
  if (currentPathname === newPathname) return false;
  if (active.url === path) return false;
  store.openTab(path, "", { activate: true });
  return true;
}

/** Execute a shared-content link (`multica:navigate` event) in the web shell. */
export function routeWebContentLinkPath(
  path: string,
  disposition: LinkClickIntent = "push",
): void {
  const store = useTabStore.getState();
  if (disposition === "foreground-tab") {
    store.openTab(path, "", { activate: true });
    return;
  }
  if (disposition === "background-tab") {
    store.openTab(path, "");
    return;
  }
  const slug = workspaceSlugForPath(path);
  if (slug && slug !== store.activeWorkspaceSlug) {
    store.switchWorkspace(slug, path);
    return;
  }
  if (tryRouteToPinnedNewTab(path)) return;
  store.navigateActiveSession(path);
}
