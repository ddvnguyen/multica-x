"use client";

import { use, useMemo } from "react";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import { AgentDetailPage } from "@multica/views/agents/agent-detail-page";
import { createAgentMemorySections } from "@multica/views/moct";

export default function AgentDetailRoute({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  const workspace = useCurrentWorkspace();
  const { role } = useCurrentMember(workspace?.id ?? "");
  const canManageMemory = role === "owner" || role === "admin";
  const extraSections = useMemo(() => workspace ? createAgentMemorySections({
    apiBasePath: "",
    canManageMemory,
    workspaceSlug: workspace.slug,
    workspaceId: workspace.id,
    wsPath: "",
    workspaceFolder: "",
    problem: null,
  }) : [], [workspace, canManageMemory]);
  return <AgentDetailPage agentId={id} extraSections={extraSections} />;
}
