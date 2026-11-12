/**
 * The island's browser storage.
 *
 * dsh and Multica share one origin, so the island must not write Multica's
 * unprefixed keys into it: a `multica_*` key would be visible to the shell, and
 * a later Multica web session on that origin would read it. Every key is
 * therefore namespaced, per-workspace suffixes included, and the adapter keeps
 * only the small set of UI preferences the fork persists.
 */

/** Prefix for every key this adapter writes. */
export const STORAGE_PREFIX = "multica.dsh.";

export interface IslandStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
  keys?(): string[];
}

export function createIslandStorage(
  backing: Pick<Storage, "getItem" | "setItem" | "removeItem" | "key" | "length"> | null = safeLocalStorage(),
): IslandStorage {
  return {
    getItem(key) {
      try {
        return backing?.getItem(STORAGE_PREFIX + key) ?? null;
      } catch {
        return null;
      }
    },
    setItem(key, value) {
      try {
        backing?.setItem(STORAGE_PREFIX + key, value);
      } catch {
        // A full or disabled store is not a reason to break the panel; the
        // preference simply does not survive this page.
      }
    },
    removeItem(key) {
      try {
        backing?.removeItem(STORAGE_PREFIX + key);
      } catch {
        // See setItem.
      }
    },
    keys() {
      if (backing === null) return [];
      const found: string[] = [];
      try {
        for (let index = 0; index < backing.length; index += 1) {
          const name = backing.key(index);
          if (name !== null && name.startsWith(STORAGE_PREFIX)) {
            found.push(name.slice(STORAGE_PREFIX.length));
          }
        }
      } catch {
        return found;
      }
      return found;
    },
  };
}

function safeLocalStorage(): Storage | null {
  try {
    return typeof window === "undefined" ? null : window.localStorage;
  } catch {
    return null;
  }
}
