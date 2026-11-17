// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { DEFAULT_APPEARANCE, useAppearanceStore } from "./index";
import { useInstalledThemesStore } from "./installed-themes";
import { setDefaultStorage } from "../platform/storage";
import type { StorageAdapter } from "../types/storage";
import { parseTheme } from "./theme-package";

const flushAdapterChange = () => new Promise<void>((resolve) => queueMicrotask(resolve));

afterEach(async () => {
  setDefaultStorage(null);
  await flushAdapterChange();
  localStorage.clear();
  useAppearanceStore.setState(DEFAULT_APPEARANCE);
  useInstalledThemesStore.setState({ themes: [], folders: {}, fromStore: {} });
});

describe("appearance stores with a late host adapter", () => {
  it("rehydrates appearance and installed themes after switching back to the namespaced adapter", async () => {
    const values = new Map<string, string>();
    const storage: StorageAdapter = {
      getItem: (key) => values.get(key) ?? null,
      setItem: (key, value) => values.set(key, value),
      removeItem: (key) => values.delete(key),
    };
    const parsed = parseTheme({
      manifest: { apiVersion: 1, id: "adapter-test", name: "Adapter Test", author: "Test", version: "1.0.0" },
      css: ".theme-light { --page-canvas: #fff; } .theme-dark { --page-canvas: #000; }",
    });
    if (!parsed.ok) throw new Error("valid theme fixture was rejected");

    setDefaultStorage(storage);
    await flushAdapterChange();
    useAppearanceStore.getState().set({ theme: "pop" });
    useInstalledThemesStore.getState().install(parsed.theme);

    setDefaultStorage(null);
    await flushAdapterChange();
    useAppearanceStore.setState(DEFAULT_APPEARANCE);
    useInstalledThemesStore.setState({ themes: [], folders: {}, fromStore: {} });

    setDefaultStorage(storage);
    await flushAdapterChange();

    expect(useAppearanceStore.getState().theme).toBe("pop");
    expect(useInstalledThemesStore.getState().themes.map(({ id }) => id)).toEqual(["adapter-test"]);
  });
});
