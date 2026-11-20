// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { ModelTier } from "@multica/core/types";
import { afterEach, describe, expect, it, vi } from "vitest";
import enAgents from "../../../locales/en/agents.json";
import enCommon from "../../../locales/en/common.json";
import enIssues from "../../../locales/en/issues.json";
import { TierPicker } from "./tier-picker";

const TEST_RESOURCES = {
  en: { common: enCommon, agents: enAgents, issues: enIssues },
};

// M-351: the TierPicker is the use_tiers counterpart of the model picker —
// it renders the workspace tiers, toggles selection by id, and marks
// deleted/disabled tiers as unavailable instead of dropping them.
const TIERS: ModelTier[] = [
  {
    id: "tier-a", workspace_id: "ws", name: "fast", idx: 0,
    runtime_id: "rt-a", provider: "codebuddy", model: "model-a",
    provider_slots: 1, enabled: true, roles: [], manual_only: false, quota_kind: "none", is_free: true, created_at: "", updated_at: "",
  },
  {
    id: "tier-b", workspace_id: "ws", name: "slow", idx: 1,
    runtime_id: "rt-b", provider: "codebuddy", model: "model-b",
    provider_slots: 1, enabled: false, roles: [], manual_only: false, quota_kind: "none", is_free: true, created_at: "", updated_at: "",
  },
];

let tiersResult: () => Promise<ModelTier[]> = async () => TIERS;

vi.mock("@multica/core/workspace/queries", () => ({
  modelTiersOptions: (wsId: string) => ({
    enabled: wsId !== "",
    queryKey: ["model-tiers", wsId],
    queryFn: () => tiersResult(),
  }),
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws" }),
}));

function renderPicker(value: string[] = []) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const onChange = vi.fn(async () => {});
  const view = render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={queryClient}>
        <TierPicker value={value} onChange={onChange} />
      </QueryClientProvider>
    </I18nProvider>,
  );
  return { ...view, onChange };
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  tiersResult = async () => TIERS;
});

describe("TierPicker", () => {
  it("labels the selection by tier name", async () => {
    renderPicker(["tier-a"]);
    expect(await screen.findByText("fast", {}, { timeout: 2000 })).toBeTruthy();
  });

  it("renders an empty state with no selection", async () => {
    renderPicker([]);
    expect(
      await screen.findByText("No tiers selected", {}, { timeout: 2000 }),
    ).toBeTruthy();
  });

  it("marks a disabled tier unavailable and still lists it", async () => {
    const { onChange } = renderPicker(["tier-a", "tier-b"]);
    fireEvent.click(await screen.findByText("fast, slow", {}, { timeout: 2000 }));
    expect(await screen.findByText("slow")).toBeTruthy();
    expect(
      await screen.findByText(/unavailable/i, {}, { timeout: 2000 }),
    ).toBeTruthy();
    expect(onChange).not.toHaveBeenCalled();
  });

  it("renders a deleted tier id as a short id, not dropped", async () => {
    renderPicker(["gone-tier-id-1234567890"]);
    expect(
      await screen.findByText("gone-tie", {}, { timeout: 2000 }),
    ).toBeTruthy();
  });

  it("shows the empty-workspace hint when no tiers exist", async () => {
    tiersResult = async () => [];
    renderPicker([]);
    // The trigger shows the empty label; the hint lives inside the popover.
    expect(
      await screen.findByText("No tiers selected", {}, { timeout: 2000 }),
    ).toBeTruthy();
    fireEvent.click(await screen.findByText("No tiers selected", {}, { timeout: 2000 }));
    expect(
      await screen.findByText(/No tiers in this workspace/i, {}, { timeout: 2000 }),
    ).toBeTruthy();
  });
});
