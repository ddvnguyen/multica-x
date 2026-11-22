"use client";

import { useQuery, queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { Agent, RoleCompositionPreview } from "../types";
import { workspaceKeys } from "../workspace/queries";

/**
 * The additive agent|role discriminator (M-501). Empty/undefined reads as
 * the legacy `agent` subtype — a Role is only ever the explicit "role".
 */
export function isRoleAgent(
  agent: Pick<Agent, "subtype"> | null | undefined,
): boolean {
  return agent?.subtype === "role";
}

/**
 * Whether the agent_role_type workspace flag is on for this workspace
 * (M-499 slice 4, fixed by M-517 Architect verdict). Reads the
 * workspace-scoped boolean the tier-roles endpoint evaluates with the
 * workspace attached — an `allow_by: workspace_id` rule enables lane z
 * while every other workspace reads false. The roles list itself stays
 * member-visible; only this boolean gates the Role surfaces. Fails closed:
 * empty workspace or fetch error reads as off. There is deliberately no
 * published-flag OR: the published flag has no workspace context and would
 * diverge from the per-workspace rule.
 *
 * The probe result is cached per workspace (5min) so one check covers the
 * Team menu, create/edit forms, and all pickers.
 */
export function agentRoleTypeProbeOptions(wsId: string) {
  return queryOptions({
    queryKey: [...workspaceKeys.tierRoles(wsId), "probe"],
    queryFn: async () => {
      try {
        const res = await api.listTierRoles(wsId);
        return res.agentRoleType === true;
      } catch {
        return false;
      }
    },
    enabled: wsId !== "",
    staleTime: 5 * 60 * 1000,
    retry: false,
  });
}

export function useAgentRoleTypeEnabled(wsId?: string): boolean {
  const { data: enabled = false } = useQuery(agentRoleTypeProbeOptions(wsId ?? ""));
  if (!wsId) return false;
  return enabled;
}

/**
 * Read-only inherited view for an Agent implementing a Role
 * (`GET /api/agents/{id}/role-preview`). Enabled only when the flag is on
 * and the agent links a Role; a 403 (caller lost Role access) surfaces as
 * an error the caller renders as hidden.
 */
export function rolePreviewOptions(agentId: string, enabled = true) {
  return queryOptions({
    queryKey: ["agents", agentId, "role-preview"] as const,
    queryFn: (): Promise<RoleCompositionPreview> =>
      api.previewRoleComposition(agentId),
    enabled: !!agentId && enabled,
    retry: false,
  });
}

export function useRolePreview(
  agentId: string,
  enabled = true,
) {
  return useQuery(rolePreviewOptions(agentId, enabled));
}
