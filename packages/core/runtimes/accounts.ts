import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type {
  AgentAccountMode,
  CreateWorkspaceAccountRequest,
  UpdateWorkspaceAccountRequest,
  WorkspaceAccount,
} from "../types/agent";
import { workspaceKeys } from "../workspace/queries";
import { runtimeModelsKeys } from "./models";

// Per-workspace freebuff-desktop accounts (M-153).
//
// An agent on a freebuff-desktop runtime picks one account, any enabled
// account, or none. The same choice scopes which models the picker lists, so
// the helpers below are the single place that maps between the three views of
// it: the agent's stored (mode, id), the single <select> value the pickers
// bind to, and the `account` query parameter of the model catalog request.

export const workspaceAccountKeys = {
  all: (wsId: string) => ["workspace-accounts", wsId] as const,
  list: (wsId: string) => [...workspaceAccountKeys.all(wsId), "list"] as const,
};

export function workspaceAccountListOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceAccountKeys.list(wsId),
    queryFn: () => api.listWorkspaceAccounts(wsId),
  });
}

function useInvalidateAccounts(wsId: string) {
  const qc = useQueryClient();
  return () => {
    qc.invalidateQueries({ queryKey: workspaceAccountKeys.all(wsId) });
    // Model catalogs are cached per account scope; an account that changed or
    // vanished makes them stale.
    qc.invalidateQueries({ queryKey: runtimeModelsKeys.all() });
  };
}

export function useCreateWorkspaceAccount(wsId: string) {
  const invalidate = useInvalidateAccounts(wsId);
  return useMutation({
    mutationFn: (body: CreateWorkspaceAccountRequest) =>
      api.createWorkspaceAccount(wsId, body),
    onSettled: invalidate,
  });
}

export function useUpdateWorkspaceAccount(wsId: string) {
  const invalidate = useInvalidateAccounts(wsId);
  return useMutation({
    mutationFn: ({
      accountId,
      patch,
    }: {
      accountId: string;
      patch: UpdateWorkspaceAccountRequest;
    }) => api.updateWorkspaceAccount(wsId, accountId, patch),
    onSettled: invalidate,
  });
}

export function useDeleteWorkspaceAccount(wsId: string) {
  const qc = useQueryClient();
  const invalidate = useInvalidateAccounts(wsId);
  return useMutation({
    mutationFn: (accountId: string) => api.deleteWorkspaceAccount(wsId, accountId),
    onSettled: () => {
      invalidate();
      // The server demotes agents bound to the deleted account.
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
    },
  });
}

/** The provider a workspace account is registered under. */
export const ACCOUNT_PROVIDER = "freebuff-desktop";

/** Runtime provider (protocol family) that can run accounts. */
export const ACCOUNT_RUNTIME_PROVIDER = "codebuddy";

/** Select value meaning "no account — use the runtime's own configuration". */
export const ACCOUNT_VALUE_DEFAULT = "default";
/** Select value meaning "any enabled account". */
export const ACCOUNT_VALUE_ANY = "any";

export interface AgentAccountSelection {
  mode: AgentAccountMode;
  accountId: string;
}

/** Whether accounts apply to a runtime of this provider. */
export function runtimeSupportsAccounts(provider: string | undefined | null): boolean {
  return provider === ACCOUNT_RUNTIME_PROVIDER;
}

/** Accounts an agent can be pointed at: enabled, freebuff-desktop only. */
export function selectableAccounts(accounts: WorkspaceAccount[] | undefined): WorkspaceAccount[] {
  return (accounts ?? []).filter((a) => a.enabled && a.provider === ACCOUNT_PROVIDER);
}

/** Normalise the (mode, id) an agent record carries. */
export function agentAccountSelection(agent: {
  account_mode?: string;
  account_id?: string;
}): AgentAccountSelection {
  if (agent.account_mode === "specific" && agent.account_id) {
    return { mode: "specific", accountId: agent.account_id };
  }
  if (agent.account_mode === "any") return { mode: "any", accountId: "" };
  return { mode: "default", accountId: "" };
}

/** The value a single <select> binds to for a selection. */
export function accountSelectionValue(sel: AgentAccountSelection): string {
  if (sel.mode === "specific") return sel.accountId;
  if (sel.mode === "any") return ACCOUNT_VALUE_ANY;
  return ACCOUNT_VALUE_DEFAULT;
}

/** Inverse of accountSelectionValue. */
export function parseAccountSelectionValue(value: string): AgentAccountSelection {
  if (value === ACCOUNT_VALUE_ANY) return { mode: "any", accountId: "" };
  if (!value || value === ACCOUNT_VALUE_DEFAULT) return { mode: "default", accountId: "" };
  return { mode: "specific", accountId: value };
}

/**
 * The `account` parameter for the model catalog request. Undefined keeps the
 * runtime's default catalog; `all` unions every enabled account's models.
 */
export function accountModelScope(sel: AgentAccountSelection): string | undefined {
  if (sel.mode === "specific") return sel.accountId;
  if (sel.mode === "any") return "all";
  return undefined;
}

/** The request fields that persist a selection on an agent. */
export function accountRequestFields(sel: AgentAccountSelection): {
  account_mode: AgentAccountMode;
  account_id: string;
} {
  return { account_mode: sel.mode, account_id: sel.mode === "specific" ? sel.accountId : "" };
}
