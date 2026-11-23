// @vitest-environment jsdom

import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { ApiError } from "@multica/core/api";
import { I18nProvider } from "@multica/core/i18n/react";
import enSettings from "../../locales/en/settings.json";

const mockCreate = vi.hoisted(() => vi.fn());
const mockUpdate = vi.hoisted(() => vi.fn());
const mockDelete = vi.hoisted(() => vi.fn());
const mockReorder = vi.hoisted(() => vi.fn());
const mockCreateMember = vi.hoisted(() => vi.fn());
const mockUpdateMember = vi.hoisted(() => vi.fn());
const mockDeleteMember = vi.hoisted(() => vi.fn());
const mockCreateRole = vi.hoisted(() => vi.fn());
const mockRenameRole = vi.hoisted(() => vi.fn());
const mockDeleteRole = vi.hoisted(() => vi.fn());

const tier = (over: Record<string, unknown>) => ({
  id: "tier-1",
  workspace_id: "workspace-1",
  name: "fast",
  idx: 0,
  provider: "freebuff",
  model: "deepseek-v4.1-flash",
  provider_slots: 1,
  enabled: true,
  roles: [],
  manual_only: false,
  auto_pick: true,
  quota_kind: "none",
  quota_kinds: [],
  is_free: true,
  ctx_max_tokens: 280000,
  compact_at_pct: 75,
  created_at: "2026-08-14T00:00:00Z",
  updated_at: "2026-08-14T00:00:00Z",
  ...over,
});

const member = (over: Record<string, unknown>) => ({
  id: "member-1",
  tier_id: "tier-1",
  position: 0,
  provider: "freebuff",
  model: "deepseek-v4.1-flash",
  provider_slots: 1,
  enabled: true,
  created_at: "2026-08-14T00:00:00Z",
  updated_at: "2026-08-14T00:00:00Z",
  ...over,
});

const data = vi.hoisted(() => ({
  tiers: [] as Array<Record<string, unknown>>,
  // Per-role lists (M-498): the tab fetches one role's tiers at a time.
  tiersByRole: {} as Record<string, Array<Record<string, unknown>>>,
  roles: [] as Array<{ key: string; position: number }>,
  members: {} as Record<string, Array<Record<string, unknown>>>,
  runtimes: [] as Array<Record<string, unknown>>,
  models: [] as Array<{ id: string; label: string }>,
  isLoading: false,
  role: "owner" as "owner" | "admin" | "member",
}));

/**
 * One mock for every query in the tab and the dialog: the role registry, the
 * per-role tier list, the per-tier member queries, the runtime list and
 * per-runtime model discovery. The key shape decides which fixture answers.
 */
function queryResult(options: { queryKey?: ReadonlyArray<unknown> }) {
  const key = options.queryKey ?? [];
  if (key.includes("members")) {
    const tierId = String(key[3] ?? "");
    return {
      data: data.members[tierId] ?? [],
      isLoading: false,
      isSuccess: true,
      isError: false,
      isPending: false,
      refetch: vi.fn(),
    };
  }
  if (key.includes("roles")) {
    return { data: data.roles, isLoading: false, isSuccess: true };
  }
  if (key.includes("tiers")) {
    const role = key.length === 4 ? String(key[3] ?? "") : "";
    const list =
      role !== "" && data.tiersByRole[role] ? data.tiersByRole[role]! : data.tiers;
    return { data: list, isLoading: data.isLoading, isSuccess: true };
  }
  if (key.includes("list")) {
    return { data: data.runtimes, isLoading: false, isSuccess: true };
  }
  return {
    data: { models: data.models, supported: true },
    isLoading: false,
    isPending: false,
    isError: false,
    isSuccess: true,
    refetch: vi.fn(),
  };
}

