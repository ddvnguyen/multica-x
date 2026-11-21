// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { ModelTier } from "@multica/core/types";
import { afterEach, describe, expect, it, vi } from "vitest";
import enIssues from "../../locales/en/issues.json";
import enCommon from "../../locales/en/common.json";
import { TierBadge } from "./tier-badge";

const TEST_RESOURCES = {
  en: { common: enCommon, issues: enIssues },
};

const TIERS: ModelTier[] = [
  {
    id: "tier-a", workspace_id: "ws", name: "fast", idx: 0,
    runtime_id: "rt-a", provider: "codebuddy", model: "model-a",
    provider_slots: 1, enabled: true, roles: [], manual_only: false, auto_pick: true, quota_kind: "none", quota_kinds: [], is_free: true, ctx_max_tokens: 280000, compact_at_pct: 75, created_at: "", updated_at: "",
  },
];

vi.mock("@multica/core/workspace/queries", () => ({
  modelTiersOptions: (wsId: string) => ({
    enabled: wsId !== "",
    queryKey: ["model-tiers", wsId],
    queryFn: async () => TIERS,
  }),
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws" }),
}));

function renderBadge(props: { tierId?: string; explicit?: boolean; parked?: boolean }) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <TierBadge {...props} />
      </QueryClientProvider>
    </I18nProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

// M-359: the run record shows which tier stamped a task and whether the
// sender pinned it (explicit) or the resolver picked it (auto), plus the
// parked state for a queued-behind-capacity row.
describe("TierBadge", () => {
  it("renders nothing without a tier id", () => {
    const { container } = renderBadge({});
    expect(container.textContent).toBe("");
  });

  it("shows the tier name for an auto pick", async () => {
    renderBadge({ tierId: "tier-a" });
    expect(await screen.findByText("fast", {}, { timeout: 2000 })).toBeTruthy();
  });

  it("marks an explicit pick", async () => {
    renderBadge({ tierId: "tier-a", explicit: true });
    expect(await screen.findByText("fast · explicit", {}, { timeout: 2000 })).toBeTruthy();
  });

  it("marks a parked row", async () => {
    renderBadge({ tierId: "tier-a", parked: true });
    expect(await screen.findByText("fast · waiting", {}, { timeout: 2000 })).toBeTruthy();
  });
});
