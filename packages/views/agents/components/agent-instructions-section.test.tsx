// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent, RoleCompositionPreview } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { AgentInstructionsSection } from "./agent-instructions-section";

// M-603: the Instructions section collapses behind its title (persisted per
// agent, default expanded) and shows what an implementing Agent inherits.

const state = vi.hoisted(() => ({
  roleTypeEnabled: true,
  preview: null as RoleCompositionPreview | null,
  previewError: false,
  previewLoading: false,
}));

vi.mock("@multica/core/agents", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/agents")>()),
  useAgentRoleTypeEnabled: () => state.roleTypeEnabled,
  useRolePreview: () => ({
    data: state.preview,
    isLoading: state.previewLoading,
    isError: state.previewError,
  }),
}));

vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useCurrentWorkspace: () => ({ id: "workspace-1" }),
  useWorkspacePaths: () => ({
    agentDetail: (id: string) => `/w/agents/${id}`,
  }),
}));

vi.mock("./tabs/instructions-tab", () => ({
  InstructionsTab: () => <div data-testid="instructions-editor" />,
}));

vi.mock("../../navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../navigation")>()),
  AppLink: ({
    href,
    children,
    ...rest
  }: {
    href: string;
    children: React.ReactNode;
    [k: string]: unknown;
  }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
  useNavigation: () => ({}),
}));

function agent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "agent-1",
    workspace_id: "workspace-1",
    name: "Lambda",
    description: "",
    instructions: "Be helpful.",
    ...overrides,
  } as Agent;
}

function preview(overrides: Partial<RoleCompositionPreview> = {}): RoleCompositionPreview {
  return {
    agent_id: "agent-1",
    role_id: "role-1",
    role_name: "Reviewer",
    instructions: "",
    role_instructions: "Review every change.",
    agent_instructions: "Be helpful.",
    skills: [],
    ...overrides,
  };
}

function renderSection(value: Agent) {
  const onUpdate = vi.fn(async () => {});
  const result = renderWithI18n(
    <AgentInstructionsSection agent={value} canEdit onUpdate={onUpdate} />,
  );
  return { onUpdate, ...result };
}

beforeEach(() => {
  state.roleTypeEnabled = true;
  state.preview = null;
  state.previewError = false;
  state.previewLoading = false;
  window.localStorage.clear();
});

afterEach(() => {
  cleanup();
});

describe("AgentInstructionsSection collapse (M-603)", () => {
  it("starts expanded and collapses behind a titled toggle with aria-expanded", async () => {
    const user = userEvent.setup();
    renderSection(agent());

    const toggle = screen.getByRole("button", { name: "Instructions" });
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Be helpful.")).toBeInTheDocument();

    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Be helpful.")).toBeNull();

    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Be helpful.")).toBeInTheDocument();
  });

  it("remembers the collapsed state per agent across reloads", async () => {
    const user = userEvent.setup();
    renderSection(agent({ id: "agent-9" }));

    await user.click(screen.getByRole("button", { name: "Instructions" }));
    expect(
      window.localStorage.getItem("multica:agent-instructions-collapsed:agent-9"),
    ).toBe("1");

    cleanup();
    renderSection(agent({ id: "agent-9" }));
    expect(
      screen.getByRole("button", { name: "Instructions" }),
    ).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Be helpful.")).toBeNull();

    cleanup();
    renderSection(agent({ id: "agent-other" }));
    expect(
      screen.getByRole("button", { name: "Instructions" }),
    ).toHaveAttribute("aria-expanded", "true");
  });

  it("editing from a collapsed section expands it", async () => {
    const user = userEvent.setup();
    renderSection(agent({ id: "agent-9" }));
    await user.click(screen.getByRole("button", { name: "Instructions" }));
    await user.click(screen.getByRole("button", { name: "Edit" }));
    expect(
      screen.getByRole("button", { name: "Instructions" }),
    ).toHaveAttribute("aria-expanded", "true");
  });
});

