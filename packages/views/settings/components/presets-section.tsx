"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import {
  agentListOptions,
  modelTiersOptions,
} from "@multica/core/workspace/queries";
import type { AgentPreset } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n";
import { SettingsCard, SettingsSection } from "./settings-layout";

const presetQueryKey = (wsId: string) => ["agent-presets", wsId] as const;

/**
 * Workspace-level preset management (M-490). A preset is an agent+tier
 * quick-pick pair, so it lives with tier config in Settings → Tiers instead
 * of the agent inspector. Admin-only edit, same `api.*AgentPreset` calls the
 * inspector editor used.
 */
export function PresetsSection({
  wsId,
  canManage,
}: {
  wsId: string;
  canManage: boolean;
}) {
  const { t } = useT("settings");
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState<AgentPreset | null>(null);
  const [name, setName] = useState("");
  const [agentId, setAgentId] = useState("");
  const [tierId, setTierId] = useState("");

  const workspaceQuery = useQuery({
    queryKey: ["workspaces", wsId],
    queryFn: () => api.getWorkspace(wsId),
    enabled: wsId !== "",
  });
  const presetsQuery = useQuery({
    queryKey: presetQueryKey(wsId),
    queryFn: () => api.listAgentPresets(wsId),
    enabled: wsId !== "",
  });
  const tiersQuery = useQuery(modelTiersOptions(wsId));
  const agentsQuery = useQuery(agentListOptions(wsId));

  const presets = useMemo(() => presetsQuery.data ?? [], [presetsQuery.data]);
  const tiers = useMemo(
    () => (tiersQuery.data ?? []).filter((tier) => tier.enabled),
    [tiersQuery.data],
  );
  const agents = useMemo(() => {
    const list = agentsQuery.data ?? [];
    return [...list]
      .filter((agent) => !agent.archived_at)
      .sort((left, right) => left.name.localeCompare(right.name));
  }, [agentsQuery.data]);

  const saveMutation = useMutation({
    mutationFn: () => {
      const input = { name: name.trim(), agent_id: agentId, tier_id: tierId };
      return editing
        ? api.updateAgentPreset(wsId, editing.id, input)
        : api.createAgentPreset(wsId, input);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: presetQueryKey(wsId) });
      setEditing(null);
      setName("");
      setAgentId("");
      setTierId("");
    },
  });
  const deleteMutation = useMutation({
    mutationFn: (presetId: string) => api.deleteAgentPreset(wsId, presetId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: presetQueryKey(wsId) }),
  });

  const startEdit = (preset: AgentPreset) => {
    setEditing(preset);
    setName(preset.name);
    setAgentId(preset.agent_id);
    setTierId(preset.tier_id);
    saveMutation.reset();
  };
  const cancelEdit = () => {
    setEditing(null);
    setName("");
    setAgentId("");
    setTierId("");
    saveMutation.reset();
  };

  if (
    workspaceQuery.isPending ||
    (!workspaceQuery.isError &&
      !workspaceQuery.data?.settings.agent_presets_enabled)
  )
    return null;

  return (
    <SettingsSection title={t(($) => $.tiers.presets_title)}>
      <SettingsCard>
        {presetsQuery.isPending ? (
          <p className="px-4 py-6 text-body text-muted-foreground">
            {t(($) => $.tiers.presets_loading)}
          </p>
        ) : presets.length > 0 ? (
          <ul className="divide-y">
            {presets.map((preset) => (
              <li
                key={preset.id}
                data-testid={`preset-row-${preset.id}`}
                className="flex min-w-0 items-center gap-2 px-4 py-3"
              >
                <span
                  className="min-w-0 flex-1 truncate text-body"
                  title={`${preset.agent_name} · ${preset.tier_name}`}
                >
                  {preset.name}{" "}
                  <span className="text-muted-foreground">
                    · {preset.agent_name} ·{" "}
                    {preset.tier_name ||
                      t(($) => $.tiers.presets_unavailable)}
                  </span>
                </span>
                {canManage && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() => startEdit(preset)}
                  >
                    {t(($) => $.tiers.presets_edit)}
                  </Button>
                )}
                {canManage && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() => deleteMutation.mutate(preset.id)}
                    disabled={deleteMutation.isPending}
                  >
                    {t(($) => $.tiers.presets_delete)}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        ) : (
          <p className="px-4 py-6 text-body text-muted-foreground">
            {t(($) => $.tiers.presets_empty_all)}
          </p>
        )}
        {canManage && (
          <div className="grid gap-2 border-t px-4 py-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_auto_auto]">
            <Input
              aria-label={t(($) => $.tiers.presets_name)}
              placeholder={t(($) => $.tiers.presets_name)}
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={80}
            />
            <select
              aria-label={t(($) => $.tiers.presets_agent)}
              className="h-9 min-w-0 rounded-md border bg-background px-2 text-caption"
              value={agentId}
              onChange={(event) => setAgentId(event.target.value)}
            >
              <option value="">
                {t(($) => $.tiers.presets_agent)}
              </option>
              {agents.map((agent) => (
                <option key={agent.id} value={agent.id}>
                  {agent.name}
                </option>
              ))}
            </select>
            <select
              aria-label={t(($) => $.tiers.presets_tier)}
              className="h-9 min-w-0 rounded-md border bg-background px-2 text-caption"
              value={tierId}
              onChange={(event) => setTierId(event.target.value)}
            >
              <option value="">
                {t(($) => $.tiers.presets_tier)}
              </option>
              {tiers.map((tier) => (
                <option key={tier.id} value={tier.id}>
                  {tier.name}
                </option>
              ))}
            </select>
            <Button
              type="button"
              size="sm"
              disabled={
                saveMutation.isPending ||
                !name.trim() ||
                !agentId ||
                !tierId ||
                !tiersQuery.isSuccess ||
                !agentsQuery.isSuccess
              }
              onClick={() => saveMutation.mutate()}
            >
              {editing
                ? t(($) => $.tiers.presets_save)
                : t(($) => $.tiers.presets_add)}
            </Button>
            {editing && (
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={cancelEdit}
              >
                {t(($) => $.tiers.cancel)}
              </Button>
            )}
          </div>
        )}
        {(workspaceQuery.isError ||
          saveMutation.isError ||
          deleteMutation.isError ||
          presetsQuery.isError ||
          tiersQuery.isError ||
          agentsQuery.isError) && (
          <p role="alert" className="px-4 pb-4 text-caption text-destructive">
            {t(($) => $.tiers.presets_error)}
          </p>
        )}
      </SettingsCard>
    </SettingsSection>
  );
}
