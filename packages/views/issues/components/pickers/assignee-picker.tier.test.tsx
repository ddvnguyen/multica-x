import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enIssues from "../../../locales/en/issues.json";
import { AssigneePicker } from "./assignee-picker";

const AGENTS = [{ id: "agent-1", name: "Helper", archived_at: null, visibility: "workspace" }];

vi.mock("@tanstack/react-query", () => ({
  useQuery: ({ queryKey }: { queryKey: string[] }) => {
    if (queryKey[0] === "members") return { data: [] };
    if (queryKey[0] === "agents") return { data: AGENTS };
    if (queryKey[0] === "workspaces" || (queryKey as string[]).includes("tiers")) {
      return {
        data: [
          { id: "tier-a", name: "fast", enabled: true, model: "model-a" },
          { id: "tier-b", name: "slow", enabled: false, model: "model-b" },
        ],
      };
    }
    return { data: [] };
  },
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/auth", () => ({ useAuthStore: () => ({ id: "user-1" }) }));
vi.mock("@multica/core/agents", () => ({ isAgentRuntimeBound: () => true }));
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
}));
vi.mock("../../../common/actor-avatar", () => ({
  ActorAvatar: () => <span data-testid="actor-avatar" />,
}));

// M-359: with `tierPick`, picking an agent advances to a tier step (Auto or
// one enabled tier) and confirms agent+tier in one update. Disabled tiers
// are hidden; plain picks (member/unassigned) bypass the step.
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

  it("confirms agent+Auto without a tier id", async () => {
    const user = userEvent.setup();
    const onUpdate = vi.fn();
    renderTierPicker(onUpdate);

    await user.click(await screen.findByText("Helper"));
    await user.click(await screen.findByText("Auto (resolver picks)"));

    expect(onUpdate).toHaveBeenCalledWith({
      assignee_type: "agent",
      assignee_id: "agent-1",
    });
  });

  it("confirms agent+tier for an enabled tier and hides disabled ones", async () => {
    const user = userEvent.setup();
    const onUpdate = vi.fn();
    renderTierPicker(onUpdate);

    await user.click(await screen.findByText("Helper"));
    await user.click(await screen.findByText(/#fast/));

    expect(onUpdate).toHaveBeenCalledWith({
      assignee_type: "agent",
      assignee_id: "agent-1",
      tier_id: "tier-a",
    });
    expect(screen.queryByText(/#slow/)).toBeNull();
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
    // No members mocked — the empty-value row (first Unassigned text in
    // the popover, past the trigger) confirms immediately with no tier step.
    const matches = await screen.findAllByText("Unassigned");
    const row = matches[matches.length - 1];
    if (!row) throw new Error("unassigned row missing");
    await user.click(row);
    expect(onUpdate).toHaveBeenCalledWith({ assignee_type: null, assignee_id: null });
  });
});
