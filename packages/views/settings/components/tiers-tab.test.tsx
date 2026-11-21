// @vitest-environment jsdom

import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enSettings from "../../locales/en/settings.json";

const mockCreate = vi.hoisted(() => vi.fn());
const mockUpdate = vi.hoisted(() => vi.fn());
const mockDelete = vi.hoisted(() => vi.fn());
const mockReorder = vi.hoisted(() => vi.fn());
const mockCreateMember = vi.hoisted(() => vi.fn());
const mockUpdateMember = vi.hoisted(() => vi.fn());
const mockDeleteMember = vi.hoisted(() => vi.fn());

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
  members: {} as Record<string, Array<Record<string, unknown>>>,
  isLoading: false,
  role: "owner" as "owner" | "admin" | "member",
}));

/**
 * One mock for every query in the tab and the dialog: the tier list, the
 * per-tier member queries, the runtime list and per-runtime model discovery.
 * The key shape decides which fixture answers.
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
  if (key.includes("tiers")) {
    return { data: data.tiers, isLoading: data.isLoading, isSuccess: true };
  }
  if (key.includes("list")) {
    return { data: [], isLoading: false, isSuccess: true };
  }
  return {
    data: { models: [], supported: true },
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
  modelTiersOptions: () => ({ queryKey: ["workspaces", "workspace-1", "tiers"] }),
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

  it("shows the empty state when no tiers exist", () => {
    data.tiers = [];
    render(<TiersTab />, { wrapper: Wrapper });
    expect(screen.getByText("No tiers yet")).toBeInTheDocument();
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

  it("moves a tier down through the reorder mutation", async () => {
    mockReorder.mockResolvedValue([]);
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /move fast down/i }));
    await waitFor(() => {
      expect(mockReorder).toHaveBeenCalledWith(["tier-2", "tier-1"]);
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

  it("offers exactly the three M-481 roles as toggles", async () => {
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /add tier/i }));
    await screen.findByTestId("tier-editor");
    expect(screen.getByRole("checkbox", { name: "Architect" })).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "Management" })).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "Engineer" })).toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: "Leader" })).toBeNull();
    expect(screen.queryByRole("checkbox", { name: "Reviewer" })).toBeNull();
  });

  it("offers the seven quota kinds as checkboxes", async () => {
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /add tier/i }));
    await screen.findByTestId("tier-editor");
    for (const label of [
      "Free daily",
      "Free",
      "Local",
      "Quota 5 hours",
      "Quota daily",
      "Quota weekly",
      "Quota monthly",
    ]) {
      expect(screen.getByRole("checkbox", { name: label })).toBeInTheDocument();
    }
    expect(screen.getAllByRole("checkbox")).toHaveLength(10); // 3 roles + 7 quotas
  });

  it("marks context and compaction advisory and shows the derived token count", async () => {
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /add tier/i }));
    await screen.findByTestId("tier-editor");
    expect(screen.getByTestId("tier-context-advisory")).toHaveTextContent(
      "advisory",
    );
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
    fireEvent.click(screen.getByRole("checkbox", { name: "Engineer" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Quota weekly" }));
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));
    await waitFor(() => {
      expect(mockCreate).toHaveBeenCalledTimes(1);
    });
    expect(mockCreate.mock.calls[0]![0]).toMatchObject({
      name: "flash",
      provider: "freebuff",
      model: "deepseek-v4.1-flash",
      roles: ["engineer"],
      auto_pick: true,
      quota_kinds: ["quota_weekly"],
      ctx_max_tokens: 280000,
      compact_at_pct: 75,
    });
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
});
