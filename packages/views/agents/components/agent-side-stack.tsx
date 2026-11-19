"use client";

import { useCallback } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Agent, UpdateAgentRequest } from "@multica/core/types";
import { type AgentPresenceDetail, useWorkspacePresenceMap } from "@multica/core/agents";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  agentDetailOptions,
  agentListOptions,
  cacheAgentResponse,
  memberListOptions,
} from "@multica/core/workspace/queries";
import { runtimeListOptions } from "@multica/core/runtimes";
import { useAgentPermissions } from "@multica/core/permissions";
import { ActorAvatar } from "../../common/actor-avatar";
import { IssueSidebarStack } from "../../issues/components/issue-sidebar-section";
import { useNavigation } from "../../navigation";
import { AgentPropertiesPanel } from "./agent-properties-panel";

export function AgentSideStack({ agentId }: { agentId: string }) {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const currentUser = useAuthStore((s) => s.user);

  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { byAgent: presenceMap } = useWorkspacePresenceMap(wsId);

  const listAgent = agents.find((a) => a.id === agentId) ?? null;
  const {
    data: detailAgent,
    isLoading: detailLoading,
    error: detailError,
  } = useQuery({
    ...agentDetailOptions(wsId, agentId),
    enabled: wsId !== "" && !listAgent,
  });
  const agent: Agent | null = listAgent ?? detailAgent ?? null;
  const presence: AgentPresenceDetail | null = agent ? presenceMap.get(agent.id) ?? null : null;
  const runtime = agent ? (runtimes.find((r) => r.id === agent.runtime_id) ?? null) : null;
  const owner = agent
    ? members.find((m) => m.user_id === (agent.owner_id ?? agent.created_by ?? "")) ?? null
    : null;
  const { canEdit } = useAgentPermissions(agent, wsId);

  const handleUpdate = useCallback(
    async (id: string, data: Record<string, unknown>) => {
      if (!wsId) return;
      try {
        const updated = await api.updateAgent(id, data as UpdateAgentRequest);
        cacheAgentResponse(qc, wsId, updated);
      } catch (err) {
        toast.error((err as Error).message);
        throw err;
      }
    },
    [qc, wsId],
  );

  const openDetail = useCallback(
    (suffix = "") => {
      if (!navigation.openInNewTab) return;
      navigation.openInNewTab(paths.agentDetail(agentId) + suffix, agentId, { activate: true });
    },
    [agentId, navigation, paths],
  );

  if (detailError || (!agent && !detailLoading)) return null;
  if (!agent) return null;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div
        className="flex items-baseline gap-2 border-b border-border/60 px-3 py-2"
        data-multica-side-preview-header=""
        title={agent.name}
      >
        <span className="inline-flex min-w-0 items-center gap-1.5">
          <ActorAvatar actorType="agent" actorId={agent.id} size="xs" profileLink={false} />
          <span className="truncate text-caption font-medium text-foreground">{agent.name}</span>
        </span>
      </div>
      <IssueSidebarStack>
        <AgentPropertiesPanel
          agent={agent}
          runtime={runtime}
          runtimes={runtimes}
          presence={presence}
          owner={owner}
          members={members}
          currentUserId={currentUser?.id ?? null}
          canEdit={canEdit.allowed}
          onUpdate={handleUpdate}
          onEditAccess={() => openDetail("?section=access")}
          onOpenSettings={() => openDetail("?section=general")}
        />
      </IssueSidebarStack>
    </div>
  );
}
