// @vitest-environment node
import { describe, expect, it } from "vitest";
import { createIslandStorage } from "../src/client/storage";
import { createTabsStore } from "../src/client/tabs";

function memoryBacking() {
  const values = new Map<string, string>();
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
    removeItem: (key: string) => values.delete(key),
    key: (index: number) => [...values.keys()][index] ?? null,
    get length() {
      return values.size;
    },
  };
}

describe("tabs reuse", () => {
  it("reuses the same tab for the same route twice", () => {
    const storage = createIslandStorage(memoryBacking() as any);
    const store = createTabsStore(storage, "demo");
    const first = store.getState().openTab("/demo/issues");
    const second = store.getState().openOrFocus("/demo/issues");
    expect(second).toBe(first);
    expect(store.getState().tabs).toHaveLength(1);
  });

  it("reuses the same tab for the same route with a query", () => {
    const storage = createIslandStorage(memoryBacking() as any);
    const store = createTabsStore(storage, "demo");
    const first = store.getState().openTab("/demo/issues");
    const second = store.getState().openOrFocus("/demo/issues?filter=a");
    expect(second).toBe(first);
    expect(store.getState().tabs).toHaveLength(1);
  });

  it("gives each chat session its own tab (M-413 round 4)", () => {
    const storage = createIslandStorage(memoryBacking() as any);
    const store = createTabsStore(storage, "demo");
    const first = store.getState().openOrFocus("/demo/chat?session=a");
    const second = store.getState().openOrFocus("/demo/chat?session=b");
    expect(second).not.toBe(first);
    expect(store.getState().tabs.map((tab) => tab.url)).toEqual([
      "/demo/chat?session=a",
      "/demo/chat?session=b",
    ]);
    // Re-opening a session focuses its own tab instead of a second copy.
    expect(store.getState().openOrFocus("/demo/chat?session=a")).toBe(first);
    expect(store.getState().activeId).toBe(first);
  });

  it("creates separate tabs for two different issues", () => {
    const storage = createIslandStorage(memoryBacking() as any);
    const store = createTabsStore(storage, "demo");
    const first = store.getState().openTab("/demo/issues/MUL-1");
    const second = store.getState().openTab("/demo/issues/MUL-2");
    expect(second).not.toBe(first);
    expect(store.getState().tabs).toHaveLength(2);
  });

  it("reuses a pinned tab instead of opening a duplicate", () => {
    const storage = createIslandStorage(memoryBacking() as any);
    const store = createTabsStore(storage, "demo");
    const first = store.getState().openTab("/demo/issues", { pinned: true });
    const second = store.getState().openOrFocus("/demo/issues?x=1");
    expect(second).toBe(first);
    expect(store.getState().tabs[0]?.pinned).toBe(true);
  });

  it("closing the reused tab removes it", () => {
    const storage = createIslandStorage(memoryBacking() as any);
    const store = createTabsStore(storage, "demo");
    const first = store.getState().openTab("/demo/issues");
    store.getState().openOrFocus("/demo/issues?x=1");
    store.getState().closeTab(first);
    expect(store.getState().tabs.find((t) => t.id === first)).toBeUndefined();
  });
});