vi.mock("@tanstack/react-query", () => ({
  queryOptions: <T,>(options: T) => options,
  useQuery: (options: { queryKey?: ReadonlyArray<unknown> }) => queryResult(options),
  useQueries: ({ queries }: { queries: Array<{ queryKey?: ReadonlyArray<unknown> }> }) =>
    queries.map((query) => queryResult(query)),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  modelTiersOptions: (_wsId: string, role?: string) => ({
    queryKey: role
      ? ["workspaces", "workspace-1", "tiers", role]
      : ["workspaces", "workspace-1", "tiers"],
  }),
  tierRolesOptions: () => ({
    queryKey: ["workspaces", "workspace-1", "tiers", "roles"],
  }),
  modelTierMembersOptions: (_wsId: string, tierId: string) => ({
    queryKey: ["workspaces", "workspace-1", "tiers", tierId, "members"],
  }),
  memberListOptions: () => ({ queryKey: ["workspaces", "workspace-1", "members"] }),
}));

vi.mock("@multica/core/workspace/mutations", () => ({
  useCreateModelTier: () => ({ mutateAsync: mockCreate, isPending: false }),
  useUpdateModelTier: () => ({ mutateAsync: mockUpdate, isPending: false }),
  useDeleteModelTier: () => ({ mutate: mockDelete, isPending: false }),
  useReorderModelTiers: () => ({ mutateAsync: mockReorder, isPending: false }),
  useCreateModelTierMember: () => ({
    mutateAsync: mockCreateMember,
    isPending: false,
  }),
  useUpdateModelTierMember: () => ({
    mutateAsync: mockUpdateMember,
    isPending: false,
  }),
  useDeleteModelTierMember: () => ({
    mutateAsync: mockDeleteMember,
    isPending: false,
  }),
  useCreateTierRole: () => ({ mutateAsync: mockCreateRole, isPending: false }),
  useRenameTierRole: () => ({ mutateAsync: mockRenameRole, isPending: false }),
  useDeleteTierRole: () => ({ mutateAsync: mockDeleteRole, isPending: false }),
}));

vi.mock("@multica/core/runtimes", () => ({
  runtimeListOptions: () => ({ queryKey: ["runtimes", "workspace-1", "list"] }),
  runtimeModelsOptions: () => ({ queryKey: ["runtimes", "models"] }),
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "workspace-1", name: "Acme", slug: "acme" }),
}));

vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: data.role, isLoading: false }),
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { TiersTab } from "./tiers-tab";

const TEST_RESOURCES = {
  en: { settings: enSettings },
};

function Wrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

