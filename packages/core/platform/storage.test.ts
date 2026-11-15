// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  defaultStorage,
  onDefaultStorageAdapterChanged,
  setDefaultStorage,
} from "./storage";
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

// M-336: a store whose module hydrated before the host adapter existed needs
// to know when the adapter lands, so it can re-read its persisted state.
describe("onDefaultStorageAdapterChanged", () => {
  /** Microtasks carry the deferred notification (the render-safety deferral). */
  async function flushNotify(): Promise<void> {
    await Promise.resolve();
    await Promise.resolve();
  }

  it("fires on install and on reset, deferred to a microtask", async () => {
    const listener = vi.fn();
    const stop = onDefaultStorageAdapterChanged(listener);
    const backing = fakeBacking();
    setDefaultStorage(backing);
    // Deferred, not synchronous: the swap runs inside a render.
    expect(listener).not.toHaveBeenCalled();
    await flushNotify();
    expect(listener).toHaveBeenCalledTimes(1);

    setDefaultStorage(defaultStorage);
    await flushNotify();
    expect(listener).toHaveBeenCalledTimes(2);
    stop();
  });

  it("does not fire when the effective backing is unchanged", async () => {
    const listener = vi.fn();
    const stop = onDefaultStorageAdapterChanged(listener);
    setDefaultStorage(defaultStorage);
    await flushNotify();
    expect(listener).not.toHaveBeenCalled();
    stop();
  });

  it("stops firing after unsubscribe", async () => {
    const listener = vi.fn();
    const stop = onDefaultStorageAdapterChanged(listener);
    stop();
    setDefaultStorage(fakeBacking());
    await flushNotify();
    expect(listener).not.toHaveBeenCalled();
    setDefaultStorage(defaultStorage);
  });
});
