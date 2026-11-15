// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { defaultStorage, setDefaultStorage } from "./storage";
import type { StorageAdapter } from "../types/storage";

/** An isolated adapter, so the tests never read the suite's localStorage. */
function fakeBacking(): StorageAdapter {
  const store = new Map<string, string>();
  return {
    getItem: (k) => store.get(k) ?? null,
    setItem: (k, v) => {
      store.set(k, v);
    },
    removeItem: (k) => {
      store.delete(k);
    },
    keys: () => [...store.keys()],
  };
}

afterEach(() => {
  // restore the browser backing for every other suite
  setDefaultStorage(defaultStorage);
  localStorage.clear();
});

describe("defaultStorage", () => {
  it("reads and writes the browser store when no host supplied one", () => {
    localStorage.setItem("test:bare", "1");
    expect(defaultStorage.getItem("test:bare")).toBe("1");
    defaultStorage.setItem("test:bare", "2");
    expect(localStorage.getItem("test:bare")).toBe("2");
  });

  // M-360: a host on a shared origin points the default adapter at its own
  // namespaced store once at boot, so every store built on `defaultStorage`
  // keeps its widths and preferences out of the bare keys.
  it("routes through a host-supplied adapter until it is reset", () => {
    const backing = fakeBacking();
    setDefaultStorage(backing);
    defaultStorage.setItem("test:key", "hosted");
    expect(backing.getItem("test:key")).toBe("hosted");
    expect(localStorage.getItem("test:key")).toBeNull();
    expect(defaultStorage.keys?.()).toEqual(["test:key"]);

    setDefaultStorage(defaultStorage);
    expect(defaultStorage.getItem("test:key")).toBeNull();
  });

  it("treats a host passing the default adapter itself as the browser store", () => {
    localStorage.setItem("test:self", "browser");
    const backing = fakeBacking();
    setDefaultStorage(backing);
    setDefaultStorage(defaultStorage);
    expect(defaultStorage.getItem("test:self")).toBe("browser");
  });
});
