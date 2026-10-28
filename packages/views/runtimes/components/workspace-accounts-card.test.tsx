// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AgentRuntime, RuntimeProfile, WorkspaceAccount } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { WorkspaceAccountsCard } from "./workspace-accounts-card";

const api = vi.hoisted(() => ({
  listWorkspaceAccounts: vi.fn(),
  createWorkspaceAccount: vi.fn(),
  updateWorkspaceAccount: vi.fn(),
  deleteWorkspaceAccount: vi.fn(),
}));
const profileCommand = vi.hoisted(() => ({ name: "freebuff-desktop-bridge" }));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/api", () => ({ api, ApiError: class ApiError extends Error {} }));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));
vi.mock("@multica/core/runtimes", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/runtimes")>(
    "@multica/core/runtimes",
  );
  return {
    ...actual,
    runtimeProfileListOptions: () => ({
      queryKey: ["runtime-profiles", "ws-1", "list"],
      queryFn: async () =>
        [{ id: "profile-1", command_name: profileCommand.name }] as RuntimeProfile[],
    }),
  };
});

const runtime = { id: "rt-1", provider: "codebuddy", profile_id: "profile-1" } as AgentRuntime;

const account = (over: Partial<WorkspaceAccount> = {}): WorkspaceAccount => ({
  id: "acc-1",
  workspace_id: "ws-1",
  provider: "freebuff-desktop",
  label: "Team A",
  endpoint: "http://a.test:4320",
  enabled: true,
  credential_ref: "ref-a",
  created_at: "",
  updated_at: "",
  ...over,
});

function renderCard(canManage: boolean) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <WorkspaceAccountsCard runtime={runtime} canManage={canManage} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  profileCommand.name = "freebuff-desktop-bridge";
  api.listWorkspaceAccounts.mockResolvedValue([account()]);
  api.createWorkspaceAccount.mockResolvedValue(account({ id: "acc-2", label: "Team B" }));
  api.updateWorkspaceAccount.mockResolvedValue(account({ enabled: false }));
  api.deleteWorkspaceAccount.mockResolvedValue(undefined);
});

describe("WorkspaceAccountsCard", () => {
  it("renders nothing for a runtime that is not freebuff-desktop", async () => {
    profileCommand.name = "codebuddy-freebuff";
    renderCard(true);
    await waitFor(() => expect(api.listWorkspaceAccounts).not.toHaveBeenCalled());
    expect(screen.queryByText("Accounts")).toBeNull();
  });

  it("lists the accounts, read-only for non-admins", async () => {
    renderCard(false);
    expect(await screen.findByText("Team A")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Add account" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete Team A" })).toBeNull();
  });

  it("lets an admin add an account", async () => {
    const user = userEvent.setup();
    renderCard(true);
    await screen.findByText("Team A");
    await user.type(screen.getByLabelText("Label"), "Team B");
    await user.type(screen.getByLabelText("Credential reference"), "ref-b");
    await user.click(screen.getByRole("button", { name: "Add account" }));
    await waitFor(() =>
      expect(api.createWorkspaceAccount).toHaveBeenCalledWith("ws-1", {
        label: "Team B",
        endpoint: undefined,
        credential_ref: "ref-b",
      }),
    );
  });

  it("lets an admin delete an account", async () => {
    const user = userEvent.setup();
    renderCard(true);
    await user.click(await screen.findByRole("button", { name: "Delete Team A" }));
    await waitFor(() => expect(api.deleteWorkspaceAccount).toHaveBeenCalledWith("ws-1", "acc-1"));
  });
});
