// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../../test/i18n";

const mocks = vi.hoisted(() => ({
  getWorkspace: vi.fn(),
  listAgentPresets: vi.fn(),
  listModelTiers: vi.fn(),
  createAgentPreset: vi.fn(),
  updateAgentPreset: vi.fn(),
  deleteAgentPreset: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({ api: mocks }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));

import { AgentPresetEditor } from "./agent-preset-editor";

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

function renderEditor() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <AgentPresetEditor agentId="agent-1" canEdit />
    </QueryClientProvider>,
  );
}

describe("AgentPresetEditor", () => {
  beforeEach(() => {
    mocks.getWorkspace.mockReset().mockResolvedValue({ settings: { agent_presets_enabled: true } });
    mocks.listAgentPresets.mockReset().mockResolvedValue([]);
    mocks.listModelTiers.mockReset().mockResolvedValue([
      { id: "tier-1", name: "Sonnet High", enabled: true },
      { id: "tier-2", name: "Fast", enabled: true },
      { id: "tier-off", name: "Disabled", enabled: false },
    ]);
    mocks.createAgentPreset.mockReset().mockResolvedValue(preset);
    mocks.updateAgentPreset.mockReset().mockResolvedValue(preset);
    mocks.deleteAgentPreset.mockReset().mockResolvedValue(undefined);
  });

  afterEach(() => cleanup());

  it("creates a named agent and tier preset", async () => {
    const user = userEvent.setup();
    renderEditor();

    await user.type(await screen.findByRole("textbox", { name: "Preset name" }), "Claude Architect");
    await user.click(screen.getByRole("button", { name: "Tier" }));
    await user.click(await screen.findByRole("button", { name: "Sonnet High" }));
    await user.click(screen.getByRole("button", { name: "Add preset" }));

    expect(mocks.createAgentPreset).toHaveBeenCalledWith("ws-1", {
      name: "Claude Architect",
      agent_id: "agent-1",
      tier_id: "tier-1",
    });
  });

  it("edits an existing preset and filters out other agents", async () => {
    const user = userEvent.setup();
    mocks.listAgentPresets.mockResolvedValue([
      preset,
      { ...preset, id: "other-preset", agent_id: "agent-2", name: "Other agent" },
    ]);
    renderEditor();

    expect(await screen.findByText("Architect Sonnet")).toBeInTheDocument();
    expect(screen.queryByText("Other agent")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    const nameInput = screen.getByRole("textbox", { name: "Preset name" });
    await user.clear(nameInput);
    await user.type(nameInput, "Architect Fast");
    await user.click(screen.getByRole("button", { name: "Tier" }));
    await user.click(await screen.findByRole("button", { name: "Fast" }));
    await user.click(screen.getByRole("button", { name: "Save preset" }));

    expect(mocks.updateAgentPreset).toHaveBeenCalledWith("ws-1", "preset-1", {
      name: "Architect Fast",
      agent_id: "agent-1",
      tier_id: "tier-2",
    });
  });
});
