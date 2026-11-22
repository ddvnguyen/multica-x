import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enIssues from "../../../locales/en/issues.json";
import { AssigneePicker } from "./assignee-picker";

const AGENTS = [
  { id: "agent-1", name: "Helper", archived_at: null, visibility: "workspace", tier_ids: [] },
  {
    id: "agent-2",
    name: "Picky",
    archived_at: null,
    visibility: "workspace",
    tier_ids: ["tier-a"],
  },
];

const TIERS = [
  { id: "tier-a", name: "fast", enabled: true, model: "model-a", provider: "codebuddy" },
  { id: "tier-b", name: "slow", enabled: false, model: "model-b", provider: "freebuff" },
];

vi.mock("@tanstack/react-query", () => ({
  useQuery: ({ queryKey }: { queryKey: string[] }) => {
    if (queryKey[0] === "members") return { data: [] };
    if (queryKey[0] === "agents") return { data: AGENTS };
    if (queryKey[0] === "workspaces" || (queryKey as string[]).includes("tiers")) {
      return { data: TIERS };
    }
    return { data: [] };
  },
  // M-493: one member query per eligible tier. The fixtures have no members,
  // so the tree falls back to each tier's legacy provider/model columns.
  useQueries: ({ queries }: { queries: unknown[] }) => queries.map(() => ({ data: [] })),
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/auth", () => ({ useAuthStore: () => ({ id: "user-1" }) }));
vi.mock("@multica/core/agents", () => ({ isAgentRuntimeBound: () => true, isRoleAgent: () => false, useAgentRoleTypeEnabled: () => false }));
vi.mock("@multica/core/permissions", () => ({
  canAssignAgentToIssue: () => ({ allowed: true }),
}));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: () => "Unassigned" }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"] }),
  agentListOptions: () => ({ queryKey: ["agents"] }),
  squadListOptions: () => ({ queryKey: ["squads"] }),
  assigneeFrequencyOptions: () => ({ queryKey: ["frequency"] }),
  modelTiersOptions: () => ({ queryKey: ["workspaces", "ws", "tiers"] }),
  modelTierMembersOptions: (_ws: string, tierId: string) => ({
    queryKey: ["workspaces", "ws", "tiers", tierId, "members"],
  }),
}));
vi.mock("@multica/core/runtimes/queries", () => ({
  runtimeListOptions: () => ({ queryKey: ["runtimes"] }),
}));
vi.mock("../../../common/actor-avatar", () => ({
  ActorAvatar: () => <span data-testid="actor-avatar" />,
}));

// M-493: with `tierPick`, picking an agent advances to the model → harness →
// runtime tier step (Auto default) and confirms agent+tier in one update.
// Disabled tiers are hidden and the list is narrowed to the agent's tiers.
describe("AssigneePicker tier step", () => {
  function renderTierPicker(onUpdate: (u: unknown) => void) {
    return render(
      <I18nProvider locale="en" resources={{ en: { issues: enIssues } }}>
        <AssigneePicker
          assigneeType={null}
          assigneeId={null}
          onUpdate={onUpdate}
          open
          onOpenChange={() => {}}
          tierPick
        />
      </I18nProvider>,
    );
  }

  async function openTierStep(user: ReturnType<typeof userEvent.setup>, agent = "Helper") {
    await user.click(await screen.findByText(agent));
  }

  it("confirms agent+Auto without a tier id", async () => {
    const user = userEvent.setup();
    const onUpdate = vi.fn();
    renderTierPicker(onUpdate);

    await openTierStep(user);
    await user.click(await screen.findByText("Auto (resolver picks)"));

    expect(onUpdate).toHaveBeenCalledWith({
      assignee_type: "agent",
      assignee_id: "agent-1",
    });
  });

  it("walks model → harness → runtime and writes the tier id once", async () => {
    const user = userEvent.setup();
    const onUpdate = vi.fn();
    renderTierPicker(onUpdate);

    await openTierStep(user);
    await user.click(await screen.findByText("model-a"));
    expect(await screen.findByText("Harness")).toBeTruthy();
    await user.click(await screen.findByText("Codebuddy"));
    expect(await screen.findByText("Runtime")).toBeTruthy();
    await user.click(await screen.findByText(/#fast/));

    expect(onUpdate).toHaveBeenCalledWith({
      assignee_type: "agent",
      assignee_id: "agent-1",
      tier_id: "tier-a",
    });
    expect(screen.queryByText(/#slow/)).toBeNull();
  });

  it("shows only the tiers the agent supports", async () => {
    const user = userEvent.setup();
    const onUpdate = vi.fn();
    renderTierPicker(onUpdate);

    await openTierStep(user, "Picky");
    expect(await screen.findByText("model-a")).toBeTruthy();
    expect(screen.queryByText("model-b")).toBeNull();
  });

  it("unassigned picks bypass the tier step", async () => {
    const user = userEvent.setup();
    const onUpdate = vi.fn();
    render(
      <I18nProvider locale="en" resources={{ en: { issues: enIssues } }}>
        <AssigneePicker
          assigneeType={null}
          assigneeId={null}
          onUpdate={onUpdate}
          open
          onOpenChange={() => {}}
          tierPick
        />
      </I18nProvider>,
    );

    const unassigned = await screen.findAllByText("Unassigned");
    const row = unassigned[unassigned.length - 1];
    if (!row) throw new Error("unassigned row missing");
    await user.click(row);

    expect(onUpdate).toHaveBeenCalledWith({ assignee_type: null, assignee_id: null });
  });
});
