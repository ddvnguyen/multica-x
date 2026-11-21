// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";

const mocks = vi.hoisted(() => ({
  getWorkspace: vi.fn(),
  listAgentPresets: vi.fn(),
  listModelTiers: vi.fn(),
  listAgents: vi.fn(),
  createAgentPreset: vi.fn(),
  updateAgentPreset: vi.fn(),
  deleteAgentPreset: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({ api: mocks }));
vi.mock("@multica/core/workspace/queries", () => ({
  modelTiersOptions: (wsId: string) => ({
    queryKey: ["workspaces", wsId, "tiers"],
    queryFn: mocks.listModelTiers,
  }),
  agentListOptions: (wsId: string) => ({
    queryKey: ["workspaces", wsId, "agents"],
    queryFn: mocks.listAgents,
  }),
}));

import { PresetsSection } from "./presets-section";

const preset = {
  id: "preset-1",
  workspace_id: "ws-1",
  name: "Architect Sonnet",
  agent_id: "agent-1",
  agent_name: "Architect",
  tier_id: "tier-1",
  tier_name: "Sonnet High",
  tier_enabled: true,
};

function renderSection(canManage = true) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <PresetsSection wsId="ws-1" canManage={canManage} />
    </QueryClientProvider>,
  );
}

describe("PresetsSection", () => {
  beforeEach(() => {
    mocks.getWorkspace.mockReset().mockResolvedValue({
      settings: { agent_presets_enabled: true },
    });
    mocks.listAgentPresets.mockReset().mockResolvedValue([]);
    mocks.listModelTiers.mockReset().mockResolvedValue([
      { id: "tier-1", name: "Sonnet High", enabled: true },
      { id: "tier-2", name: "Fast", enabled: true },
      { id: "tier-off", name: "Disabled", enabled: false },
    ]);
    mocks.listAgents.mockReset().mockResolvedValue([
      { id: "agent-1", name: "Architect", archived_at: null },
      { id: "agent-2", name: "Engineer", archived_at: null },
    ]);
    mocks.createAgentPreset.mockReset().mockResolvedValue(preset);
    mocks.updateAgentPreset.mockReset().mockResolvedValue(preset);
    mocks.deleteAgentPreset.mockReset().mockResolvedValue(undefined);
  });

  afterEach(() => cleanup());

  it("creates a preset with a chosen agent and tier", async () => {
    const user = userEvent.setup();
    renderSection();

    await user.type(
      await screen.findByRole("textbox", { name: "Preset name" }),
      "Claude Architect",
    );
    await user.selectOptions(
      screen.getByRole("combobox", { name: "Agent" }),
      "agent-1",
    );
    await user.selectOptions(
      screen.getByRole("combobox", { name: "Tier" }),
      "tier-1",
    );
    await user.click(screen.getByRole("button", { name: "Add preset" }));

    expect(mocks.createAgentPreset).toHaveBeenCalledWith("ws-1", {
      name: "Claude Architect",
      agent_id: "agent-1",
      tier_id: "tier-1",
    });
  });

  it("lists every workspace preset and re-points one to another agent and tier", async () => {
    const user = userEvent.setup();
    mocks.listAgentPresets.mockResolvedValue([
      preset,
      { ...preset, id: "other-preset", name: "Engineer Fast", agent_id: "agent-2", agent_name: "Engineer" },
    ]);
    renderSection();

    expect(await screen.findByText("Architect Sonnet")).toBeInTheDocument();
    expect(screen.getByText("Engineer Fast")).toBeInTheDocument();
    await user.click(screen.getAllByRole("button", { name: "Edit" })[0]!);
    const nameInput = screen.getByRole("textbox", { name: "Preset name" });
    await user.clear(nameInput);
    await user.type(nameInput, "Architect Fast");
    await user.selectOptions(
      screen.getByRole("combobox", { name: "Agent" }),
      "agent-2",
    );
    await user.selectOptions(
      screen.getByRole("combobox", { name: "Tier" }),
      "tier-2",
    );
    await user.click(screen.getByRole("button", { name: "Save preset" }));

    expect(mocks.updateAgentPreset).toHaveBeenCalledWith("ws-1", "preset-1", {
      name: "Architect Fast",
      agent_id: "agent-2",
      tier_id: "tier-2",
    });
  });

  it("hides the editor for non-admin members", async () => {
    renderSection(false);

    expect(await screen.findByText("No presets yet.")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Add preset" }),
    ).toBeNull();
  });
});
