import { describe, expect, it } from "vitest";
import type { ModelTier, ModelTierMember } from "@multica/core/types";
import { buildTierPickTree, tierPickModelCount } from "./tier-pick-tree";

function tier(overrides: Partial<ModelTier> = {}): ModelTier {
  return {
    id: "tier-a",
    workspace_id: "ws",
    name: "fast",
    idx: 0,
    provider: "opencode-go",
    model: "deepseek-v4.1-flash",
    provider_slots: 1,
    enabled: true,
    roles: [],
    manual_only: false,
    auto_pick: true,
    quota_kind: "none",
    quota_kinds: [],
    is_free: true,
    ctx_max_tokens: 280000,
    compact_at_pct: 75,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

function member(overrides: Partial<ModelTierMember> = {}): ModelTierMember {
  return {
    id: "member-a",
    tier_id: "tier-a",
    position: 0,
    provider: "opencode-go",
    model: "deepseek-v4.1-flash",
    provider_slots: 1,
    enabled: true,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("buildTierPickTree", () => {
  it("groups one model under several harnesses and their runtimes", () => {
    const tree = buildTierPickTree(
      [tier()],
      new Map([
        [
          "tier-a",
          [
            member({
              id: "m1",
              position: 0,
              provider: "freebuff",
              runtime_id: "rt-freebuff",
            }),
            member({
              id: "m2",
              position: 1,
              provider: "opencode-go",
              runtime_id: "rt-go",
            }),
            member({
              id: "m3",
              position: 2,
              provider: "codebuddy",
            }),
          ],
        ],
      ]),
      { runtimeLabel: (id) => (id === "rt-freebuff" ? "Freebuff Desktop" : undefined) },
    );

    expect(tree).toHaveLength(1);
    const model = tree[0]!;
    expect(model.label).toBe("deepseek-v4.1-flash");
    expect(model.harnesses.map((harness) => harness.key)).toEqual([
      "freebuff",
      "opencode-go",
      "codebuddy",
    ]);
    expect(model.harnesses[0]!.entries[0]!.runtimeLabel).toBe("Freebuff Desktop");
    expect(model.harnesses[1]!.entries[0]!.runtimeLabel).toBe("rt-go");
    expect(model.harnesses[2]!.entries[0]!.runtimeLabel).toBe("Codebuddy");
    expect(model.harnesses[0]!.entries[0]!.tierLabel).toBe("#fast");
    expect(tierPickModelCount(model)).toBe(3);
  });

  it("falls back to the legacy tier columns when a tier has no members", () => {
    const [model] = buildTierPickTree([tier({ provider: "codebuddy", runtime_id: "rt-cb" })]);

    expect(model!.harnesses).toHaveLength(1);
    expect(model!.harnesses[0]!.key).toBe("codebuddy");
    const entry = model!.harnesses[0]!.entries[0]!;
    expect(entry.member).toBeUndefined();
    expect(entry.runtimeId).toBe("rt-cb");
    expect(entry.runtimeLabel).toBe("rt-cb");
  });

  it("hides disabled tiers and members unless asked to include them", () => {
    const tiers = [
      tier({ id: "on", name: "fast", idx: 0 }),
      tier({ id: "off", name: "slow", idx: 1, enabled: false }),
    ];
    const members = new Map<string, ModelTierMember[]>([
      [
        "on",
        [
          member({ id: "on-m", position: 0, enabled: true }),
          member({ id: "off-m", position: 1, enabled: false }),
        ],
      ],
    ]);

    const pickers = buildTierPickTree(tiers, members);
    expect(pickers).toHaveLength(1);
    expect(pickers[0]!.harnesses[0]!.entries.map((entry) => entry.key)).toEqual(["on:on-m"]);

    const admin = buildTierPickTree(tiers, members, { includeDisabled: true });
    expect(admin[0]!.harnesses[0]!.entries.map((entry) => entry.key)).toEqual([
      "on:on-m",
      "on:off-m",
      "off:legacy",
    ]);
  });

  it("orders models, harnesses and members by tier idx then member position", () => {
    const tree = buildTierPickTree(
      [tier({ id: "late", idx: 5, model: "sonnet-5-5" }), tier({ id: "early", idx: 0, model: "deepseek" })],
      new Map([
        ["late", [member({ id: "l1", tier_id: "late", model: "sonnet-5-5", position: 0 })]],
        [
          "early",
          [
            member({ id: "e2", tier_id: "early", model: "deepseek", position: 2, provider: "codebuddy" }),
            member({ id: "e1", tier_id: "early", model: "deepseek", position: 1, provider: "freebuff" }),
          ],
        ],
      ]),
    );

    expect(tree.map((model) => model.label)).toEqual(["deepseek", "sonnet-5-5"]);
    expect(tree[0]!.harnesses.map((harness) => harness.key)).toEqual(["freebuff", "codebuddy"]);
  });
});
