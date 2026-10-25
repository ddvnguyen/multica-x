// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import {
  BOARD_ROUTE,
  createIslandNavigation,
  HIDDEN_AREAS,
  islandPath,
  parseIslandRoute,
} from "../src/client/navigation";

describe("parseIslandRoute", () => {
  it("reads the board", () => {
    expect(parseIslandRoute("/demo/issues")).toEqual(BOARD_ROUTE);
    expect(parseIslandRoute("/demo/issues?view=board")).toEqual(BOARD_ROUTE);
  });

  it("reads one issue, including an identifier segment", () => {
    expect(parseIslandRoute("/demo/issues/MUL-12")).toEqual({
      view: "issue",
      issueId: "MUL-12",
    });
    expect(parseIslandRoute("/demo/issues/MUL-12#comment-abc")).toEqual({
      view: "issue",
      issueId: "MUL-12",
    });
    expect(parseIslandRoute("/demo/issues/MUL%2D12")).toEqual({
      view: "issue",
      issueId: "MUL-12",
    });
  });

  it("refuses every other destination", () => {
    expect(parseIslandRoute("/demo/billing")).toBeNull();
    expect(parseIslandRoute("/demo/issues/MUL-1/extra")).toBeNull();
  });
});

describe("islandPath", () => {
  it("round-trips through the parser", () => {
    for (const route of [BOARD_ROUTE, { view: "issue" as const, issueId: "MUL-9" }]) {
      expect(parseIslandRoute(islandPath("demo", route))).toEqual(route);
    }
  });
});

describe("createIslandNavigation", () => {
  it("swaps the rendered page on push instead of leaving the panel", () => {
    const onChange = vi.fn();
    const navigation = createIslandNavigation({
      slug: "demo",
      route: BOARD_ROUTE,
      onChange,
    });
    navigation.push("/demo/issues/MUL-3");
    expect(onChange).toHaveBeenCalledWith({ view: "issue", issueId: "MUL-3" });
  });

  it("refuses the surfaces this panel hides (Q8)", () => {
    const onChange = vi.fn();
    const onRefused = vi.fn();
    const navigation = createIslandNavigation({
      slug: "demo",
      route: BOARD_ROUTE,
      onChange,
      onRefused,
    });
    for (const area of HIDDEN_AREAS) {
      navigation.push(`/demo/${area}`);
    }
    expect(onChange).not.toHaveBeenCalled();
    expect(onRefused).toHaveBeenCalledTimes(HIDDEN_AREAS.length);
  });

  it("reports the board as the only place behind an issue", () => {
    const board = createIslandNavigation({
      slug: "demo",
      route: BOARD_ROUTE,
      onChange: vi.fn(),
    });
    expect(board.pathname).toBe("/demo/issues");
    expect(board.canGoBack?.()).toBe(false);

    const issue = createIslandNavigation({
      slug: "demo",
      route: { view: "issue", issueId: "MUL-1" },
      onChange: vi.fn(),
    });
    expect(issue.pathname).toBe("/demo/issues/MUL-1");
    expect(issue.canGoBack?.()).toBe(true);
    expect(issue.getShareableUrl("/demo/issues/MUL-1")).toBe("");
  });
});
