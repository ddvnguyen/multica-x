import { pickLocale, type LocaleAdapter } from "../fork";
import type { IslandStorage } from "./storage";

/**
 * The island's language preference.
 *
 * dsh and Multica share an origin but not a language: the shell has its own
 * setting, and the panel must not write a Multica cookie into it (`storage.ts`
 * explains why every key is namespaced). So the island keeps the reader's
 * choice in its own storage and resolves it with the fork's `pickLocale`, the
 * same function the web and desktop adapters go through — one resolution rule
 * for all three hosts.
 *
 * Without an adapter the fork's `useLocaleAdapter` throws, which is what made
 * Settings → Preferences render as the error boundary (M-154).
 */

/** Storage key under the island's `multica.dsh.` prefix. */
export const LOCALE_STORAGE_KEY = "locale";

export function createIslandLocaleAdapter(storage: IslandStorage): LocaleAdapter {
  return {
    getUserChoice() {
      return storage.getItem(LOCALE_STORAGE_KEY);
    },
    getSystemPreferences() {
      if (typeof navigator === "undefined") return [];
      const languages = navigator.languages ?? [];
      return languages.length > 0 ? [...languages] : [navigator.language];
    },
    persist(locale) {
      storage.setItem(LOCALE_STORAGE_KEY, locale);
    },
  };
}

/** The locale this panel renders in: the stored choice, else the shell's. */
export function resolveIslandLocale(
  adapter: LocaleAdapter,
): ReturnType<typeof pickLocale> {
  return pickLocale(adapter);
}
