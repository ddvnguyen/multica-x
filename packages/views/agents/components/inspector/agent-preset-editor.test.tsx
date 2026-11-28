// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { waitFor } from "@testing-library/react";
import { renderWithI18n } from "../../../test/i18n";

const mocks = vi.hoisted(() => ({
  getWorkspace: vi.fn(),
  listAgentPresets: vi.fn(),
  listModelTiers: vi.fn(),
  createAgentPreset: vi.fn(),
  updateAgentPreset: vi.fn(),
  deleteAgentPreset: vi.fn(),
  getAgent: vi.fn(),
  roleTypeEnabled: true,
}));

vi.mock("@multica/core/api", () => ({ api: mocks }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/agents", () => ({
  isRoleAgent: (a: { subtype?: string } | null | undefined) => a?.subtype === "role",
  useAgentRoleTypeEnabled: () => mocks.roleTypeEnabled,
}));
vi.mock("@multica/core/workspace/queries", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@multica/core/workspace/queries")>();
  return { ...mod, agentDetailOptions: () => ({ queryKey: ["agents", "agent-1"] }) };
});

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

const ROLE = { id: "agent-1", name: "Reviewer", subtype: "role" };
const AGENT = { id: "agent-2", name: "Helper", subtype: "agent" };

function renderEditor(agentId = "agent-1") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(["agents", "agent-1"], agentId === "agent-1" ? ROLE : AGENT);
  queryClient.setQueryData(["agents", "agent-2"], AGENT);
  renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <AgentPresetEditor agentId={agentId} canEdit />
    </QueryClientProvider>,
  );
}

describe("AgentPresetEditor", () => {
  beforeEach(() => {
    mocks.roleTypeEnabled = true;
    mocks.getAgent.mockReset().mockResolvedValue(ROLE);
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

  it("hides the editor for plain Agents and keeps their presets stored", async () => {
    mocks.listAgentPresets.mockResolvedValue([
      preset,
      { ...preset, id: "other-preset", agent_id: "agent-2", name: "Other agent" },
    ]);
    renderEditor("agent-2");

    // The editor renders nothing for a plain Agent — the rows exist only in
    // the store for the test to assert they were NOT surfaced or mutated.
    await waitFor(() => expect(mocks.listAgentPresets).toHaveBeenCalled());
    expect(screen.queryByText("Architect Sonnet")).toBeNull();
    expect(screen.queryByRole("textbox", { name: "Preset name" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Add preset" })).toBeNull();
    expect(mocks.createAgentPreset).not.toHaveBeenCalled();
    expect(mocks.updateAgentPreset).not.toHaveBeenCalled();
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