describe("TiersTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    data.role = "owner";
    data.isLoading = false;
    data.tiers = [
      tier({ id: "tier-1", name: "fast", idx: 0 }),
      tier({ id: "tier-2", name: "fallback", idx: 1, enabled: false }),
    ];
    data.roles = [
      { key: "architect", position: 0 },
      { key: "management", position: 1 },
      { key: "engineer", position: 2 },
    ];
    data.tiersByRole = {};
    data.runtimes = [];
    data.models = [];
    data.members = {
      "tier-1": [
        member({ id: "member-1", position: 0 }),
        member({
          id: "member-2",
          position: 1,
          provider: "codebuddy",
          model: "eng-cb-deepseek",
        }),
      ],
      "tier-2": [member({ id: "member-3", tier_id: "tier-2" })],
    };
    mockUpdateMember.mockResolvedValue({});
    mockCreateMember.mockResolvedValue({ id: "member-new" });
    mockDeleteMember.mockResolvedValue(undefined);
  });

  it("lists tiers ordered with the disabled badge", () => {
    render(<TiersTab />, { wrapper: Wrapper });
    expect(screen.getByTestId("tier-row-tier-1")).toBeInTheDocument();
    expect(screen.getByTestId("tier-row-tier-2")).toBeInTheDocument();
    expect(screen.getByText("Disabled")).toBeInTheDocument();
  });

  it("shows every member of a tier as a harness + model chip", () => {
    render(<TiersTab />, { wrapper: Wrapper });
    const chips = within(screen.getByTestId("tier-members-tier-1"));
    expect(chips.getByTestId("tier-member-tier-1-member-1")).toHaveTextContent(
      "freebuff · deepseek-v4.1-flash",
    );
    expect(chips.getByTestId("tier-member-tier-1-member-2")).toHaveTextContent(
      "codebuddy · eng-cb-deepseek",
    );
  });

  it("marks a tier that resolves automatically with the auto-pick badge", () => {
    data.tiers = [
      tier({ id: "tier-1", name: "fast", auto_pick: true }),
      tier({ id: "tier-2", name: "fallback", auto_pick: false, manual_only: true }),
    ];
    render(<TiersTab />, { wrapper: Wrapper });
    expect(screen.getByTestId("tier-auto-pick-tier-1")).toHaveTextContent(
      "auto-pick",
    );
    expect(screen.getByTestId("tier-auto-pick-tier-2")).toHaveTextContent(
      "manual only",
    );
  });

  it("warns in the list when a tier has no members", () => {
    data.members = { "tier-1": [], "tier-2": [] };
    render(<TiersTab />, { wrapper: Wrapper });
    expect(
      screen.getByTestId("tier-no-members-tier-1"),
    ).toHaveTextContent(/no members/i);
  });

  it("shows the cooldown badge for a tier whose provider is cooling", () => {
    const until = new Date(Date.now() + 15 * 60_000);
    data.tiers = [
      tier({ id: "tier-1", name: "fast", idx: 0, cooldown_until: until.toISOString() }),
    ];
    render(<TiersTab />, { wrapper: Wrapper });
    const badge = screen.getByTestId("tier-cooldown-tier-1");
    const hh = String(until.getHours()).padStart(2, "0");
    const mm = String(until.getMinutes()).padStart(2, "0");
    expect(badge).toHaveTextContent(`Cooling down until ${hh}:${mm}`);
  });

  it("hides the cooldown badge once the deadline passed", () => {
    data.tiers = [
      tier({
        id: "tier-1",
        name: "fast",
        cooldown_until: new Date(Date.now() - 60_000).toISOString(),
      }),
    ];
    render(<TiersTab />, { wrapper: Wrapper });
    expect(screen.queryByTestId("tier-cooldown-tier-1")).toBeNull();
    expect(screen.queryByText(/cooling down/i)).toBeNull();
  });

  it("names the role in the empty state of its tab", () => {
    data.tiers = [];
    render(<TiersTab />, { wrapper: Wrapper });
    expect(screen.getByText("No tiers in Architect yet")).toBeInTheDocument();
  });

  it("renders the read-only notice for members and no management actions", () => {
    data.role = "member";
    render(<TiersTab />, { wrapper: Wrapper });
    expect(screen.getByTestId("tier-row-tier-1")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /add tier/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /edit/i })).toBeNull();
    expect(
      screen.getByText(/Only workspace owners and admins/),
    ).toBeInTheDocument();
  });

  it("moves a tier down through the reorder mutation, scoped to the tab's role", async () => {
    mockReorder.mockResolvedValue([]);
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /move fast down/i }));
    await waitFor(() => {
      expect(mockReorder).toHaveBeenCalledWith({
        ids: ["tier-2", "tier-1"],
        role: "architect",
      });
    });
  });

  it("opens a large, scrollable editor", async () => {
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /add tier/i }));
    const editor = await screen.findByTestId("tier-editor");
    expect(editor.className).toContain("sm:max-w-[960px]");
    expect(editor.className).toContain("overflow-hidden");
    const body = screen.getByTestId("tier-editor-body");
    expect(body.className).toContain("overflow-y-auto");
  });

  it("offers the registry roles behind a tag selector", async () => {
    data.roles = [
      ...data.roles,
      { key: "reviewer", position: 3 },
    ];
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /add tier/i }));
    await screen.findByTestId("tier-editor");
    // The preselected tab's role renders as a tag, not as a ticked checkbox.
    expect(screen.getByTestId("tier-role-tag-architect")).toHaveTextContent(
      "Architect",
    );
    expect(screen.queryByRole("checkbox", { name: "Architect" })).toBeNull();

    fireEvent.click(screen.getByTestId("tier-roles-add"));
    // A workspace-defined role is offered by its own key.
    for (const label of ["Architect", "Management", "Engineer", "reviewer"]) {
      expect(
        await screen.findByRole("checkbox", { name: label }),
      ).toBeInTheDocument();
    }
    // A role the registry does not carry is still not offered.
    expect(screen.queryByRole("checkbox", { name: "Leader" })).toBeNull();
  });

  it("renders role picks as tags and clears them back to None", async () => {
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /add tier/i }));
    await screen.findByTestId("tier-editor");

    // The tag's own remove control deselects the role.
    fireEvent.click(screen.getByRole("button", { name: "Remove Architect" }));
    expect(screen.queryByTestId("tier-role-tag-architect")).toBeNull();
    expect(screen.getByTestId("tier-roles-none")).toHaveTextContent("None");

    fireEvent.click(screen.getByTestId("tier-roles-add"));
    fireEvent.click(await screen.findByRole("checkbox", { name: "Management" }));
    expect(screen.getByTestId("tier-role-tag-management")).toHaveTextContent(
      "Management",
    );
  });

  it("manages the role registry from the Tiers tab", async () => {
    mockCreateRole.mockResolvedValue([]);
    mockRenameRole.mockResolvedValue([]);
    mockDeleteRole.mockResolvedValue([]);
    render(<TiersTab />, { wrapper: Wrapper });

    fireEvent.click(screen.getByTestId("tier-manage-roles"));
    await screen.findByTestId("tier-roles-dialog");
    expect(screen.getByTestId("tier-role-row-engineer")).toHaveTextContent(
      "Engineer",
    );

    fireEvent.change(screen.getByTestId("tier-role-new"), {
      target: { value: "Reviewer" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add role" }));
    await waitFor(() => {
      expect(mockCreateRole).toHaveBeenCalledWith("reviewer");
    });

    fireEvent.click(screen.getByRole("button", { name: "Rename Engineer" }));
    const renameInput = screen.getByTestId("tier-role-rename-input");
    fireEvent.change(renameInput, { target: { value: "builder" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => {
      expect(mockRenameRole).toHaveBeenCalledWith({
        key: "engineer",
        newKey: "builder",
      });
    });
  });

  it("confirms before removing a role and explains a role still in use", async () => {
    mockDeleteRole.mockRejectedValueOnce(
      new ApiError("role is used by a tier", 409, "Conflict", {
        code: "role_in_use",
      }),
    );
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByTestId("tier-manage-roles"));
    await screen.findByTestId("tier-roles-dialog");

    // Remove asks first; nothing is deleted on the first click.
    fireEvent.click(screen.getByRole("button", { name: "Remove Architect" }));
    expect(mockDeleteRole).not.toHaveBeenCalled();
    fireEvent.click(screen.getByTestId("tier-role-delete-confirm"));
    await waitFor(() => {
      expect(mockDeleteRole).toHaveBeenCalledWith("architect");
    });
    expect(
      await screen.findByText(/A tier still uses this role/i),
    ).toBeInTheDocument();
  });

  it("rejects a non-slug role key before calling the API", async () => {
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByTestId("tier-manage-roles"));
    await screen.findByTestId("tier-roles-dialog");
    fireEvent.change(screen.getByTestId("tier-role-new"), {
      target: { value: "Not A Slug" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add role" }));
    expect(
      screen.getByText(/Use 1-40 lowercase letters/i),
    ).toBeInTheDocument();
    expect(mockCreateRole).not.toHaveBeenCalled();
  });

  it("offers the seven quota kinds behind one selector", async () => {
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /add tier/i }));
    await screen.findByTestId("tier-editor");
    // The dialog itself shows no quota checkbox until the selector opens.
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
    fireEvent.click(screen.getByTestId("tier-quota-add"));
    for (const label of [
      "Free daily",
      "Free",
      "Local",
      "Quota 5 hours",
      "Quota daily",
      "Quota weekly",
      "Quota monthly",
    ]) {
      expect(
        await screen.findByRole("checkbox", { name: label }),
      ).toBeInTheDocument();
    }
  });

  it("renders quota picks as tags and clears them back to None", async () => {
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /add tier/i }));
    await screen.findByTestId("tier-editor");
    expect(screen.getByTestId("tier-quota-none")).toHaveTextContent("None");

    fireEvent.click(screen.getByTestId("tier-quota-add"));
    fireEvent.click(await screen.findByRole("checkbox", { name: "Quota weekly" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Free daily" }));
    expect(screen.getByTestId("tier-quota-tag-quota_weekly")).toHaveTextContent(
      "Quota weekly",
    );
    expect(screen.getByTestId("tier-quota-tag-free_daily")).toHaveTextContent(
      "Free daily",
    );
    expect(screen.queryByTestId("tier-quota-none")).toBeNull();

    // Unticking in the selector removes the tag too.
    fireEvent.click(screen.getByRole("checkbox", { name: "Free daily" }));
    expect(screen.queryByTestId("tier-quota-tag-free_daily")).toBeNull();

    // The tag's own remove control is the second way to deselect.
    fireEvent.click(
      screen.getByRole("button", { name: "Remove Quota weekly" }),
    );
    expect(screen.queryByTestId("tier-quota-tag-quota_weekly")).toBeNull();
    expect(screen.getByTestId("tier-quota-none")).toHaveTextContent("None");
  });

  it("marks context and compaction advisory and shows the derived token count", async () => {
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /add tier/i }));
    await screen.findByTestId("tier-editor");
    expect(screen.getByTestId("tier-context-advisory")).toHaveTextContent(
      "advisory",
    );
    // M-543: the badge is the only "advisory" on the field; the duplicate
    // label above it came from a second copy of the same string.
    expect(screen.getAllByText("advisory")).toHaveLength(1);
    expect(screen.getByText(/about 210,000 tokens/i)).toBeInTheDocument();
  });

  it("adds and removes member lines in the editor", async () => {
    data.members = { "tier-1": [member({ id: "member-1" })] };
    data.tiers = [tier({ id: "tier-1", name: "fast", idx: 0 })];
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /edit/i }));
    await screen.findByTestId("tier-member-row-0");
    fireEvent.click(screen.getByRole("button", { name: /add member/i }));
    expect(screen.getByTestId("tier-member-row-1")).toBeInTheDocument();
    fireEvent.click(
      screen.getAllByRole("button", { name: /remove member/i })[1]!,
    );
    expect(screen.queryByTestId("tier-member-row-1")).toBeNull();
    expect(screen.getByTestId("tier-member-row-0")).toBeInTheDocument();
  });

  it("creates a tier with the profile fields and its first member", async () => {
    mockCreate.mockResolvedValue({ id: "tier-new" });
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /add tier/i }));
    const editor = await screen.findByTestId("tier-editor");
    fireEvent.change(editor.querySelector("#tier-name") as HTMLInputElement, {
      target: { value: "flash" },
    });
    fireEvent.change(screen.getByLabelText("Harness"), {
      target: { value: "freebuff" },
    });
    fireEvent.change(screen.getByLabelText("Model"), {
      target: { value: "deepseek-v4.1-flash" },
    });
    // "Add tier" was pressed on the Architect tab, so Architect is preselected.
    fireEvent.click(screen.getByTestId("tier-roles-add"));
    fireEvent.click(await screen.findByRole("checkbox", { name: "Engineer" }));
    fireEvent.click(screen.getByTestId("tier-quota-add"));
    fireEvent.click(
      await screen.findByRole("checkbox", { name: "Quota weekly" }),
    );
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));
    await waitFor(() => {
      expect(mockCreate).toHaveBeenCalledTimes(1);
    });
    expect(mockCreate.mock.calls[0]![0]).toMatchObject({
      name: "flash",
      provider: "freebuff",
      model: "deepseek-v4.1-flash",
      roles: ["architect", "engineer"],
      auto_pick: true,
      quota_kinds: ["quota_weekly"],
      ctx_max_tokens: 280000,
      compact_at_pct: 75,
    });
    // is_free is derived from the quota kinds on the server (M-497).
    expect(mockCreate.mock.calls[0]![0]).not.toHaveProperty("is_free");
  });

  it("adds the remaining members of a new tier after the profile", async () => {
    mockCreate.mockResolvedValue({ id: "tier-new" });
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /add tier/i }));
    const editor = await screen.findByTestId("tier-editor");
    fireEvent.change(editor.querySelector("#tier-name") as HTMLInputElement, {
      target: { value: "flash" },
    });
    fireEvent.change(screen.getByLabelText("Harness"), {
      target: { value: "freebuff" },
    });
    fireEvent.change(screen.getByLabelText("Model"), {
      target: { value: "deepseek-v4.1-flash" },
    });
    fireEvent.click(screen.getByRole("button", { name: /add member/i }));
    fireEvent.change(screen.getAllByLabelText("Harness")[1]!, {
      target: { value: "codebuddy" },
    });
    fireEvent.change(screen.getAllByLabelText("Model")[1]!, {
      target: { value: "eng-cb-deepseek" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));
    await waitFor(() => {
      expect(mockCreateMember).toHaveBeenCalledTimes(1);
    });
    expect(mockCreateMember.mock.calls[0]![0]).toMatchObject({
      tierId: "tier-new",
      provider: "codebuddy",
      model: "eng-cb-deepseek",
      position: 1,
    });
  });

  it("reorders members and writes the new pick order on save", async () => {
    mockUpdate.mockResolvedValue({ id: "tier-1" });
    data.members = {
      "tier-1": [
        member({ id: "member-1", position: 0, provider: "freebuff", model: "a" }),
        member({ id: "member-2", position: 1, provider: "codebuddy", model: "b" }),
      ],
    };
    data.tiers = [tier({ id: "tier-1", name: "fast", idx: 0 })];
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /edit/i }));
    await screen.findByTestId("tier-member-row-0");
    fireEvent.click(
      screen.getByRole("button", { name: /move member freebuff a down/i }),
    );
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));
    await waitFor(() => {
      expect(mockUpdateMember).toHaveBeenCalled();
    });
    const positionWrites = mockUpdateMember.mock.calls.map(
      (call) => call[0] as { memberId: string; position?: number },
    );
    expect(positionWrites).toContainEqual(
      expect.objectContaining({ memberId: "member-2", position: 0 }),
    );
    expect(positionWrites).toContainEqual(
      expect.objectContaining({ memberId: "member-1", position: 1 }),
    );
  });

  it("removes a deleted member on save", async () => {
    mockUpdate.mockResolvedValue({ id: "tier-1" });
    data.members = {
      "tier-1": [
        member({ id: "member-1", position: 0, provider: "freebuff", model: "a" }),
        member({ id: "member-2", position: 1, provider: "codebuddy", model: "b" }),
      ],
    };
    data.tiers = [tier({ id: "tier-1", name: "fast", idx: 0 })];
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /edit/i }));
    await screen.findByTestId("tier-member-row-1");
    fireEvent.click(
      screen.getAllByRole("button", { name: /remove member/i })[1]!,
    );
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));
    await waitFor(() => {
      expect(mockDeleteMember).toHaveBeenCalledWith({
        tierId: "tier-1",
        memberId: "member-2",
      });
    });
  });

  it("sends an explicit null only when clearing quality", async () => {
    mockUpdate.mockResolvedValue({ id: "tier-1" });
    data.members = { "tier-1": [member({ id: "member-1" })] };
    data.tiers = [tier({ id: "tier-1", name: "fast", idx: 0, quality: 68 })];
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /edit/i }));
    let editor = await screen.findByTestId("tier-editor");
    const qualityInput = editor.querySelector(
      "#tier-quality",
    ) as HTMLInputElement;
    expect(qualityInput.value).toBe("68");
    // Blank-without-checkbox omits (keeps the stored value).
    fireEvent.change(qualityInput, { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));
    await waitFor(() => {
      expect(mockUpdate).toHaveBeenCalled();
    });
    expect(mockUpdate.mock.calls[0]![0]).not.toHaveProperty("quality");
    // The save closed the dialog: reopen, then checked clear sends null.
    fireEvent.click(screen.getByRole("button", { name: /edit/i }));
    editor = await screen.findByTestId("tier-editor");
    fireEvent.click(screen.getByLabelText(/clear to unknown/i));
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));
    await waitFor(() => {
      expect(mockUpdate).toHaveBeenCalledTimes(2);
    });
    expect(mockUpdate.mock.calls[1]![0]).toHaveProperty("quality", null);
  });

  it("gives each member field its own column and keeps Slots numeric", async () => {
    data.members = { "tier-1": [member({ id: "member-1" })] };
    data.tiers = [tier({ id: "tier-1", name: "fast", idx: 0 })];
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /edit/i }));
    const row = await screen.findByTestId("tier-member-row-0");
    // M-496: the row sizes from its own width, not the viewport's, and every
    // cell can shrink below its content so a long runtime label cannot spill
    // over the next column.
    expect(row.className).toContain("@container");
    const grid = row.firstElementChild as HTMLElement;
    expect(grid.className).toContain("@[46rem]:grid-cols-");
    for (const label of ["Harness", "Model", "Thinking", "Runtime", "Slots"]) {
      const cell = screen.getByLabelText(label).parentElement;
      expect(cell?.className).toContain("min-w-0");
    }
    // The runtime select fills its column instead of sizing to its label.
    const runtime = screen.getByLabelText("Runtime");
    expect(runtime.className).toContain("w-full");
    expect(runtime.className).toContain("min-w-0");
    const slots = screen.getByLabelText("Slots") as HTMLInputElement;
    expect(slots.inputMode).toBe("numeric");
    expect(slots.value).toBe("1");
  });

  it("rejects a non-slug name before submitting", async () => {
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /add tier/i }));
    const editor = await screen.findByTestId("tier-editor");
    fireEvent.change(
      editor.querySelector("#tier-name") as HTMLInputElement,
      { target: { value: "Hard Tier" } },
    );
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));
    expect(await screen.findByText(/lowercase letters/i)).toBeInTheDocument();
    expect(mockCreate).not.toHaveBeenCalled();
  });

  it("makes the member harness a selector that keeps an unknown stored value", async () => {
    data.members = {
      "tier-1": [member({ id: "member-1", provider: "acme-harness" })],
    };
    data.tiers = [tier({ id: "tier-1", name: "fast", idx: 0 })];
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /edit/i }));
    await screen.findByTestId("tier-member-row-0");
    const harness = screen.getByLabelText("Harness") as HTMLSelectElement;
    // M-545: no typing — the harness is pickable from the known set.
    expect(harness.tagName).toBe("SELECT");
    const values = [...harness.options].map((option) => option.value);
    expect(values).toContain("freebuff");
    expect(values).toContain("codebuddy");
    // A saved harness outside the list still loads and stays selectable.
    expect(harness.value).toBe("acme-harness");
    expect(values).toContain("acme-harness");
  });

  it("offers workspace runtime providers as harness options", async () => {
    data.runtimes = [
      { id: "rt-1", provider: "qwen-local" },
    ];
    data.members = { "tier-1": [member({ id: "member-1" })] };
    data.tiers = [tier({ id: "tier-1", name: "fast", idx: 0 })];
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /edit/i }));
    await screen.findByTestId("tier-member-row-0");
    const harness = screen.getByLabelText("Harness") as HTMLSelectElement;
    expect([...harness.options].map((option) => option.value)).toContain(
      "qwen-local",
    );
  });

  it("turns the model into a picker when the harness maps to one runtime", async () => {
    data.runtimes = [
      { id: "rt-1", provider: "freebuff", name: "Freebuff", status: "online" },
    ];
    data.models = [{ id: "deepseek-v4.1-flash", label: "DeepSeek flash" }];
    data.members = { "tier-1": [member({ id: "member-1", provider: "freebuff" })] };
    data.tiers = [tier({ id: "tier-1", name: "fast", idx: 0 })];
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /edit/i }));
    await screen.findByTestId("tier-member-row-0");
    // The catalog is reachable through the harness alone, so Model is a
    // selector rather than a free-text field.
    expect(screen.getByLabelText("Model").tagName).not.toBe("INPUT");
  });

  it("rejects a member line without a harness or model", async () => {
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /add tier/i }));
    const editor = await screen.findByTestId("tier-editor");
    fireEvent.change(
      editor.querySelector("#tier-name") as HTMLInputElement,
      { target: { value: "flash" } },
    );
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));
    expect(
      await screen.findByText(/Each member needs a harness and a model/i),
    ).toBeInTheDocument();
    expect(mockCreate).not.toHaveBeenCalled();
  });

  it("opens one tab per registry role and lists only that role's tiers", () => {
    // The same tier in two roles shows in both tabs, with each role's order.
    data.tiersByRole = {
      architect: [
        tier({ id: "tier-both", name: "both", roles: ["architect", "management"] }),
        tier({ id: "tier-a", name: "arch-only", roles: ["architect"] }),
      ],
      management: [
        tier({ id: "tier-both", name: "both", roles: ["architect", "management"] }),
        tier({ id: "tier-m", name: "mgmt-only", roles: ["management"] }),
      ],
    };
    render(<TiersTab />, { wrapper: Wrapper });
    for (const label of ["Architect", "Management", "Engineer"]) {
      expect(screen.getByRole("tab", { name: label })).toBeInTheDocument();
    }
    expect(screen.getByTestId("tier-row-tier-a")).toBeInTheDocument();
    expect(screen.queryByTestId("tier-row-tier-m")).toBeNull();

    fireEvent.click(screen.getByRole("tab", { name: "Management" }));
    expect(screen.getByTestId("tier-row-tier-m")).toBeInTheDocument();
    expect(screen.getByTestId("tier-row-tier-both")).toBeInTheDocument();
    expect(screen.queryByTestId("tier-row-tier-a")).toBeNull();
  });

  it("reorders inside the active tab and sends that role", async () => {
    mockReorder.mockResolvedValue([]);
    data.tiersByRole = {
      architect: [tier({ id: "tier-a", name: "arch-only" })],
      management: [
        tier({ id: "tier-m1", name: "mgmt-one" }),
        tier({ id: "tier-m2", name: "mgmt-two" }),
      ],
    };
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("tab", { name: "Management" }));
    fireEvent.click(
      screen.getByRole("button", { name: /move mgmt-one down/i }),
    );
    await waitFor(() => {
      expect(mockReorder).toHaveBeenCalledWith({
        ids: ["tier-m2", "tier-m1"],
        role: "management",
      });
    });
  });

  it("preselects the active tab's role when adding a tier", async () => {
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("tab", { name: "Engineer" }));
    fireEvent.click(screen.getByRole("button", { name: /add tier/i }));
    await screen.findByTestId("tier-editor");
    expect(screen.getByTestId("tier-role-tag-engineer")).toHaveTextContent(
      "Engineer",
    );
    expect(screen.queryByTestId("tier-role-tag-architect")).toBeNull();
  });

  it("grays the form out and disables everything but Enabled, Cancel and Save", async () => {
    mockUpdate.mockResolvedValue({ id: "tier-1" });
    data.members = { "tier-1": [member({ id: "member-1" })] };
    data.tiers = [tier({ id: "tier-1", name: "fast" })];
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /edit/i }));
    const fields = (await screen.findByTestId(
      "tier-editor-fields",
    )) as HTMLFieldSetElement;
    expect(fields.disabled).toBe(false);

    fireEvent.click(screen.getByTestId("tier-enabled-switch"));
    expect(fields.disabled).toBe(true);
    expect(fields.className).toContain("opacity-60");
    expect(
      (screen.getByLabelText("Name") as HTMLInputElement).disabled,
    ).toBe(true);
    expect(screen.getByTestId("tier-enabled-switch")).not.toBeDisabled();
    expect(screen.getByRole("button", { name: /cancel/i })).toBeEnabled();
    expect(screen.getByRole("button", { name: /^save$/i })).toBeEnabled();

    // Saving the off state still works: only the tier's own fields are off.
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));
    await waitFor(() => {
      expect(mockUpdate).toHaveBeenCalled();
    });
    expect(mockUpdate.mock.calls[0]![0]).toMatchObject({ enabled: false });
  });
});
