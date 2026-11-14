/**
 * Where the fork keeps device-local UI state that is not a zustand store.
 *
 * Two things write it: the resizable panel layouts (`react-resizable-panels`'
 * `useDefaultLayout`, called by the issue, project, object and agent-builder
 * pages) and the rich table's column widths. Both are device preferences, so
 * the browser's own `localStorage` is the right home on web and desktop — that
 * origin belongs to Multica alone.
 *
 * A host that shares its origin with another app must not write there: the dsh
 * island sits beside the shell's own storage, so it installs a namespaced
 * adapter with `setDeviceStorage` and every one of these keys follows it. This
 * is the same rule `SidebarProvider`'s `storage` prop follows for the column
 * widths (M-330); this covers the writes that are not on that prop's path
 * (M-336, phase 2).
 */

/** The slice of `Storage` both writers need. */
export interface DeviceStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

let installed: DeviceStorage | null = null;
let overridden = false;

/**
 * Install the host's storage. `null` keeps device state for this page only —
 * the same meaning `SidebarProvider`'s `storage={null}` has.
 */
export function setDeviceStorage(next: DeviceStorage | null): void {
  installed = next;
  overridden = true;
}

/** The browser's own store, or `null` where there is none (SSR, blocked). */
function browserStorage(): DeviceStorage | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

/** The store device preferences go to. Read per call: a host may install late. */
export function deviceStorage(): DeviceStorage | null {
  return overridden ? installed : browserStorage();
}

/**
 * The host's adapter, or `undefined` when none is installed.
 *
 * For a caller whose own default is already the browser's store — the panel
 * library defaults `storage` to `localStorage` — passing nothing is the exact
 * behaviour web and desktop had before the island needed an adapter: no
 * argument, no wrapper, the library's own path.
 */
export function deviceStorageOverride(): DeviceStorage | undefined {
  return overridden ? (installed ?? undefined) : undefined;
}

/** Drop the host's adapter and go back to the browser's own store. */
export function resetDeviceStorage(): void {
  installed = null;
  overridden = false;
}
