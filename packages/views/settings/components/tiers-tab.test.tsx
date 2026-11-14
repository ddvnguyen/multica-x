// @vitest-environment jsdom

import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enSettings from "../../locales/en/settings.json";

const mockCreate = vi.hoisted(() => vi.fn());
const mockUpdate = vi.hoisted(() => vi.fn());
const mockDelete = vi.hoisted(() => vi.fn());
const mockReorder = vi.hoisted(() => vi.fn());

const tier = (over: Record<string, unknown>) => ({
  id: "tier-1",
  workspace_id: "workspace-1",
  name: "fast",
  idx: 0,
  provider: "codebuddy",
  model: "m1",
  provider_slots: 1,
  enabled: true,
  created_at: "2026-08-14T00:00:00Z",
  updated_at: "2026-08-14T00:00:00Z",
  ...over,
});

const data = vi.hoisted(() => ({
  tiers: [] as Array<Record<string, unknown>>,
  isLoading: false,
  role: "owner" as "owner" | "admin" | "member",
}));

vi.mock("@tanstack/react-query", () => ({
  queryOptions: <T,>(options: T) => options,
  useQuery: () => ({ data: data.tiers, isLoading: data.isLoading }),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  modelTiersOptions: () => ({ queryKey: ["workspaces", "workspace-1", "tiers"] }),
  memberListOptions: () => ({ queryKey: ["workspaces", "workspace-1", "members"] }),
}));

vi.mock("@multica/core/workspace/mutations", () => ({
  useCreateModelTier: () => ({ mutateAsync: mockCreate, isPending: false }),
  useUpdateModelTier: () => ({ mutateAsync: mockUpdate, isPending: false }),
  useDeleteModelTier: () => ({ mutate: mockDelete, isPending: false }),
  useReorderModelTiers: () => ({ mutateAsync: mockReorder, isPending: false }),
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
  });

  it("lists tiers ordered with the disabled badge", () => {
    render(<TiersTab />, { wrapper: Wrapper });
    expect(screen.getByTestId("tier-row-tier-1")).toBeInTheDocument();
    expect(screen.getByTestId("tier-row-tier-2")).toBeInTheDocument();
    expect(screen.getByText("Disabled")).toBeInTheDocument();
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

  it("opens the editor on add", async () => {
    render(<TiersTab />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: /add tier/i }));
    expect(await screen.findByTestId("tier-editor")).toBeInTheDocument();
  });
});
