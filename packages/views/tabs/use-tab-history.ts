import { useCallback, useMemo } from "react";
import {
  browsingHistoryKeyForUrl,
  useTabStore,
  useActiveBrowsingHistory,
  useActiveBrowsingHistoryTitles,
  useActiveTabHistory,
  useWindowHistory,
} from "@multica/core/tabs";

export interface BrowsingHistoryEntry {
  url: string;
  title?: string;
}

/**
 * Shell back/forward for the active tab (MUL-4741 session architecture).
 *
 * Per-tab history is a virtual stack on the tab session — the single app
 * router has no usable history of its own (the Coordinator always navigates
 * with replace). goBack/goForward move the session's history index; the
 * Coordinator then reconciles the router to the newly projected URL. No
 * direction hints, no router.navigate(±1).
 *
 * Desktop keeps this model. The web strip walks the window's history instead
 * (`useWindowTabHistory`): its Back/Forward are the browser's, and a browser
 * does not stop at the edge of a tab.
 */
export function useTabHistory() {
  const { historyIndex, historyLength, historyEntries } = useActiveTabHistory();
  const browsingHistoryUrls = useActiveBrowsingHistory();
  const browsingHistoryTitles = useActiveBrowsingHistoryTitles();
  const browsingHistory = useMemo<BrowsingHistoryEntry[]>(
    () =>
      browsingHistoryUrls.map((url) => ({
        url,
        title: browsingHistoryTitles[browsingHistoryKeyForUrl(url)],
      })),
    [browsingHistoryTitles, browsingHistoryUrls],
  );

  const canGoBack = historyIndex > 0;
  const canGoForward = historyIndex < historyLength - 1;

  const goBack = useCallback(() => {
    useTabStore.getState().goBack();
  }, []);

  const goForward = useCallback(() => {
    useTabStore.getState().goForward();
  }, []);

  const goToHistoryIndex = useCallback((index: number) => {
    useTabStore.getState().goToHistoryIndex(index);
  }, []);

  return {
    canGoBack,
    canGoForward,
    historyEntries,
    historyIndex,
    browsingHistory,
    goBack,
    goForward,
    goToHistoryIndex,
  };
}

/** A page with no resolved title still has to read as something. */
function historyEntryLabel(url: string, titles: Map<string, string>): string {
  const titled = titles.get(url);
  if (titled) return titled;
  const path = url.replace(/[?#].*$/, "");
  const segment = path.split("/").filter(Boolean).at(-1);
  return segment && segment !== "issues" ? segment : path;
}

/**
 * The window's one history, for the web strip (M-413 round 4).
 *
 * Back returns to the previous location across tabs — focusing the tab that
 * holds it — instead of stopping at the current tab's first entry. Entries of
 * closed tabs stay in the list (so the index the window stands on does not
 * move) but are never destinations, which is why the same skip rule backs both
 * the disabled state and the move itself.
 *
 * Titles come from every workspace group's browsing history: the entries can
 * span workspaces, and the active group only knows its own.
 */
export function useWindowTabHistory() {
  const { entries, index, canGoBack, canGoForward } = useWindowHistory();
  const byWorkspace = useTabStore((s) => s.byWorkspace);

  const titles = useMemo(() => {
    const map = new Map<string, string>();
    for (const group of Object.values(byWorkspace)) {
      for (const url of group.browsingHistory) {
        const title = group.browsingHistoryTitles[browsingHistoryKeyForUrl(url)];
        if (title && !map.has(url)) map.set(url, title);
      }
    }
    return map;
  }, [byWorkspace]);

  const history = useMemo(
    () =>
      entries.map((entry) => ({
        url: entry.url,
        label: historyEntryLabel(entry.url, titles),
      })),
    [entries, titles],
  );

  const goBack = useCallback(() => {
    useTabStore.getState().goBackInWindow();
  }, []);

  const goForward = useCallback(() => {
    useTabStore.getState().goForwardInWindow();
  }, []);

  const goToHistoryIndex = useCallback((windowIndex: number) => {
    useTabStore.getState().goToWindowHistoryIndex(windowIndex);
  }, []);

  return {
    canGoBack,
    canGoForward,
    history,
    historyIndex: index,
    goBack,
    goForward,
    goToHistoryIndex,
  };
}
