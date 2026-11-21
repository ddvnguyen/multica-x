"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { modelTiersOptions } from "@multica/core/workspace/queries";
import type { AgentPreset } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../../i18n";

const presetQueryKey = (wsId: string) => ["agent-presets", wsId] as const;

/** Workspace presets bound to the inspected agent. */
export function AgentPresetEditor({ agentId, canEdit }: { agentId: string; canEdit: boolean }) {
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const { t } = useT("agents");
  const [editing, setEditing] = useState<AgentPreset | null>(null);
  const [name, setName] = useState("");
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
  const presets = (presetsQuery.data ?? []).filter((preset) => preset.agent_id === agentId);
  const tiers = (tiersQuery.data ?? []).filter((tier) => tier.enabled);
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
    setTierId(preset.tier_id);
    saveMutation.reset();
  };
  const cancelEdit = () => {
    setEditing(null);
    setName("");
    setTierId("");
    saveMutation.reset();
  };

  if (workspaceQuery.isPending || (!workspaceQuery.isError && !workspaceQuery.data?.settings.agent_presets_enabled)) return null;

  return (
    <section className="space-y-2" aria-label={t(($) => $.inspector.presets_title)}>
      <h3 className="text-label font-medium">{t(($) => $.inspector.presets_title)}</h3>
      {presetsQuery.isPending ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.inspector.presets_loading)}</p>
      ) : presets.length > 0 ? (
        <ul className="space-y-1">
          {presets.map((preset) => (
            <li key={preset.id} className="flex min-w-0 items-center gap-2 rounded-md border px-2 py-1.5">
              <span className="min-w-0 flex-1 truncate text-caption" title={`${preset.agent_name} · ${preset.tier_name}`}>
                {preset.name} <span className="text-muted-foreground">· {preset.tier_name || t(($) => $.inspector.presets_unavailable)}</span>
              </span>
              {canEdit && <Button type="button" size="xs" variant="ghost" onClick={() => startEdit(preset)}>{t(($) => $.inspector.presets_edit)}</Button>}
              {canEdit && <Button type="button" size="xs" variant="ghost" onClick={() => deleteMutation.mutate(preset.id)} disabled={deleteMutation.isPending}>{t(($) => $.inspector.presets_delete)}</Button>}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-caption text-muted-foreground">{t(($) => $.inspector.presets_empty)}</p>
      )}
      {canEdit && (
        <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto_auto]">
          <Input aria-label={t(($) => $.inspector.presets_name)} placeholder={t(($) => $.inspector.presets_name)} value={name} onChange={(event) => setName(event.target.value)} maxLength={80} />
          <select aria-label={t(($) => $.inspector.presets_tier)} className="h-9 min-w-0 rounded-md border bg-background px-2 text-caption" value={tierId} onChange={(event) => setTierId(event.target.value)}>
            <option value="">{t(($) => $.inspector.presets_tier)}</option>
            {tiers.map((tier) => <option key={tier.id} value={tier.id}>{tier.name}</option>)}
          </select>
          <Button type="button" size="sm" disabled={saveMutation.isPending || !name.trim() || !tierId || !tiersQuery.isSuccess} onClick={() => saveMutation.mutate()}>
            {editing ? t(($) => $.inspector.presets_save) : t(($) => $.inspector.presets_add)}
          </Button>
          {editing && <Button type="button" size="sm" variant="outline" onClick={cancelEdit}>{t(($) => $.inspector.cancel)}</Button>}
        </div>
      )}
      {(workspaceQuery.isError || saveMutation.isError || deleteMutation.isError || presetsQuery.isError || tiersQuery.isError) && (
        <p role="alert" className="text-caption text-destructive">{t(($) => $.inspector.presets_error)}</p>
      )}
    </section>
  );
}
