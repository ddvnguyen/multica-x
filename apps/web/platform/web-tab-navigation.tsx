"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import {
  NavigationProvider,
  type LinkClickIntent,
  type NavigationAdapter,
} from "@multica/views/navigation";
import { workspaceSlugForPath } from "@multica/core/paths";
import {
  chatSessionIdForUrl,
  findTabForUrl,
  getActiveTab,
  sanitizeTabPath,
  useTabStore,
} from "@multica/core/tabs";
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
 * The address bar's fragment, as state.
 *
 * `usePathname`/`useSearchParams` do not report the hash and do not re-render
 * on an in-page anchor click, so reading `window.location.hash` during render
 * missed it: the session kept the pre-anchor URL and the next projection
 * dropped the fragment (a comment deeplink lost its target on tab switch).
 */
function useAddressBarHash(): string {
  const [hash, setHash] = useState("");
  useEffect(() => {
    const sync = () => setHash(window.location.hash);
    sync();
    window.addEventListener("hashchange", sync);
    window.addEventListener("popstate", sync);
    return () => {
      window.removeEventListener("hashchange", sync);
      window.removeEventListener("popstate", sync);
    };
  }, []);
  return hash;
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
  const hash = useAddressBarHash();
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
  //
  // Runs once, after the workspace list arrives. The address bar is not just
  // a workspace hint: with several restored tabs, the tab the URL names has to
  // become the active one, or the first router commit below records the URL on
  // whichever tab happened to be persisted as active and the reload lands the
  // page in the wrong tab.
  const seededRef = useRef(false);
  useEffect(() => {
    if (validSlugs.length === 0) return;
    if (transitionPath(pathname)) return;
    if (seededRef.current) return;
    // A projection in flight owns the location until the router commits it.
    if (pendingProjection.current !== null) return;
    seededRef.current = true;
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
    if (!target) return;
    const group = current.byWorkspace[target];
    const named = urlSlug === target ? url : undefined;
    if (!group || target !== current.activeWorkspaceSlug) {
      // Entering the workspace (or reviving it) is the store's call: it seeds
      // a group with the address bar's URL, or opens it as a tab.
      current.switchWorkspace(target, named);
      return;
    }
    // Same workspace as the address bar: a URL that names one of the restored
    // tabs activates that tab. A URL that names nothing is an external
    // navigation, and the router → store effect below records it on the active
    // session — adding a tab here would resurrect every stale deep link as an
    // extra tab beside the restored ones.
    const clean = named ? sanitizeTabPath(named) : null;
    if (!clean) return;
    const match = findTabForUrl(group, clean);
    if (match) current.setActiveTab(match.id);
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
    //
    // The active session may name another workspace than the address bar: the
    // rail's workspace link, a tab click on a tab of another workspace, or a
    // cross-workspace content link all switch the store group and leave the
    // router to follow. Guarding on "same workspace as the address bar" made
    // exactly those clicks leave the page on the old workspace (M-413 round 2).
    // A slug the user cannot access is not a destination: the seed effect
    // drops such groups.
    const targetSlug = workspaceSlugForPath(activeUrl);
    if (!targetSlug) return;
    if (validSlugs.length > 0 && !validSlugs.includes(targetSlug)) return;
    projectUrl(activeUrl, url);
    router.replace(activeUrl);
  }, [activeUrl, pathname, url, router, validSlugs]);

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
 * - back/forward/canGoBack walk the window's one history (the previous
 *   location across tabs, focusing the tab that holds it), falling back to
 *   the browser's own history for the page that was open before the shell;
 * - a chat session is one tab: a navigation that names a session an open tab
 *   already shows focuses that tab instead of pointing this one at a second
 *   copy of the same conversation;
 * - `openInNewTab` writes through the store so modifier-clicks and
 *   "open in new tab" CTAs open product tabs; cross-workspace opens also
 *   switch workspace for the same focus reason;
 * - pin-originated pushes force a new tab, matching the desktop adapter.
 *
 * Browser history chords stay native: back/forward map to the session stack
 * (what the shell's back button reads), so the browser's own back button —
 * which pops the real history the sync wrote with replace — may need an
 * extra press to leave a long tab session.
 *
 * The provider sits above the whole dashboard shell (the strip is a layout
 * header, not part of the page), so it must not add a Suspense boundary of its
 * own: a route commit would replace the entire chrome — strip, rail and all —
 * with the fallback. Route pages bring their own boundaries for
 * `useSearchParams`.
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
  // extends with replace. Walk the window's history first — the previous
  // location across tabs, focusing that tab — and fall back to the browser
  // (and its in-app guard) when the shell's history has nothing behind it.
  const back = useMemo(
    () => () => {
      if (useTabStore.getState().goBackInWindow()) return;
      if (canGoBackInApp()) router.back();
    },
    [router],
  );

  const adapter: NavigationAdapter = useMemo(() => {
    const location = splitLocation(activeUrl ?? "/");
    return {
      push: (path: string) => {
        const store = useTabStore.getState();
        if (focusOpenChatSession(store, path)) return;
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
        if (focusOpenChatSession(store, path)) return;
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
        useTabStore.getState().goForwardInWindow();
      },
      canGoBack: () =>
        useTabStore.getState().canGoBackInWindow() || canGoBackInApp(),
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
    <NavigationProvider value={adapter}>{children}</NavigationProvider>
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
 * One session is one tab (M-413 round 4). A chat navigation that names a
 * session an open tab already shows focuses that tab — the list row, the
 * strip's history, a chip, a deep link — instead of pointing the current tab
 * at a second copy of the same conversation.
 *
 * Returns false when no tab holds the session (the caller navigates normally)
 * or when the tab that holds it is already the active one (nothing to do).
 */
function focusOpenChatSession(
  store: ReturnType<typeof useTabStore.getState>,
  path: string,
): boolean {
  const sessionId = chatSessionIdForUrl(path);
  if (!sessionId) return false;
  const slug = workspaceSlugForPath(path);
  if (!slug || slug !== store.activeWorkspaceSlug) return false;
  const group = store.byWorkspace[slug];
  if (!group) return false;
  const match = findTabForUrl(group, path);
  if (!match) return false;
  if (match.id !== group.activeTabId) store.setActiveTab(match.id);
  return true;
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
