"use client";

import { useMemo } from "react";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import { SettingsPage } from "@multica/views/settings";
import { createMoctGroup } from "@multica/views/moct";

export default function Page() {
  const workspace = useCurrentWorkspace();
  const { role } = useCurrentMember(workspace?.id ?? "");
  const canManageMemory = role === "owner" || role === "admin";
  const extraGroups = useMemo(() => workspace ? [createMoctGroup({
    apiBasePath: "",
    canManageMemory,
    workspaceSlug: workspace.slug,
    workspaceId: workspace.id,
    wsPath: "",
    workspaceFolder: "",
    problem: null,
  })] : [], [workspace, canManageMemory]);
  return <SettingsPage extraGroups={extraGroups} />;
}
