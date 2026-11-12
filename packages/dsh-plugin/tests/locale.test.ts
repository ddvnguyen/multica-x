// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createIslandLocaleAdapter,
  LOCALE_STORAGE_KEY,
  resolveIslandLocale,
} from "../src/client/locale";
import { createIslandStorage, STORAGE_PREFIX } from "../src/client/storage";

function memoryBacking() {
  const values = new Map<string, string>();
  return {
    values,
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => {
      values.set(key, value);
    },
    removeItem: (key: string) => {
      values.delete(key);
    },
    key: (index: number) => [...values.keys()][index] ?? null,
    get length() {
      return values.size;
    },
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("createIslandLocaleAdapter", () => {
  it("follows the shell's languages until the reader picks one", () => {
    vi.stubGlobal("navigator", { languages: ["fr-CA", "en"], language: "fr-CA" });
    const adapter = createIslandLocaleAdapter(createIslandStorage(memoryBacking()));

    expect(adapter.getUserChoice()).toBeNull();
    expect(resolveIslandLocale(adapter)).toBe("fr");
  });

  it("prefers the stored choice over the shell's language", () => {
    vi.stubGlobal("navigator", { languages: ["en-US"], language: "en-US" });
    const backing = memoryBacking();
    const adapter = createIslandLocaleAdapter(createIslandStorage(backing));

    adapter.persist("ja");

    expect(adapter.getUserChoice()).toBe("ja");
    expect(resolveIslandLocale(adapter)).toBe("ja");
    // Namespaced like every other island preference: dsh's origin is shared.
    expect(backing.getItem(`${STORAGE_PREFIX}${LOCALE_STORAGE_KEY}`)).toBe("ja");
  });

  it("resolves an unreadable stored value the way the fork does", () => {
    vi.stubGlobal("navigator", { languages: ["ko-KR"], language: "ko-KR" });
    const backing = memoryBacking();
    backing.setItem(`${STORAGE_PREFIX}${LOCALE_STORAGE_KEY}`, "not-a-locale");

    // The adapter hands the stored string to `pickLocale` exactly as the web
    // and desktop adapters hand over their cookie: the fork's matcher decides,
    // and an unmatched tag is its default rather than a second rule here.
    const adapter = createIslandLocaleAdapter(createIslandStorage(backing));
    expect(resolveIslandLocale(adapter)).toBe("en");
  });

  it("uses navigator.language when the browser reports no language list", () => {
    vi.stubGlobal("navigator", { languages: [], language: "zh-CN" });
    const adapter = createIslandLocaleAdapter(createIslandStorage(memoryBacking()));

    expect(resolveIslandLocale(adapter)).toBe("zh-Hans");
  });
});
