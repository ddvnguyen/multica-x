// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enIssues from "../../../locales/en/issues.json";
import { AssigneePicker } from "./assignee-picker";

afterEach(() => {
  cleanup();
});
const TIERS = [
  { id: "tier-a", name: "fast", enabled: true, model: "model-a", provider: "codebuddy" },
];

const ROLE = {
  id: "role-1",
  name: "Reviewer",
  archived_at: null,
  visibility: "workspace",
  subtype: "role",
  tier_role: "engineer",
  tier_ids: [],
};
const AGENT = {
  id: "agent-1",
  name: "Helper",
  archived_at: null,
  visibility: "workspace",
  tier_ids: [],
};

vi.mock("@tanstack/react-query", () => ({
  useQuery: ({ queryKey }: { queryKey: string[] }) => {
    if (queryKey[0] === "members") return { data: [] };
    if (queryKey[0] === "agents") return { data: [ROLE, AGENT] };
    if (queryKey[0] === "workspaces" || (queryKey as string[]).includes("tiers")) {
      return { data: TIERS };
    }
    return { data: [] };
  },
  useQueries: ({ queries }: { queries: unknown[] }) => queries.map(() => ({ data: [] })),
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/auth", () => ({ useAuthStore: () => ({ id: "user-1" }) }));
vi.mock("@multica/core/agents", () => ({
  isAgentRuntimeBound: () => true,
  isRoleAgent: (a: { subtype?: string }) => a?.subtype === "role",
  useAgentRoleTypeEnabled: () => true,
}));
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

// M-499 slice 4: Roles partition out of the agent list with a Role badge;
// picking a Role confirms agent+role in one update (same assignee contract).
describe("AssigneePicker roles section", () => {
  function renderPicker(onUpdate: (u: unknown) => void) {
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
  it("shows the Roles section with a Role badge beside Agents", async () => {
    renderPicker(vi.fn());
    const user = userEvent.setup();
    // The Agent flow is untouched: Helper still lists under Agents.
    expect(await screen.findByText("Helper")).toBeTruthy();
    expect(await screen.findByText("Roles")).toBeTruthy();
    expect(await screen.findByText("Reviewer")).toBeTruthy();
    expect(await screen.findByText("Role")).toBeTruthy();
    await user.click(await screen.findByText("Reviewer"));
    // Role tier step opens (empty tiers fixture): back row names the Role.
    expect(await screen.findAllByText(/Reviewer/)).not.toHaveLength(0);
  });

  it("confirms a Role pick as assignee agent+id in one update", async () => {
    const onUpdate = vi.fn();
    renderPicker(onUpdate);
    const user = userEvent.setup();

    await user.click(await screen.findByText("Reviewer"));
    // Role tier step opens: the back row names the picked Role.
    expect(await screen.findByText(/← Reviewer/)).toBeTruthy();
    const autos = await screen.findAllByText("Auto (resolver picks)");
    await user.click(autos[autos.length - 1]!);

    expect(onUpdate).toHaveBeenCalledWith({
      assignee_type: "agent",
      assignee_id: "role-1",
    });
  });
});
