import { createStore, type StoreApi } from "zustand/vanilla";
import { createJSONStorage, persist } from "zustand/middleware";
import type { IslandStorage } from "./storage";
import {
  FAMILY_ROOTS,
  islandParent,
  islandPath,
  parseIslandRoute,
  type IslandRoute,
} from "./navigation";

export const TABS_CAP = 20;
export const HISTORY_CAP = 50;

export interface IslandTab {
  id: string;
  url: string;
  history: { stack: string[]; index: number };
}

export interface TabsGroup {
  tabs: IslandTab[];
  activeId: string | null;
  mru: string[];
}

export type TabsState = TabsGroup & {
  // tab lifecycle
  openTab: (url: string, options?: { activate?: boolean; insertAfterActive?: boolean }) => string;
  open: (url: string, options?: { activate?: boolean; insertAfterActive?: boolean }) => string;
  focusTab: (id: string) => void;
  focus: (id: string) => void;
  closeTab: (id: string) => void;
  close: (id: string) => void;
  // rail entry handling
  railEntry: (entry: IslandRoute | string) => void;
  onRailEntry: (entry: IslandRoute | string) => void;
  openOrFocus: (url: string) => string;
  openOrFocusUrl: (url: string) => string;
  // history on the active tab
  push: (path: string) => void;
  replace: (path: string) => void;
  back: () => boolean;
  forward: () => boolean;
  canGoBack: (route?: IslandRoute | null) => boolean;
  // helpers
  activeTab: () => IslandTab | null;
  activeUrl: () => string | null;
};