describe("AgentInstructionsSection inherited Role (M-603)", () => {
  it("shows the inherited Role name, its instructions, and both skill sources", () => {
    state.preview = preview({
      skills: [
        { id: "s1", name: "review", description: "", source: "role" },
        { id: "s2", name: "notes", description: "", source: "agent" },
      ],
    });
    state.preview = preview({
      skills: [
        { id: "s1", name: "review", description: "", source: "role" },
        { id: "s2", name: "notes", description: "", source: "agent" },
      ],
    });
    renderSection(agent({ implements_role_id: "role-1" }));

    const link = screen.getByRole("link", { name: "Inherited from Reviewer" });
    expect(link).toHaveAttribute("href", "/w/agents/role-1");
    expect(screen.getByText("Review every change.")).toBeInTheDocument();
    expect(screen.getByText("Be helpful.")).toBeInTheDocument();
    expect(screen.getByText("Role")).toBeInTheDocument();
    expect(screen.getByText("Agent")).toBeInTheDocument();

    // Server composition order: Role instructions, Agent additions, union.
    const block = screen.getByText("Review every change.").closest("div.rounded-lg")!;
    const text = block.textContent ?? "";
    expect(text.indexOf("Review every change.")).toBeLessThan(
      text.indexOf("Be helpful."),
    );
    expect(text.indexOf("Be helpful.")).toBeLessThan(text.indexOf("review"));
  });

  it("says there are no additions when the server composition has none", () => {
    state.preview = preview({
      role_instructions: "Review every change.",
      agent_instructions: "",
    });
    renderSection(agent({ implements_role_id: "role-1", instructions: "stale local text" }));

    expect(screen.getByText("Review every change.")).toBeInTheDocument();
    expect(
      screen.getByText("No additions; inherits the Role."),
    ).toBeInTheDocument();
    expect(screen.queryByText("stale local text")).toBeNull();
  });

  it("hides the inherited block while loading or on error, falling back to own instructions", () => {
    state.previewLoading = true;
    const { unmount } = renderSection(agent({ implements_role_id: "role-1" }));
    expect(screen.queryByRole("link", { name: /Inherited from/ })).toBeNull();
    expect(screen.getByText("Be helpful.")).toBeInTheDocument();
    unmount();

    state.previewLoading = false;
    state.previewError = true;
    renderSection(agent({ implements_role_id: "role-1", instructions: "Be helpful." }));
    expect(screen.queryByRole("link", { name: /Inherited from/ })).toBeNull();
    expect(screen.getByText("Be helpful.")).toBeInTheDocument();
  });

  it("shows nothing extra for an Agent with no Role", () => {
    state.preview = preview();
    renderSection(agent());

    expect(screen.queryByRole("link", { name: /Inherited from/ })).toBeNull();
    expect(screen.getByText("Be helpful.")).toBeInTheDocument();
  });

  it("shows the Role instructions title and hint for a Role agent", () => {
    renderSection(agent({ id: "role-1", subtype: "role" }));

    expect(
      screen.getByRole("button", { name: "Role instructions" }),
    ).toHaveAttribute("aria-expanded", "true");
    expect(
      screen.getByText("Every Agent that implements this Role inherits these."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Inherited from/ })).toBeNull();
  });

  it("shows an explicit empty state with an Edit action for a new Role", () => {
    renderSection(agent({ id: "role-1", subtype: "role", instructions: "" }));

    expect(
      screen.getByText(
        "No instructions yet. Agents implementing this Role inherit nothing until you add some.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument();
  });

  it("lists a Role's own skills read-only with their source", () => {
    renderSection(
      agent({
        id: "role-1",
        subtype: "role",
        instructions: "Review everything.",
        skills: [{ id: "s1", name: "review", description: "" }],
      }),
    );
    expect(screen.getByText("Review everything.")).toBeInTheDocument();
    expect(screen.getByText("Role skills")).toBeInTheDocument();
    expect(screen.getByText("review")).toBeInTheDocument();
    expect(screen.getByText("Role")).toBeInTheDocument();
  });

  it("collapses the inherited block with the section", async () => {
    const user = userEvent.setup();
    state.preview = preview();
    renderSection(agent({ implements_role_id: "role-1" }));

    expect(
      screen.getByRole("link", { name: "Inherited from Reviewer" }),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Instructions" }));
    expect(screen.queryByRole("link", { name: /Inherited from/ })).toBeNull();
  });
});
