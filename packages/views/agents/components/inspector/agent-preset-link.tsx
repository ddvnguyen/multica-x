"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { AppLink } from "../../../navigation";
import { useT } from "../../../i18n";

/**
 * Read-only preset pointer in the agent inspector (M-490). Preset management
 * lives in Settings → Tiers; the inspector keeps the tier multi-select and
 * only names the presets bound to this agent, with a link to manage them.
 */
export function AgentPresetLink({ agentId }: { agentId: string }) {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const { t } = useT("settings");
  const workspaceQuery = useQuery({
    queryKey: ["workspaces", wsId],
    queryFn: () => api.getWorkspace(wsId),
    enabled: wsId !== "",
  });
  const presetsQuery = useQuery({
    queryKey: ["agent-presets", wsId],
    queryFn: () => api.listAgentPresets(wsId),
    enabled: wsId !== "",
  });

  if (
    workspaceQuery.isPending ||
    (!workspaceQuery.isError &&
      !workspaceQuery.data?.settings.agent_presets_enabled)
  )
    return null;

  const presets = (presetsQuery.data ?? []).filter(
    (preset) => preset.agent_id === agentId,
  );

  return (
    <section className="space-y-2" aria-label={t(($) => $.tiers.presets_title)}>
      <h3 className="text-label font-medium">
        {t(($) => $.tiers.presets_title)}
      </h3>
      {presetsQuery.isPending ? (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.tiers.presets_loading)}
        </p>
      ) : presets.length > 0 ? (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.tiers.presets_using_label)}:{" "}
          {presets.map((preset) => preset.name).join(", ")}
        </p>
      ) : (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.tiers.presets_empty)}
        </p>
      )}
      <AppLink
        href={`${paths.settings()}?tab=tiers`}
        className="inline-flex text-caption font-medium text-primary hover:underline"
      >
        {t(($) => $.tiers.presets_manage_link)}
      </AppLink>
      {presetsQuery.isError && (
        <p role="alert" className="text-caption text-destructive">
          {t(($) => $.tiers.presets_error)}
        </p>
      )}
    </section>
  );
}
