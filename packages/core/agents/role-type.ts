"use client";

import { useQuery, queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import { useFeatureEnabled } from "../config";
import { AGENT_ROLE_TYPE_FLAG } from "../feature-flags/keys";
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
 * (M-499 slice 4). Reads the published frontend flag when the server
 * publishes it; falls back to the member-visible tier-roles registry probe
 * (`GET /tiers/roles` is NOT flag-gated — ListTierRoles only requires
 * workspace membership). Fails closed: empty workspace, fetch error, or
 * unpublished flag all read as off.
 *
 * The probe result is cached per workspace (5min) so one check covers the
 * Team menu, create/edit forms, and all pickers.
 */
export function agentRoleTypeProbeOptions(wsId: string) {
  return queryOptions({
    queryKey: [...workspaceKeys.tierRoles(wsId), "probe"],
    queryFn: async () => {
      try {
        await api.listTierRoles(wsId);
        return true;
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
  const published = useFeatureEnabled(AGENT_ROLE_TYPE_FLAG, false);
  const { data: probed = false } = useQuery({
    ...agentRoleTypeProbeOptions(wsId ?? ""),
    enabled: !published && !!wsId,
  });
  if (!wsId) return false;
  return published || probed;
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
