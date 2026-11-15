// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from "vitest";

const storage = vi.hoisted(() => new Map<string, string>());
vi.mock("../../platform/storage", () => {
  // Stand-in for the module's own listener registry, so the tests can fire the
  // adapter-landed notification the real `setDefaultStorage` fires.
  const listeners: Array<() => void> = [];
  return {
    defaultStorage: {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => storage.set(key, value),
      removeItem: (key: string) => storage.delete(key),
    },
    onDefaultStorageAdapterChanged: (listener: () => void) => {
      listeners.push(listener);
      return () => {
        const at = listeners.indexOf(listener);
        if (at >= 0) listeners.splice(at, 1);
      };
    },
    __adapterLanded: () => {
      for (const listener of listeners) listener();
    },
  };
});

import * as platformStorage from "../../platform/storage";
import { useIssueOpeningStore } from "./issue-opening-store";

/** The test double of `setDefaultStorage` firing its notification. */
function adapterLanded(): void {
  (platformStorage as unknown as { __adapterLanded: () => void }).__adapterLanded();
}

beforeEach(() => {
  storage.clear();
  useIssueOpeningStore.setState({ openMode: "page", hasUserChoice: false });
});

describe("issue opening preference", () => {
  it("restores the saved choice on rehydration", async () => {
    useIssueOpeningStore.getState().setOpenMode("peek");
    const saved = storage.get("multica_issue_opening")!;
    expect(JSON.parse(saved).state).toEqual({ openMode: "peek" });
    useIssueOpeningStore.getState().setOpenMode("page");
    storage.set("multica_issue_opening", saved);
    await useIssueOpeningStore.persist.rehydrate();
    expect(useIssueOpeningStore.getState().openMode).toBe("peek");
  });

  it("restores the new-tab preference", async () => {
    useIssueOpeningStore.getState().setOpenMode("tab");
    const saved = storage.get("multica_issue_opening")!;
    useIssueOpeningStore.getState().setOpenMode("page");
    storage.set("multica_issue_opening", saved);
    await useIssueOpeningStore.persist.rehydrate();
    expect(useIssueOpeningStore.getState()).toMatchObject({ openMode: "tab", hasUserChoice: true });
  });

  it.each([null, {}, { openMode: "unknown" }])("defaults to full pages for missing or invalid preferences: %j", async (state) => {
    useIssueOpeningStore.getState().setOpenMode("peek");
    storage.set("multica_issue_opening", JSON.stringify({ state, version: 0 }));
    await useIssueOpeningStore.persist.rehydrate();
    expect(useIssueOpeningStore.getState().openMode).toBe("page");
    expect(useIssueOpeningStore.getState().hasUserChoice).toBe(false);
  });

  // M-336: the dsh island hydrates this store before CoreProvider installs the
  // `multica.dsh.*` adapter, so the first read misses and the choice would be
  // clobbered by the capability's migration effect on every reload.
  describe("late host adapter (the island's boot order)", () => {
    it("re-reads the persisted choice when the adapter lands", async () => {
      storage.set(
        "multica_issue_opening",
        JSON.stringify({ state: { openMode: "peek" }, version: 0 }),
      );
      expect(useIssueOpeningStore.getState()).toMatchObject({
        openMode: "page",
        hasUserChoice: false,
      });
      adapterLanded();
      await Promise.resolve();
      expect(useIssueOpeningStore.getState()).toMatchObject({
        openMode: "peek",
        hasUserChoice: true,
      });
    });

    it("stays on the defaults when the adapter holds nothing valid", async () => {
      storage.set(
        "multica_issue_opening",
        JSON.stringify({ state: { openMode: "banana" }, version: 0 }),
      );
      adapterLanded();
      await Promise.resolve();
      expect(useIssueOpeningStore.getState()).toMatchObject({
        openMode: "page",
        hasUserChoice: false,
      });
    });
  });
});
