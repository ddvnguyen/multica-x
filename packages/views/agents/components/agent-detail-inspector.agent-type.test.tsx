// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { AgentDetailInspector } from "./agent-detail-inspector";

// M-567: the type property drives a guarded conversion. These tests cover the
// select, the per-mode field sets, the confirm copy, and the Role's
// "Inherited by" list derived from the workspace's agents.

const state = vi.hoisted(() => ({
  roleTypeEnabled: true,
  workspaceAgents: [] as Agent[],
}));

vi.mock("@multica/core/agents", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/agents")>()),
  useAgentRoleTypeEnabled: () => state.roleTypeEnabled,
}));

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: (options: { queryKey?: readonly unknown[] }) => {
    const key = options.queryKey ?? [];
    // agentListOptions -> ["workspaces", wsId, "agents"]
    if (key.length === 3 && key[2] === "agents") {
      return { data: state.workspaceAgents, isSuccess: true };
    }
    return { data: undefined, isSuccess: true };
  },
}));

vi.mock("./inspector/use-execution-controls", () => ({
  useExecutionControls: () => ({
    qoderRuntime: null,
    canDiscoverRuntimeModels: false,
    runtimeOptions: [],
    changeModel: vi.fn(),
  }),
}));

vi.mock("./inspector/agent-preset-editor", () => ({
  AgentPresetEditor: () => <div data-testid="preset-editor" />,
}));
vi.mock("../../common/avatar-upload-control", () => ({
  AvatarUploadControl: () => <div data-testid="avatar-upload" />,
}));
vi.mock("./inspector/model-picker", () => ({
  ModelPicker: () => <div data-testid="model-picker" />,
}));
vi.mock("./inspector/runtime-picker", () => ({
  RuntimePicker: () => <div data-testid="runtime-picker" />,
}));
vi.mock("./inspector/thinking-prop-row", () => ({
  ThinkingSettingField: () => <div data-testid="thinking-field" />,
}));
vi.mock("./inspector/service-tier-setting-field", () => ({
  ServiceTierSettingField: () => <div data-testid="service-tier-field" />,
}));
vi.mock("./inspector/tier-picker", () => ({
  TierPicker: () => <div data-testid="tier-picker" />,
}));

function agent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "agent-1",
    workspace_id: "workspace-1",
    name: "Lambda",
    description: "",
    runtime_id: "runtime-1",
    ...overrides,
  } as Agent;
}

function implementer(id: string, name: string): Agent {
  return agent({ id, name, implements_role_id: "role-1" });
}

function renderInspector(value: Agent, onUpdate = vi.fn(async () => {})) {
  renderWithI18n(
    <AgentDetailInspector
      agent={value}
      runtime={null}
      runtimes={[]}
      members={[]}
      currentUserId="user-1"
      canEdit
      onUpdate={onUpdate}
    />,
  );
  return onUpdate;
}

describe("AgentDetailInspector Agent Type (M-567)", () => {
  beforeEach(() => {
    state.roleTypeEnabled = true;
    state.workspaceAgents = [];
  });
  afterEach(() => {
    cleanup();
  });

  it("shows the type select in both modes and hides it when the flag is off", () => {
    renderInspector(agent());
    const select = screen.getByRole("combobox", { name: "Agent Type" });
    expect(select).toHaveValue("agent");
    expect(screen.getByTestId("runtime-picker")).toBeInTheDocument();

    cleanup();
    renderInspector(agent({ id: "role-1", subtype: "role", runtime_id: "" }));
    expect(screen.getByRole("combobox", { name: "Agent Type" })).toHaveValue(
      "role",
    );
    expect(screen.queryByTestId("runtime-picker")).toBeNull();
    expect(screen.getByText("Tier role")).toBeInTheDocument();

    cleanup();
    state.roleTypeEnabled = false;
    renderInspector(agent({ id: "agent-3" }));
    expect(screen.queryByRole("combobox", { name: "Agent Type" })).toBeNull();
  });

  it("confirms Agent → Role with the cleared-link note before writing", async () => {
    const user = userEvent.setup();
    const onUpdate = renderInspector(
      agent({ implements_role_id: "role-1" }),
    );

    await user.selectOptions(
      screen.getByRole("combobox", { name: "Agent Type" }),
      "role",
    );
    expect(
      screen.getByText(/Implements Role link will be cleared/i),
    ).toBeInTheDocument();
    expect(onUpdate).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onUpdate).toHaveBeenCalledWith("agent-1", { subtype: "role" });
  });

  it("blocks Role → Agent with the implementer count and names", async () => {
    const user = userEvent.setup();
    state.workspaceAgents = [
      implementer("a-1", "Alpha"),
      implementer("a-2", "Beta"),
    ];
    const onUpdate = renderInspector(
      agent({ id: "role-1", subtype: "role", runtime_id: "" }),
    );

    expect(screen.getByText("Inherited by")).toBeInTheDocument();
    expect(screen.getByText("Alpha")).toBeInTheDocument();
    expect(screen.getByText("Beta")).toBeInTheDocument();

    await user.selectOptions(
      screen.getByRole("combobox", { name: "Agent Type" }),
      "agent",
    );
    expect(
      screen.getByText(/2 agents implement this Role; unlink them first/i),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(onUpdate).not.toHaveBeenCalled();
  });

  it("confirms Role → Agent when nobody implements the Role", async () => {
    const user = userEvent.setup();
    const onUpdate = renderInspector(
      agent({ id: "role-1", subtype: "role", runtime_id: "", tier_role: "engineer" }),
    );
    expect(screen.getByText("No agents implement this Role")).toBeInTheDocument();

    await user.selectOptions(
      screen.getByRole("combobox", { name: "Agent Type" }),
      "agent",
    );
    expect(screen.getByText(/Needs a runtime/i)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => {
      expect(onUpdate).toHaveBeenCalledWith("role-1", { subtype: "agent" });
    });
  });
});
