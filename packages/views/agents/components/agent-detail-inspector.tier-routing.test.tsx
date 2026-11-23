// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { AgentDetailInspector } from "./agent-detail-inspector";

// M-545: the Agent form is split by Agent type. An Agent picks runtime and
// model; a Role picks its tier table. Only an agent that still carries the
// old routing (`use_tiers` or a tier-table role) sees the legacy block.

vi.mock("@multica/core/agents", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/agents")>()),
  useAgentRoleTypeEnabled: () => true,
}));

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: [], isSuccess: true }),
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
    description: "Test agent",
    runtime_id: "runtime-1",
    ...overrides,
  } as Agent;
}

function renderInspector(value: Agent) {
  return renderWithI18n(
    <AgentDetailInspector
      agent={value}
      runtime={null}
      runtimes={[]}
      members={[]}
      currentUserId="user-1"
      canEdit
      onUpdate={vi.fn(async () => {})}
    />,
  );
}

describe("AgentDetailInspector Agent vs Agent-Role", () => {
  afterEach(() => {
    cleanup();
  });

  it("gives a plain Agent its runtime, its model and an optional Implements Role", () => {
    renderInspector(agent());

    expect(screen.getByTestId("runtime-picker")).toBeInTheDocument();
    expect(screen.getByTestId("model-picker")).toBeInTheDocument();
    expect(screen.getByText("Implements Role")).toBeInTheDocument();
    // No tier controls on a normal Agent.
    expect(screen.queryByText("Use tiers")).toBeNull();
    expect(screen.queryByText("Role")).toBeNull();
    expect(screen.queryByText("Tier role")).toBeNull();
    expect(screen.queryByTestId("legacy-tier-routing")).toBeNull();
  });

  it("keeps one compact legacy block for an agent that still uses tiers", () => {
    renderInspector(agent({ use_tiers: true }));

    expect(screen.getByTestId("legacy-tier-routing")).toBeInTheDocument();
    expect(screen.getByText("Use tiers")).toBeInTheDocument();
    expect(screen.getByText("Role")).toBeInTheDocument();
    expect(screen.getByTestId("tier-picker")).toBeInTheDocument();
    // Tier routing replaces the runtime/model pair.
    expect(screen.queryByTestId("runtime-picker")).toBeNull();
    expect(screen.queryByTestId("model-picker")).toBeNull();
  });

  it("keeps the legacy block for an agent left with a tier-table role", () => {
    renderInspector(agent({ role: "engineer" }));

    expect(screen.getByTestId("legacy-tier-routing")).toBeInTheDocument();
    expect(screen.getByText("Role")).toBeInTheDocument();
    // Tiers are off, so the agent routes by its own runtime again.
    expect(screen.getByTestId("runtime-picker")).toBeInTheDocument();
    expect(screen.queryByTestId("tier-picker")).toBeNull();
  });

  it("gives an Agent-Role only its tier Role, no runtime or model", () => {
    renderInspector(agent({ subtype: "role" }));

    expect(screen.getByText("Tier role")).toBeInTheDocument();
    expect(screen.queryByTestId("runtime-picker")).toBeNull();
    expect(screen.queryByTestId("model-picker")).toBeNull();
    expect(screen.queryByText("Use tiers")).toBeNull();
    expect(screen.queryByText("Implements Role")).toBeNull();
    expect(screen.queryByTestId("legacy-tier-routing")).toBeNull();
    expect(screen.getByTestId("preset-editor")).toBeInTheDocument();
  });
});
