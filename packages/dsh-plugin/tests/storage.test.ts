// @vitest-environment node
import { describe, expect, it } from "vitest";
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

describe("createIslandStorage", () => {
  it("namespaces every key so dsh's origin is not a shared bucket", () => {
    const backing = memoryBacking();
    const storage = createIslandStorage(backing);
    storage.setItem("issues-view", "{}");
    expect([...backing.values.keys()]).toEqual([`${STORAGE_PREFIX}issues-view`]);
    expect(storage.getItem("issues-view")).toBe("{}");
    expect(backing.getItem("issues-view")).toBeNull();
  });

  it("lists only its own keys and removes them", () => {
    const backing = memoryBacking();
    backing.setItem("dsh:theme", "dark");
    const storage = createIslandStorage(backing);
    storage.setItem("a", "1");
    storage.setItem("b", "2");
    expect(storage.keys?.()).toEqual(["a", "b"]);
    storage.removeItem("a");
    expect(storage.keys?.()).toEqual(["b"]);
  });

  it("survives a storage that throws or is absent", () => {
    const throwing = createIslandStorage({
      getItem: () => {
        throw new Error("blocked");
      },
      setItem: () => {
        throw new Error("blocked");
      },
      removeItem: () => {
        throw new Error("blocked");
      },
      key: () => null,
      length: 0,
    });
    expect(() => throwing.setItem("x", "y")).not.toThrow();
    expect(throwing.getItem("x")).toBeNull();
    expect(createIslandStorage(null).getItem("x")).toBeNull();
  });
});
