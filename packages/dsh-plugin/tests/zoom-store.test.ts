// @vitest-environment node
import { describe, expect, it } from "vitest";
import { createIslandStorage, STORAGE_PREFIX } from "../src/client/storage";
import {
  createZoomStore,
  DEFAULT_ZOOM,
  normalizeZoom,
  readZoom,
  ZOOM_STORAGE_KEY,
} from "../src/client/zoom-store";

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

describe("normalizeZoom", () => {
  it("keeps a supported level", () => {
    for (const level of [80, 90, 100, 110, 125, 150]) {
      expect(normalizeZoom(level)).toBe(level);
      expect(normalizeZoom(String(level))).toBe(level);
    }
  });

  it("clamps anything else to the nearest level", () => {
    expect(normalizeZoom(84)).toBe(80);
    expect(normalizeZoom(140)).toBe(150);
    expect(normalizeZoom(0)).toBe(80);
    expect(normalizeZoom(400)).toBe(150);
  });

  it("reads an unreadable value as the default", () => {
    expect(normalizeZoom("")).toBe(DEFAULT_ZOOM);
    expect(normalizeZoom("large")).toBe(DEFAULT_ZOOM);
    expect(normalizeZoom(null)).toBe(DEFAULT_ZOOM);
    expect(normalizeZoom(Number.NaN)).toBe(DEFAULT_ZOOM);
  });
});

describe("createZoomStore", () => {
  it("starts at 100 % on a device that never chose one", () => {
    const store = createZoomStore(createIslandStorage(memoryBacking()));
    expect(store.get()).toBe(100);
  });

  it("persists the level under the island's prefix", () => {
    const backing = memoryBacking();
    const store = createZoomStore(createIslandStorage(backing));

    store.set(125);

    expect(store.get()).toBe(125);
    expect(backing.getItem(`${STORAGE_PREFIX}${ZOOM_STORAGE_KEY}`)).toBe("125");
  });

  it("reads the stored level back on the next mount", () => {
    const backing = memoryBacking();
    createZoomStore(createIslandStorage(backing)).set(150);

    const reopened = createZoomStore(createIslandStorage(backing));
    expect(reopened.get()).toBe(150);
  });

  it("notifies subscribers once per real change and not for a repeat", () => {
    const store = createZoomStore(createIslandStorage(memoryBacking()));
    let calls = 0;
    const stop = store.subscribe(() => {
      calls += 1;
    });

    store.set(110);
    store.set(110);
    store.set(125);

    expect(calls).toBe(2);
    stop();
    store.set(100);
    expect(calls).toBe(2);
  });

  it("treats an unsupported stored value as the default", () => {
    const backing = memoryBacking();
    backing.setItem(`${STORAGE_PREFIX}${ZOOM_STORAGE_KEY}`, "huge");

    expect(readZoom(createIslandStorage(backing))).toBe(DEFAULT_ZOOM);
  });
});
