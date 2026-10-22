// @vitest-environment node
import { describe, expect, it, beforeEach } from "vitest";
import { createStore, type StoreApi } from "zustand/vanilla";
import {
  mergeViewStatePersisted,
  viewStoreSlice,
  type IssueViewState,
} from "./view-store";

/**
 * A collapsed board column is a display preference, not a filter: the column
 * keeps its place and its count and shows no cards. It is persisted per
 * workspace, like every other view preference.
 */
describe("collapsed board columns", () => {
  let store: StoreApi<IssueViewState>;
  beforeEach(() => {
    store = createStore<IssueViewState>()((set) => viewStoreSlice(set));
  });

  it("collapses and expands a column by its column id", () => {
    store.getState().toggleBoardColumnCollapsed("status:todo");

    expect(store.getState().boardCollapsedColumns).toEqual(["status:todo"]);

    store.getState().toggleBoardColumnCollapsed("status:todo");

    expect(store.getState().boardCollapsedColumns).toEqual([]);
  });

  it("keeps other columns' collapse state when one is toggled", () => {
    store.getState().toggleBoardColumnCollapsed("status:todo");
    store.getState().toggleBoardColumnCollapsed("project:abc");

    expect(store.getState().boardCollapsedColumns).toEqual([
      "status:todo",
      "project:abc",
    ]);

    store.getState().toggleBoardColumnCollapsed("status:todo");

    expect(store.getState().boardCollapsedColumns).toEqual(["project:abc"]);
  });

  it("restores collapsed columns from a persisted snapshot", () => {
    const merged = mergeViewStatePersisted(
      { boardCollapsedColumns: ["status:in_progress"] },
      store.getState(),
    );

    expect(merged.boardCollapsedColumns).toEqual(["status:in_progress"]);
  });

  it("drops a persisted snapshot that is not a list of column ids", () => {
    const merged = mergeViewStatePersisted(
      { boardCollapsedColumns: "status:todo" },
      store.getState(),
    );

    expect(merged.boardCollapsedColumns).toEqual([]);
  });

  it("ignores non-string ids in a persisted snapshot", () => {
    const merged = mergeViewStatePersisted(
      { boardCollapsedColumns: ["status:todo", 7, null] },
      store.getState(),
    );

    expect(merged.boardCollapsedColumns).toEqual(["status:todo"]);
  });
});
