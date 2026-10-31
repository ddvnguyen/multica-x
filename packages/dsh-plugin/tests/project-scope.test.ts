import { describe, expect, it } from "vitest";
import {
  ALL_PROJECTS,
  PROJECT_SCOPE_KEY,
  readProjectScope,
  writeProjectScope,
} from "../src/client/project-scope";
import { createIslandStorage, STORAGE_PREFIX } from "../src/client/storage";

/** A `localStorage`-shaped Map the adapter can back onto. */
function backing() {
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

const PROJECTS = [{ id: "p1" }, { id: "p2" }];

describe("island project scope (M-173)", () => {
  it("defaults to All projects when nothing is stored", () => {
    const storage = createIslandStorage(backing());
    expect(readProjectScope(storage, PROJECTS)).toBe(ALL_PROJECTS);
  });

  it("round-trips a selection under the namespaced key", () => {
    const store = backing();
    const storage = createIslandStorage(store);

    writeProjectScope(storage, "p2");

    expect(store.values.get(STORAGE_PREFIX + PROJECT_SCOPE_KEY)).toBe("p2");
    expect(readProjectScope(storage, PROJECTS)).toBe("p2");
  });

  it("stores the id only, so a renamed project keeps its selection", () => {
    const store = backing();
    const storage = createIslandStorage(store);

    writeProjectScope(storage, "p1");

    expect(store.values.get(STORAGE_PREFIX + PROJECT_SCOPE_KEY)).toBe("p1");
    // The label lives in the list, not in storage.
    expect(JSON.parse(JSON.stringify([...store.values.values()]))).toEqual(["p1"]);
  });

  it("resets to All projects when the selected project is gone", () => {
    const storage = createIslandStorage(backing());
    writeProjectScope(storage, "deleted");

    expect(readProjectScope(storage, PROJECTS)).toBe(ALL_PROJECTS);
  });

  it("clears the key when All projects is chosen", () => {
    const store = backing();
    const storage = createIslandStorage(store);

    writeProjectScope(storage, "p1");
    writeProjectScope(storage, ALL_PROJECTS);

    expect(store.values.has(STORAGE_PREFIX + PROJECT_SCOPE_KEY)).toBe(false);
    expect(readProjectScope(storage, PROJECTS)).toBe(ALL_PROJECTS);
  });

  it("keeps the stored id while the list is still loading", () => {
    const storage = createIslandStorage(backing());
    writeProjectScope(storage, "p1");

    // No list yet: do not flash back to All projects on every mount.
    expect(readProjectScope(storage, undefined)).toBe("p1");
  });
});
