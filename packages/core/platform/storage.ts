import type { StorageAdapter } from "../types/storage";

/**
 * The backing adapter `defaultStorage` routes through, when a host supplies
 * one. Null means the browser store below is the behaviour.
 */
let backing: StorageAdapter | null = null;

// Paired module vars: the listeners module-scope persist stores registered,
// and the pending-notification flag. Always set/cleared together.
const _adapterListeners = new Set<() => void>();
let _pendingAdapterNotify = false;

/**
 * Point the default adapter at a host's own store.
 *
 * `defaultStorage` is bound at module scope, and so are the persist stores
 * built on it (issue view states, the surface registry) — a host cannot pass
 * its own adapter into them one by one. The web and the desktop never call
 * this: their origin is their own, and the direct `localStorage` paths below
 * are the whole behaviour. The dsh island points it at its namespaced adapter
 * once at boot (from `CoreProvider`'s storage), so every persisted width and
 * preference lands under `multica.dsh.*` instead of bare keys the shell shares
 * (M-360).
 */
export function setDefaultStorage(adapter: StorageAdapter | null): void {
  // Passing `defaultStorage` itself means "back to the browser store" — it
  // must not be routed through the property, which would recurse.
  const next = adapter === defaultStorage ? null : adapter;
  if (backing === next) return;
  backing = next;
  // Deferred exactly like the workspace rehydrations (workspace-storage.ts):
  // the swap runs inside CoreProvider's first render, and a rehydrate's
  // setState must not land in the middle of it.
  if (_pendingAdapterNotify) return;
  _pendingAdapterNotify = true;
  queueMicrotask(() => {
    _pendingAdapterNotify = false;
    for (const listener of _adapterListeners) listener();
  });
}

/**
 * Subscribe to a host installing (or resetting) the default adapter.
 *
 * Module-scope persist stores hydrate when their module evaluates — before any
 * host has had a chance to supply its adapter — so a store built on
 * `defaultStorage` reads the browser store first, and a host whose keys live
 * elsewhere (the dsh island's `multica.dsh.*` namespace) leaves that first
 * read empty. Registering here lets such a store re-read its persisted state
 * the moment the adapter lands: the accommodation workspace-scoped stores
 * already get from `registerForWorkspaceRehydration` (workspace-storage.ts).
 *
 * @returns an unsubscribe function.
 */
export function onDefaultStorageAdapterChanged(listener: () => void): () => void {
  _adapterListeners.add(listener);
  return () => {
    _adapterListeners.delete(listener);
  };
}

/** SSR-safe localStorage. Works in both Next.js (SSR) and Electron (always client). */
export const defaultStorage: StorageAdapter = {
  getItem: (k) => browser().getItem(k),
  setItem: (k, v) => {
    browser().setItem(k, v);
  },
  removeItem: (k) => {
    browser().removeItem(k);
  },
  keys: () => {
    if (backing !== null) return backing.keys?.() ?? browserKeys();
    return browserKeys();
  },
};

/** The browser store, or the host's when it supplied one. */
function browser(): StorageAdapter {
  return backing ?? windowStore();
}

function browserKeys(): string[] {
  if (typeof window === "undefined") return [];
  return Object.keys(window.localStorage);
}

let windowStoreAdapter: StorageAdapter | null = null;

function windowStore(): StorageAdapter {
  if (windowStoreAdapter !== null) return windowStoreAdapter;
  if (typeof window === "undefined") {
    // Match the SSR contract the methods had before hosts existed: reads are
    // null, writes are dropped, and the key list is empty.
    const absent: StorageAdapter = {
      getItem: () => null,
      setItem: () => {
        // Storage does not exist on the server.
      },
      removeItem: () => {
        // Storage does not exist on the server.
      },
    };
    return absent;
  }
  const store = window.localStorage;
  windowStoreAdapter = {
    getItem: (k) => store.getItem(k),
    setItem: (k, v) => {
      store.setItem(k, v);
    },
    removeItem: (k) => {
      store.removeItem(k);
    },
    keys: () => Object.keys(store),
  };
  return windowStoreAdapter;
}
