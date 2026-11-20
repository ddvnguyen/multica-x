import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { getCurrentWsId } from "@multica/core/platform";
import { buildTierPickItems, createTierPickSuggestion } from "./mention-suggestion";

vi.mock("@multica/core/platform", () => ({
  getCurrentWsId: vi.fn(() => "ws-1"),
}));

vi.mock("./suggestion-trigger-arming", () => ({
  isTriggerArmedAt: () => true,
}));

function fakeQc(tiers: unknown[]): QueryClient {
  return {
    getQueryData: (key: unknown) => {
      if (Array.isArray(key) && key[key.length - 1] === "tiers") return tiers;
      return undefined;
    },
    getQueriesData: () => [],
  } as unknown as QueryClient;
}

const TIERS = [
  { id: "tier-a", name: "fast", model: "model-a", enabled: true },
  { id: "tier-b", name: "slow", model: "model-b", enabled: false },
];

// M-359: after an `@agent` pill, typing `#` lists the workspace's tiers for
// the explicit pick. The items come from the already-cached tiers list (no
// server round-trip); disabled tiers are flagged, not dropped.
describe("buildTierPickItems", () => {
  it("returns all tiers on an empty query", () => {
    expect(buildTierPickItems(fakeQc(TIERS), "")).toHaveLength(2);
  });

  it("filters by name", () => {
    const items = buildTierPickItems(fakeQc(TIERS), "fas");
    expect(items.map((t) => t.name)).toEqual(["fast"]);
  });

  it("flags disabled tiers", () => {
    const items = buildTierPickItems(fakeQc(TIERS), "");
    expect(items.find((t) => t.name === "slow")?.disabled).toBe(true);
    expect(items.find((t) => t.name === "fast")?.disabled).toBe(false);
  });

  it("returns nothing without a workspace", () => {
    vi.mocked(getCurrentWsId).mockReturnValueOnce(null);
    expect(buildTierPickItems(fakeQc(TIERS), "")).toEqual([]);
  });
});

describe("createTierPickSuggestion command", () => {
  it("rewrites the preceding agent mention label to name#tier", () => {
    const config = createTierPickSuggestion(fakeQc(TIERS));
    const mentionNode = {
      type: { name: "mention" },
      attrs: { type: "agent", label: "Helper", id: "agent-1" },
    };
    const dispatched: unknown[] = [];
    const editor = {
      state: {
        doc: {
          nodesBetween: (_from: number, _to: number, cb: (node: unknown, pos: number) => void) => {
            cb(mentionNode, 10);
          },
        },
        tr: {
          setNodeMarkup: vi.fn().mockReturnThis(),
          delete: vi.fn().mockReturnThis(),
        },
      },
      view: { dispatch: (tr: unknown) => dispatched.push(tr) },
      chain: () => ({ focus: () => ({ run: () => {} }) }),
    };
    // shouldShow passes only directly after an agent mention.
    expect(
      config.shouldShow?.({ editor: editor as never, range: { from: 12, to: 12 } } as never),
    ).toBe(true);
    config.command?.({
      editor: editor as never,
      range: { from: 11, to: 16 },
      props: { id: "tier-a", name: "fast", disabled: false },
    } as never);
    expect(editor.state.tr.setNodeMarkup).toHaveBeenCalledWith(10, undefined, {
      type: "agent",
      label: "Helper#fast",
      id: "agent-1",
    });
    expect(dispatched).toHaveLength(1);
  });

  it("replaces an existing tier suffix instead of stacking", () => {
    const config = createTierPickSuggestion(fakeQc(TIERS));
    const mentionNode = {
      type: { name: "mention" },
      attrs: { type: "agent", label: "Helper#slow", id: "agent-1" },
    };
    const editor = {
      state: {
        doc: {
          nodesBetween: (_from: number, _to: number, cb: (node: unknown, pos: number) => void) => {
            cb(mentionNode, 10);
          },
        },
        tr: {
          setNodeMarkup: vi.fn().mockReturnThis(),
          delete: vi.fn().mockReturnThis(),
        },
      },
      view: { dispatch: () => {} },
      chain: () => ({ focus: () => ({ run: () => {} }) }),
    };
    config.command?.({
      editor: editor as never,
      range: { from: 11, to: 16 },
      props: { id: "tier-a", name: "fast", disabled: false },
    } as never);
    expect(editor.state.tr.setNodeMarkup).toHaveBeenCalledWith(10, undefined, {
      type: "agent",
      label: "Helper#fast",
      id: "agent-1",
    });
  });
});