function newId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `tab-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

export function familyOfRoute(route: IslandRoute): string {
  switch (route.view) {
    case "board":
    case "issue":
      return "issues";
    case "agents":
    case "agent":
      return "agents";
    case "skills":
    case "skill":
      return "skills";
    case "inbox":
      return "inbox";
    case "chat":
      return "chat";
    case "settings":
      return "settings";
  }
}

export function familyOfUrl(url: string): string | null {
  const route = parseIslandRoute(url);
  if (route === null) return null;
  return familyOfRoute(route);
}

function routeFromUrl(url: string): IslandRoute | null {
  return parseIslandRoute(url);
}

export interface CreateTabsStoreOptions {
  storage: IslandStorage;
  /** Workspace whose paths this store builds; not part of the persist key (M-173). */
  slug: string;
}

export function createTabsStore(
  storageOrOptions: IslandStorage | CreateTabsStoreOptions,
  slugArg?: string,
): StoreApi<TabsState> {
  const storage =
    (storageOrOptions as CreateTabsStoreOptions).storage !== undefined
      ? (storageOrOptions as CreateTabsStoreOptions).storage
      : (storageOrOptions as IslandStorage);
  const slug =
    (storageOrOptions as CreateTabsStoreOptions).slug !== undefined
      ? (storageOrOptions as CreateTabsStoreOptions).slug
      : (slugArg as string);

  // One workspace, one tab set (M-173): the key dropped its per-slug suffix,
  // so a second history can never be stranded behind a slug change.
  const persistKey = "tabs.v1";

  const slice = (
    set: (partial: Partial<TabsGroup> | ((s: TabsGroup) => Partial<TabsGroup>)) => void,
    get: () => TabsState,
  ): TabsGroup & Omit<TabsState, keyof TabsGroup> => {
    const base: TabsGroup = { tabs: [], activeId: null, mru: [] };

    const update = (fn: (s: TabsGroup) => TabsGroup) => {
      const current: TabsGroup = { tabs: get().tabs, activeId: get().activeId, mru: get().mru };
      const next = fn(current);
      set(next);
    };

    const focusId = (id: string) => {
      update((s) => {
        const mru = [id, ...s.mru.filter((x) => x !== id)];
        return { ...s, activeId: id, mru };
      });
    };

    const evictIfNeeded = (s: TabsGroup): TabsGroup => {
      if (s.tabs.length < TABS_CAP) return s;
      // find least recently used unpinned tab that is not active
      const candidates = [...s.mru].reverse();
      let victim: string | null = null;
      for (const id of candidates) {
        if (id === s.activeId) continue;
        if (s.tabs.some((t) => t.id === id)) {
          victim = id;
          break;
        }
      }
      if (victim === null) {
        // fall back to oldest tab not active
        for (const t of s.tabs) {
          if (t.id !== s.activeId) {
            victim = t.id;
            break;
          }
        }
      }
      if (victim === null) return s;
      return {
        tabs: s.tabs.filter((t) => t.id !== victim),
        activeId: s.activeId === victim ? (s.tabs.find((t) => t.id !== victim)?.id ?? null) : s.activeId,
        mru: s.mru.filter((x) => x !== victim),
      };
    };

    const openTab = (url: string, options?: { activate?: boolean; insertAfterActive?: boolean }) => {
      const activate = options?.activate !== false;
      const insertAfterActive = options?.insertAfterActive !== false;
      const id = newId();
      const tab: IslandTab = { id, url, history: { stack: [url], index: 0 } };
      update((s) => {
        const next = evictIfNeeded(s);
        const activeIndex = next.tabs.findIndex((t) => t.id === next.activeId);
        const insertAt = insertAfterActive && activeIndex >= 0 ? activeIndex + 1 : next.tabs.length;
        const tabs = [...next.tabs];
        tabs.splice(insertAt, 0, tab);
        const mru = activate ? [id, ...next.mru.filter((x) => x !== id)] : next.mru;
        const activeId = activate ? id : next.activeId;
        return { tabs, activeId, mru };
      });
      return id;
    };

    const closeTab = (id: string) => {
      update((s) => {
        const tabs = s.tabs.filter((t) => t.id !== id);
        const mru = s.mru.filter((x) => x !== id);
        let activeId = s.activeId;
        if (activeId === id) {
          // most recently used other tab
          const nextMru = mru.find((x) => tabs.some((t) => t.id === x));
          activeId = nextMru ?? (tabs.length ? tabs[tabs.length - 1]!.id : null);
        }
        // close-last: open family root of closed tab
        if (tabs.length === 0) {
          const closed = s.tabs.find((t) => t.id === id);
          const fam = closed ? familyOfUrl(closed.url) : null;
          let rootUrl = closed ? closed.url : `/${slug}/issues`;
          if (fam !== null) {
            // map family to root url
            const rootRoute = Object.entries(FAMILY_ROOTS).find(([f]) => f === fam)?.[1] as IslandRoute | undefined;
            if (rootRoute) rootUrl = islandPath(slug, rootRoute);
          }
          const newTab: IslandTab = { id: newId(), url: rootUrl, history: { stack: [rootUrl], index: 0 } };
          tabs.push(newTab);
          activeId = newTab.id;
          mru.unshift(newTab.id);
        }
        return { tabs, activeId, mru };
      });
    };

    const openOrFocus = (url: string) => {
      const existing = get().tabs.find((t) => t.url === url);
      if (existing) {
        focusId(existing.id);
        return existing.id;
      }
      return openTab(url, { activate: true });
    };

    const railEntry = (entry: IslandRoute | string) => {
      const route: IslandRoute | null =
        typeof entry === "string" ? parseIslandRoute(entry) : entry;
      if (route === null) return;
      const family = familyOfRoute(route);
      const entryUrl = typeof entry === "string" ? entry : islandPath(slug, entry);
      update((s) => {
        const active = s.tabs.find((t) => t.id === s.activeId);
        if (active && familyOfUrl(active.url) === family) {
          return s; // keep
        }
        // focus MRU tab of family
        const familyTabIds = s.tabs.filter((t) => familyOfUrl(t.url) === family).map((t) => t.id);
        const mruFamily = s.mru.filter((id) => familyTabIds.includes(id));
        if (mruFamily.length > 0) {
          const id = mruFamily[0]!;
          const mru = [id, ...s.mru.filter((x) => x !== id)];
          return { ...s, activeId: id, mru };
        }
        // open new tab at E
        // reuse openTab logic inline to avoid recursion issues
        const next = s;
        if (next.tabs.length >= TABS_CAP) {
          // evict
          // simple: reuse evictIfNeeded by reconstructing
        }
        const id = newId();
        const tab: IslandTab = { id, url: entryUrl, history: { stack: [entryUrl], index: 0 } };
        // evict if needed
        let tabs = next.tabs;
        let activeId = next.activeId;
        let mru = next.mru;
        if (tabs.length >= TABS_CAP) {
          // find victim
          const candidates = [...mru].reverse();
          let victim: string | null = null;
          for (const cid of candidates) {
            if (cid === activeId) continue;
            if (tabs.some((t) => t.id === cid)) {
              victim = cid;
              break;
            }
          }
          if (victim === null) {
            for (const t of tabs) {
              if (t.id !== activeId) {
                victim = t.id;
                break;
              }
            }
          }
          if (victim !== null) {
            tabs = tabs.filter((t) => t.id !== victim);
            mru = mru.filter((x) => x !== victim);
            if (activeId === victim) activeId = tabs.length ? tabs[0]!.id : null;
          }
        }
        const activeIndex = tabs.findIndex((t) => t.id === activeId);
        const insertAt = activeIndex >= 0 ? activeIndex + 1 : tabs.length;
        const newTabs = [...tabs];
        newTabs.splice(insertAt, 0, tab);
        mru = [id, ...mru.filter((x) => x !== id)];
        activeId = id;
        return { tabs: newTabs, activeId, mru };
      });
    };

    const push = (path: string) => {
      update((s) => {
        const active = s.tabs.find((t) => t.id === s.activeId);
        if (!active) return s;
        const hist = active.history;
        const stack = [...hist.stack.slice(0, hist.index + 1), path];
        // cap
        let index = stack.length - 1;
        let finalStack = stack;
        if (finalStack.length > HISTORY_CAP) {
          const overflow = finalStack.length - HISTORY_CAP;
          finalStack = finalStack.slice(overflow);
          index = Math.max(0, index - overflow);
        }
        const tabs = s.tabs.map((t) =>
          t.id === s.activeId ? { ...t, url: path, history: { stack: finalStack, index } } : t,
        );
        return { ...s, tabs };
      });
    };

    const replace = (path: string) => {
      update((s) => {
        const active = s.tabs.find((t) => t.id === s.activeId);
        if (!active) return s;
        const hist = active.history;
        const stack = [...hist.stack];
        if (stack.length === 0) stack.push(path);
        else stack[hist.index] = path;
        const tabs = s.tabs.map((t) =>
          t.id === s.activeId ? { ...t, url: path, history: { stack, index: hist.index } } : t,
        );
        return { ...s, tabs };
      });
    };

    const back = (): boolean => {
      const s = get();
      const active = s.tabs.find((t) => t.id === s.activeId);
      if (!active) return false;
      if (active.history.index > 0) {
        update((st) => {
          const tabs = st.tabs.map((t) =>
            t.id === st.activeId ? { ...t, history: { ...t.history, index: t.history.index - 1 }, url: t.history.stack[t.history.index - 1]! } : t,
          );
          return { ...st, tabs };
        });
        return true;
      }
      return false;
    };

    const forward = (): boolean => {
      const s = get();
      const active = s.tabs.find((t) => t.id === s.activeId);
      if (!active) return false;
      if (active.history.index < active.history.stack.length - 1) {
        update((st) => {
          const tabs = st.tabs.map((t) =>
            t.id === st.activeId ? { ...t, history: { ...t.history, index: t.history.index + 1 }, url: t.history.stack[t.history.index + 1]! } : t,
          );
          return { ...st, tabs };
        });
        return true;
      }
      return false;
    };

    const canGoBackFn = (route?: IslandRoute | null) => {
      const s = get();
      const active = s.tabs.find((t) => t.id === s.activeId);
      const index = active?.history.index ?? 0;
      if (index > 0) return true;
      const r = route ?? (active ? routeFromUrl(active.url) : null);
      return r !== null && islandParent(r) !== null;
    };

    const activeTab = () => get().tabs.find((t) => t.id === get().activeId) ?? null;
    const activeUrl = () => activeTab()?.url ?? null;

    const actions = {
      openTab,
      open: openTab,
      focusTab: focusId,
      focus: focusId,
      closeTab,
      close: closeTab,
      railEntry,
      onRailEntry: railEntry,
      openOrFocus,
      openOrFocusUrl: openOrFocus,
      push,
      replace,
      back,
      forward,
      canGoBack: canGoBackFn,
      activeTab,
      activeUrl,
    };

    return { ...base, ...actions };
  };

  const store = createStore<TabsState>()(
    persist(slice as any, {
      name: persistKey,
      storage: createJSONStorage(() => storage),
      partialize: (state: TabsState) => ({
        tabs: state.tabs,
        activeId: state.activeId,
        mru: state.mru,
      }),
    }),
  );

  return store;
}

// aliases
export const createIslandTabs = createTabsStore;
export const createIslandTabsStore = createTabsStore;
export const createTabStore = createTabsStore;
