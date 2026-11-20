// @vitest-environment node
import { beforeEach, describe, expect, it } from "vitest";
import { RAIL_SPACES } from "../paths/spaces";
import { resolveRailOrder, useRailOrderStore } from "./rail-order-store";

describe("resolveRailOrder", () => {
  it("uses the default order until the user reorders", () => {
    expect(resolveRailOrder(null)).toEqual(RAIL_SPACES);
  });

  // NB: `decisions` is the fork's own rail space (M-253) and `docs` arrived
  // upstream; every saved order below carries both. Keep these lists in
  // step with RAIL_SPACES.
  it("inserts Docs and Decisions into a saved layout from before they existed", () => {
    expect(
      resolveRailOrder(["chat", "inbox", "work", "team", "autopilots"]),
    ).toEqual(["chat", "decisions", "work", "docs", "team", "autopilots"]);
  });

  it("keeps a saved order", () => {
    const saved = ["chat", "decisions", "work", "docs", "team", "autopilots"];
    expect(resolveRailOrder(saved)).toEqual(saved);
  });

  it("drops unknown and repeated ids", () => {
    expect(
      resolveRailOrder(["chat", "settings", 3, "chat", "work", "docs", "team", "autopilots"]),
    ).toEqual(["chat", "decisions", "work", "docs", "team", "autopilots"]);
  });

  it("drops the retired Inbox space from an order saved before it moved into Work", () => {
    expect(
      resolveRailOrder(["chat", "inbox", "work", "docs", "team", "autopilots"]),
    ).toEqual(["chat", "decisions", "work", "docs", "team", "autopilots"]);
  });

  it("puts a space missing from the saved order after its default neighbour", () => {
    expect(resolveRailOrder(["chat", "autopilots", "work"])).toEqual([
      "chat",
      "decisions",
      "autopilots",
      "work",
      "docs",
      "team",
    ]);
    expect(resolveRailOrder(["chat", "team", "autopilots"])).toEqual([
      "work",
      "docs",
      "chat",
      "decisions",
      "team",
      "autopilots",
    ]);
  });
});

describe("useRailOrderStore", () => {
  beforeEach(() => {
    useRailOrderStore.setState({ order: null });
  });

  it("saves a custom order", () => {
    const order = ["chat", "decisions", "work", "docs", "team", "autopilots"] as const;
    useRailOrderStore.getState().setOrder(order);
    expect(useRailOrderStore.getState().order).toEqual(order);
  });

  it("forgets the order once it is back to the default", () => {
    useRailOrderStore
      .getState()
      .setOrder(["team", "work", "docs", "chat", "autopilots"]);
    useRailOrderStore.getState().setOrder(RAIL_SPACES);
    expect(useRailOrderStore.getState().order).toBeNull();
  });
});
