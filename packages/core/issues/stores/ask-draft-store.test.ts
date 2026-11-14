// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useAskDraftStore, ASK_DRAFT_STORAGE_KEY } from "./ask-draft-store";

describe("ask draft store", () => {
  beforeEach(() => {
    useAskDraftStore.setState({ drafts: {} });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("saves and restores picks + text per card, isolated by key", () => {
    const { setAnswers, getAnswers } = useAskDraftStore.getState();
    setAnswers("ask:card-1", "issue-a", [{ selected: [0], text: "line one\nline two" }]);
    setAnswers("ask:card-2", "issue-a", [{ selected: [], text: "" }]);

    expect(getAnswers("ask:card-1")).toEqual([
      { selected: [0], text: "line one\nline two" },
    ]);
    // An emptied draft carries nothing: the store drops it instead of
    // persisting a blank record.
    expect(getAnswers("ask:card-2")).toBeUndefined();
  });

  it("clears the draft when the card is answered", () => {
    const store = useAskDraftStore.getState();
    store.setAnswers("ask:card-1", "issue-a", [{ selected: [1], text: "" }]);
    expect(store.getDraft("ask:card-1")).toBeDefined();

    store.clearDraft("ask:card-1");
    expect(useAskDraftStore.getState().getDraft("ask:card-1")).toBeUndefined();
  });

  it("drops drafts for cards that are no longer open", () => {
    const store = useAskDraftStore.getState();
    store.setAnswers("ask:open-1", "issue-a", [{ selected: [], text: "keep me" }]);
    store.setAnswers("ask:gone-9", "issue-a", [{ selected: [0], text: "" }]);

    store.pruneToOpen("issue-a", new Set(["open-1"]));

    const { drafts } = useAskDraftStore.getState();
    expect(Object.keys(drafts)).toEqual(["ask:open-1"]);
  });

  it("keeps another issue's drafts when this issue's panel prunes", () => {
    const store = useAskDraftStore.getState();
    store.setAnswers("ask:card-a", "issue-a", [{ selected: [0], text: "A's draft" }]);
    store.setPlanComments("plan:plan-a", "issue-a", ["A's comment"]);

    // Issue B's panel loads with only B's cards open: A's drafts survive.
    store.pruneToOpen("issue-b", new Set(["card-b"]));

    const { drafts } = useAskDraftStore.getState();
    expect(Object.keys(drafts).sort()).toEqual(["ask:card-a", "plan:plan-a"]);
    expect(drafts["ask:card-a"]?.issueId).toBe("issue-a");
  });

  it("never prunes transcript keys (`ask:<task>:<seq>`)", () => {
    const store = useAskDraftStore.getState();
    store.setAnswers("ask:task-7:3", "issue-a", [{ selected: [], text: "on the run" }]);

    // Any issue's ask-plan read leaves the transcript draft alone; the
    // transcript mount clears it once the ask leaves pending.
    store.pruneToOpen("issue-a", new Set());

    expect(useAskDraftStore.getState().getAnswers("ask:task-7:3")).toEqual([
      { selected: [], text: "on the run" },
    ]);
  });

  it("keeps plan comments + note under the plan key, cleared on verdict", () => {
    const store = useAskDraftStore.getState();
    store.setPlanComments("plan:plan-1", "issue-a", ["Ship z first."]);
    store.setPlanNote("plan:plan-1", "issue-a", "also rename s2");

    expect(useAskDraftStore.getState().getDraft("plan:plan-1")).toMatchObject({
      planComments: ["Ship z first."],
      planNote: "also rename s2",
    });

    store.clearDraft("plan:plan-1");
    expect(useAskDraftStore.getState().getDraft("plan:plan-1")).toBeUndefined();
  });

  it("registers under the ask-drafts persist key", () => {
    expect(ASK_DRAFT_STORAGE_KEY).toBe("multica_ask_drafts");
  });
});
