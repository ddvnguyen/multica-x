// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import type { ModelTier, ModelTierMember } from "@multica/core/types";
import enIssues from "../../../locales/en/issues.json";
import { TierPickFlow } from "./tier-pick-flow";

const TIER: ModelTier = {
  id: "tier-a",
  workspace_id: "ws",
  name: "deepseek-flash",
  idx: 0,
  provider: "freebuff",
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
};

const MEMBERS: ModelTierMember[] = [
  {
    id: "m-freebuff",
    tier_id: "tier-a",
    position: 0,
    provider: "freebuff",
    model: "deepseek-v4.1-flash",
    runtime_id: "rt-freebuff",
    provider_slots: 1,
    enabled: true,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
  {
    id: "m-go",
    tier_id: "tier-a",
    position: 1,
    provider: "opencode-go",
    model: "deepseek-v4.1-flash",
    runtime_id: "rt-go",
    provider_slots: 1,
    enabled: true,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
];

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws" }));
vi.mock("@tanstack/react-query", () => ({
  useQueries: ({ queries }: { queries: unknown[] }) => queries.map(() => ({ data: MEMBERS })),
  useQuery: () => ({
    data: [
      { id: "rt-freebuff", name: "Freebuff Desktop", provider: "freebuff" },
      { id: "rt-go", name: "OpenCode Go", provider: "opencode-go" },
    ],
  }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  modelTierMembersOptions: (wsId: string, tierId: string) => ({
    queryKey: ["workspaces", wsId, "tiers", tierId, "members"],
  }),
}));
vi.mock("@multica/core/runtimes/queries", () => ({
  runtimeListOptions: (wsId: string) => ({ queryKey: ["runtimes", wsId] }),
}));

function renderFlow(onPick: (tierId: string) => void, onAuto?: () => void) {
  return render(
    <I18nProvider locale="en" resources={{ en: { issues: enIssues } }}>
      <TierPickFlow
        tiers={[TIER]}
        membersByTier={new Map([[TIER.id, MEMBERS]])}
        onPick={onPick}
        {...(onAuto ? { onAuto } : {})}
      />
    </I18nProvider>,
  );
}

describe("TierPickFlow", () => {
  it("walks model → harness → runtime and picks the tier config", async () => {
    const user = userEvent.setup();
    const onPick = vi.fn();
    renderFlow(onPick);

    await user.click(await screen.findByText("deepseek-v4.1-flash"));
    expect(await screen.findByText("Harness")).toBeTruthy();
    await user.click(await screen.findByText("Freebuff"));
    expect(await screen.findByText("Runtime")).toBeTruthy();
    // The leaf shows the runtime name AND the tier config it belongs to.
    expect(await screen.findByText("Freebuff Desktop")).toBeTruthy();
    expect(await screen.findByText(/#deepseek-flash · freebuff\/deepseek-v4.1-flash/i)).toBeTruthy();
    await user.click(await screen.findByText("Freebuff Desktop"));

    expect(onPick).toHaveBeenCalledWith("tier-a");
  });

  it("offers the same model under a second harness", async () => {
    const user = userEvent.setup();
    renderFlow(vi.fn());
    await user.click(await screen.findByText("deepseek-v4.1-flash"));

    expect(await screen.findByText("Freebuff")).toBeTruthy();
    expect(await screen.findByText("Opencode-go")).toBeTruthy();
  });

  it("renders the Auto row when the caller allows it", async () => {
    const user = userEvent.setup();
    const onAuto = vi.fn();
    renderFlow(vi.fn(), onAuto);

    await user.click(await screen.findByText("Auto (resolver picks)"));
    expect(onAuto).toHaveBeenCalled();
  });
});
