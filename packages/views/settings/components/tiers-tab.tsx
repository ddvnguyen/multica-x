"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Switch } from "@multica/ui/components/ui/switch";
import { Loader2 } from "@multica/ui/icons";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import { modelTiersOptions } from "@multica/core/workspace/queries";
import {
  useCreateModelTier,
  useDeleteModelTier,
  useReorderModelTiers,
  useUpdateModelTier,
} from "@multica/core/workspace/mutations";
import {
  runtimeListOptions,
  runtimeModelsOptions,
} from "@multica/core/runtimes";
import type { ModelTier, ModelTierInput } from "@multica/core/types";
import { useT } from "../../i18n";
import {
  SettingsCard,
  SettingsReadOnlyNotice,
  SettingsSection,
  SettingsTab,
} from "./settings-layout";

/**
 * Workspace model tiers (M-350). The ordered list the M-351 resolver walks at
 * enqueue: index 0 first, 99 last. `provider_slots` is the max simultaneous
 * sessions on that runtime/provider (e.g. Freebuff = 1) — not an agent
 * concurrency cap. Runtime/model pickers reuse the existing runtime catalog
 * (`POST /runtimes/{id}/models`); this tab stores the chosen values only.
 */

function TierRow({
  tier,
  position,
  total,
  canManage,
  onEdit,
  onDelete,
  onMove,
  movePending,
}: {
  tier: ModelTier;
  position: number;
  total: number;
  canManage: boolean;
  onEdit: () => void;
  onDelete: () => void;
  onMove: (direction: -1 | 1) => void;
  movePending: boolean;
}) {
  const { t } = useT("settings");
  return (
    <li
      data-testid={`tier-row-${tier.id}`}
      className="flex min-w-0 items-center gap-3 px-4 py-3"
    >
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
          <span className="truncate font-medium">{tier.name}</span>
          {!tier.enabled && (
            <Badge variant="secondary">{t(($) => $.tiers.disabled_badge)}</Badge>
          )}
        </div>
        <span className="truncate text-caption text-muted-foreground">
          {tier.provider} · {tier.model}
          {tier.runtime_id
            ? ""
            : ` · ${t(($) => $.tiers.no_runtime_suffix)}`}
          {tier.cost ? ` · ${tier.cost}` : ""}
          {typeof tier.quality === "number" ? ` · Q${tier.quality}` : ""}
          {` · ×${tier.provider_slots}`}
        </span>
      </div>
      {canManage && (
        <div className="flex shrink-0 items-center gap-1">
          <Button
            variant="ghost"
            size="sm"
            disabled={position === 0 || movePending}
            onClick={() => onMove(-1)}
            aria-label={t(($) => $.tiers.move_up, { name: tier.name })}
          >
            ↑
          </Button>
          <Button
            variant="ghost"
            size="sm"
            disabled={position === total - 1 || movePending}
            onClick={() => onMove(1)}
            aria-label={t(($) => $.tiers.move_down, { name: tier.name })}
          >
            ↓
          </Button>
          <Button variant="ghost" size="sm" onClick={onEdit}>
            {t(($) => $.tiers.edit_action)}
          </Button>
          <Button variant="ghost" size="sm" onClick={onDelete}>
            {t(($) => $.tiers.remove_action)}
          </Button>
        </div>
      )}
    </li>
  );
}

export function TiersTab() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const { role } = useCurrentMember(wsId);
  const canManage = role === "owner" || role === "admin";

  const tiersQuery = useQuery(modelTiersOptions(wsId));
  const createTier = useCreateModelTier(wsId);
  const updateTier = useUpdateModelTier(wsId);
  const deleteTier = useDeleteModelTier(wsId);
  const reorderTiers = useReorderModelTiers(wsId);

  const tiers = useMemo(() => tiersQuery.data ?? [], [tiersQuery.data]);

  const [editorOpen, setEditorOpen] = useState(false);
  const [editingTier, setEditingTier] = useState<ModelTier | null>(null);
  const [deletingTier, setDeletingTier] = useState<ModelTier | null>(null);

  const handleMove = async (tier: ModelTier, direction: -1 | 1) => {
    const ids = tiers.map((row) => row.id);
    const from = ids.indexOf(tier.id);
    const to = from + direction;
    if (from < 0 || to < 0 || to >= ids.length) return;
    const next = [...ids];
    next.splice(from, 1);
    next.splice(to, 0, tier.id);
    try {
      await reorderTiers.mutateAsync(next);
    } catch {
      toast.error(t(($) => $.tiers.reorder_failed_toast));
    }
  };

  return (
    <SettingsTab
      title={t(($) => $.tiers.title)}
      description={t(($) => $.tiers.description)}
      scope="workspace"
    >
      <SettingsSection title={t(($) => $.tiers.list_title)}>
        <SettingsCard>
          {!canManage && <SettingsReadOnlyNotice wsId={wsId} />}
          {tiersQuery.isLoading ? (
            <p className="px-4 py-6 text-body text-muted-foreground">
              {t(($) => $.tiers.loading)}
            </p>
          ) : tiers.length === 0 ? (
            <div className="space-y-3 px-4 py-6">
              <p className="font-medium">{t(($) => $.tiers.empty_title)}</p>
              <p className="text-body text-muted-foreground">
                {t(($) => $.tiers.empty_description)}
              </p>
              {canManage && (
                <Button
                  onClick={() => {
                    setEditingTier(null);
                    setEditorOpen(true);
                  }}
                >
                  {t(($) => $.tiers.add_tier)}
                </Button>
              )}
            </div>
          ) : (
            <>
              {canManage && (
                <div className="flex justify-end border-b px-4 py-2">
                  <Button
                    onClick={() => {
                      setEditingTier(null);
                      setEditorOpen(true);
                    }}
                  >
                    {t(($) => $.tiers.add_tier)}
                  </Button>
                </div>
              )}
              <ul className="divide-y">
                {tiers.map((tier, index) => (
                  <TierRow
                    key={tier.id}
                    tier={tier}
                    position={index}
                    total={tiers.length}
                    canManage={canManage}
                    onEdit={() => {
                      setEditingTier(tier);
                      setEditorOpen(true);
                    }}
                    onDelete={() => setDeletingTier(tier)}
                    onMove={(direction) => void handleMove(tier, direction)}
                    movePending={reorderTiers.isPending}
                  />
                ))}
              </ul>
            </>
          )}
        </SettingsCard>
      </SettingsSection>

      {editorOpen && (
        <TierEditorDialog
          wsId={wsId}
          tier={editingTier}
          onClose={() => {
            setEditorOpen(false);
            setEditingTier(null);
          }}
          onSaved={() => {
            setEditorOpen(false);
            setEditingTier(null);
          }}
          createTier={createTier}
          updateTier={updateTier}
        />
      )}

      <AlertDialog
        open={deletingTier !== null}
        onOpenChange={(open) => {
          if (!open) setDeletingTier(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.tiers.delete_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {deletingTier
                ? t(($) => $.tiers.delete_description, {
                    name: deletingTier.name,
                  })
                : null}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.tiers.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (!deletingTier) return;
                deleteTier.mutate(deletingTier.id, {
                  onSuccess: () => {
                    toast.success(t(($) => $.tiers.removed_toast));
                    setDeletingTier(null);
                  },
                  onError: () => {
                    toast.error(t(($) => $.tiers.remove_failed_toast));
                  },
                });
              }}
            >
              {t(($) => $.tiers.delete_confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}
/** Dialog contract: named so consumers don't couple to the hook shape. */
export interface TierCreateMutation {
  mutateAsync: (input: ModelTierInput) => Promise<ModelTier>;
  isPending: boolean;
}
export interface TierUpdateMutation {
  mutateAsync: (input: ModelTierInput & { tierId: string }) => Promise<ModelTier>;
  isPending: boolean;
}
/**
 * Create/edit dialog. Runtime and model are dropdowns fed by the existing
 * runtime list and model catalog — no new backend for catalogs. The model
 * dropdown stays usable when discovery is offline: it renders the error with
 * the stored value kept editable as free text.
 */
function TierEditorDialog({
  wsId,
  tier,
  onClose,
  onSaved,
  createTier,
  updateTier,
}: {
  wsId: string;
  tier: ModelTier | null;
  onClose: () => void;
  onSaved: () => void;
  createTier: TierCreateMutation;
  updateTier: TierUpdateMutation;
}) {
  const { t } = useT("settings");
  const [name, setName] = useState(tier?.name ?? "");
  const [runtimeId, setRuntimeId] = useState(tier?.runtime_id ?? "");
  const [provider, setProvider] = useState(tier?.provider ?? "");
  const [model, setModel] = useState(tier?.model ?? "");
  const [modelCustom, setModelCustom] = useState(false);
  const [thinkingLevel, setThinkingLevel] = useState(
    tier?.thinking_level ?? "",
  );
  const [serviceTier, setServiceTier] = useState(tier?.service_tier ?? "");
  const [cost, setCost] = useState(tier?.cost ?? "");
  const [quality, setQuality] = useState(
    tier?.quality !== undefined ? String(tier.quality) : "",
  );
  const [providerSlots, setProviderSlots] = useState(
    String(tier?.provider_slots ?? 1),
  );
  const [enabled, setEnabled] = useState(tier?.enabled ?? true);
  const [error, setError] = useState("");
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});

  const runtimesQuery = useQuery(runtimeListOptions(wsId));
  const runtimes = useMemo(
    () => runtimesQuery.data ?? [],
    [runtimesQuery.data],
  );
  const onlineRuntimes = useMemo(
    () => runtimes.filter((r) => r.status === "online"),
    [runtimes],
  );
  const selectedRuntime =
    runtimes.find((r) => r.id === runtimeId) ?? null;

  const modelsQuery = useQuery({
    ...runtimeModelsOptions(runtimeId || null),
    enabled: runtimeId !== "",
  });
  const models = useMemo(
    () => modelsQuery.data?.models ?? [],
    [modelsQuery.data],
  );
  const thinkingLevels = useMemo(() => {
    const selected = models.find((m) => m.id === model);
    return selected?.thinking?.supported_levels ?? [];
  }, [models, model]);
  const serviceTiers = useMemo(() => {
    const selected = models.find((m) => m.id === model);
    return selected?.service_tiers ?? [];
  }, [models, model]);

  const pending = createTier.isPending || updateTier.isPending;

  const handleSave = async () => {
    setError("");
    const nextErrors: Record<string, string> = {};
    if (!name.trim())
      nextErrors.name = t(($) => $.tiers.name_required);
    if (!provider.trim())
      nextErrors.provider = t(($) => $.tiers.provider_required);
    if (!model.trim())
      nextErrors.model = t(($) => $.tiers.model_required);
    const slots = Number(providerSlots);
    if (!Number.isInteger(slots) || slots < 1)
      nextErrors.provider_slots = t(
        ($) => $.tiers.provider_slots_invalid,
      );
    if (quality.trim() !== "") {
      const q = Number(quality);
      if (!Number.isInteger(q) || q < 0 || q > 100)
        nextErrors.quality = t(($) => $.tiers.quality_invalid);
    }
    if (cost.trim() !== "" && !Number.isFinite(Number(cost)))
      nextErrors.cost = t(($) => $.tiers.cost_invalid);
    setFieldErrors(nextErrors);
    if (Object.keys(nextErrors).length > 0) return;

    const input = {
      name: name.trim(),
      runtime_id: runtimeId || "",
      provider: provider.trim(),
      model: model.trim(),
      ...(thinkingLevel.trim()
        ? { thinking_level: thinkingLevel.trim() }
        : {}),
      ...(serviceTier.trim() ? { service_tier: serviceTier.trim() } : {}),
      ...(cost.trim() !== "" ? { cost: cost.trim() } : {}),
      ...(quality.trim() !== ""
        ? { quality: Number(quality) }
        : {}),
      provider_slots: Number(providerSlots),
      enabled,
    };
    try {
      if (tier) {
        await updateTier.mutateAsync({ tierId: tier.id, ...input });
        toast.success(t(($) => $.tiers.updated_toast));
      } else {
        await createTier.mutateAsync(input);
        toast.success(t(($) => $.tiers.added_toast));
      }
      onSaved();
    } catch {
      setError(t(($) => $.tiers.save_failed_toast));
    }
  };

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent data-testid="tier-editor">
        <DialogHeader>
          <DialogTitle>
            {tier
              ? t(($) => $.tiers.edit_title)
              : t(($) => $.tiers.add_title)}
          </DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="tier-name">{t(($) => $.tiers.name_label)}</Label>
            <Input
              id="tier-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t(($) => $.tiers.name_placeholder)}
            />
            {fieldErrors.name && (
              <p className="text-caption text-destructive">{fieldErrors.name}</p>
            )}
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="tier-runtime">
              {t(($) => $.tiers.runtime_label)}
            </Label>
            <Select
              items={[
                { value: "__none", label: t(($) => $.tiers.runtime_none) },
                ...onlineRuntimes.map((r) => ({
                  value: r.id,
                  label: `${r.name} · ${r.provider}`,
                })),
                ...runtimes
                  .filter((r) => r.status !== "online")
                  .map((r) => ({
                    value: r.id,
                    label: `${r.name} · ${t(($) => $.tiers.runtime_offline)}`,
                  })),
              ]}
              value={runtimeId || "__none"}
              onValueChange={(v) => {
                if (!v) return;
                const next = v === "__none" ? "" : v;
                setRuntimeId(next);
                const rt = runtimes.find((r) => r.id === next);
                if (rt) setProvider(rt.provider);
                setModel("");
                setModelCustom(false);
              }}
            >
              <SelectTrigger id="tier-runtime">
                <SelectValue
                  placeholder={t(($) => $.tiers.runtime_placeholder)}
                />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="__none">
                  {t(($) => $.tiers.runtime_none)}
                </SelectItem>
                {onlineRuntimes.map((r) => (
                  <SelectItem key={r.id} value={r.id}>
                    {r.name} · {r.provider}
                  </SelectItem>
                ))}
                {runtimes
                  .filter((r) => r.status !== "online")
                  .map((r) => (
                    <SelectItem key={r.id} value={r.id}>
                      {r.name} · {t(($) => $.tiers.runtime_offline)}
                    </SelectItem>
                  ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="tier-provider">
              {t(($) => $.tiers.provider_label)}
            </Label>
            <Input
              id="tier-provider"
              value={provider}
              onChange={(e) => setProvider(e.target.value)}
              placeholder={t(($) => $.tiers.provider_placeholder)}
            />
            {fieldErrors.provider && (
              <p className="text-caption text-destructive">
                {fieldErrors.provider}
              </p>
            )}
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="tier-model">{t(($) => $.tiers.model_label)}</Label>
            {runtimeId === "" ? (
              <Input
                id="tier-model"
                value={model}
                onChange={(e) => setModel(e.target.value)}
                placeholder={t(($) => $.tiers.model_placeholder)}
              />
            ) : modelsQuery.isPending ? (
              <p className="flex items-center gap-2 text-body text-muted-foreground">
                <Loader2 aria-hidden="true" className="size-4 animate-spin" />
                {t(($) => $.tiers.models_loading)}
              </p>
            ) : modelsQuery.isError || modelCustom ? (
              <div className="space-y-1.5">
                {modelsQuery.isError && !modelCustom && (
                  <p className="text-caption text-muted-foreground">
                    {t(($) => $.tiers.models_offline_note)}
                  </p>
                )}
                <Input
                  id="tier-model"
                  value={model}
                  onChange={(e) => setModel(e.target.value)}
                  placeholder={t(($) => $.tiers.model_placeholder)}
                />
                {!modelCustom && modelsQuery.isError && (
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => modelsQuery.refetch()}
                  >
                    {t(($) => $.tiers.models_retry)}
                  </Button>
                )}
              </div>
            ) : (
              <div className="space-y-1.5">
                <Select
                  items={[
                    ...models.map((m) => ({
                      value: m.id,
                      label: m.label || m.id,
                    })),
                    {
                      value: "__custom",
                      label: t(($) => $.tiers.model_custom),
                    },
                  ]}
                  value={model || "__custom"}
                  onValueChange={(v) => {
                    if (!v) return;
                    if (v === "__custom") {
                      setModelCustom(true);
                      return;
                    }
                    setModel(v);
                  }}
                >
                  <SelectTrigger id="tier-model">
                    <SelectValue
                      placeholder={t(($) => $.tiers.model_placeholder)}
                    />
                  </SelectTrigger>
                  <SelectContent>
                    {models.map((m) => (
                      <SelectItem key={m.id} value={m.id}>
                        {m.label || m.id}
                      </SelectItem>
                    ))}
                    <SelectItem value="__custom">
                      {t(($) => $.tiers.model_custom)}
                    </SelectItem>
                  </SelectContent>
                </Select>
                {selectedRuntime && modelsQuery.data?.supported === false && (
                  <p className="text-caption text-muted-foreground">
                    {t(($) => $.tiers.models_unsupported_note)}
                  </p>
                )}
              </div>
            )}
            {fieldErrors.model && (
              <p className="text-caption text-destructive">
                {fieldErrors.model}
              </p>
            )}
          </div>

          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="tier-thinking">
                {t(($) => $.tiers.thinking_label)}
              </Label>
              {thinkingLevels.length > 0 ? (
                <Select
                  items={[
                    {
                      value: "__default",
                      label: t(($) => $.tiers.runtime_default),
                    },
                    ...thinkingLevels.map((level) => ({
                      value: level.value,
                      label: level.label || level.value,
                    })),
                  ]}
                  value={thinkingLevel || "__default"}
                  onValueChange={(v) => {
                    if (!v) return;
                    setThinkingLevel(v === "__default" ? "" : v);
                  }}
                >
                  <SelectTrigger id="tier-thinking">
                    <SelectValue
                      placeholder={t(($) => $.tiers.optional_placeholder)}
                    />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__default">
                      {t(($) => $.tiers.runtime_default)}
                    </SelectItem>
                    {thinkingLevels.map((level) => (
                      <SelectItem key={level.value} value={level.value}>
                        {level.label || level.value}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : (
                <Input
                  id="tier-thinking"
                  value={thinkingLevel}
                  onChange={(e) => setThinkingLevel(e.target.value)}
                  placeholder={t(($) => $.tiers.optional_placeholder)}
                />
              )}
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="tier-service">
                {t(($) => $.tiers.service_label)}
              </Label>
              {serviceTiers.length > 0 ? (
                <Select
                  items={[
                    {
                      value: "__default",
                      label: t(($) => $.tiers.runtime_default),
                    },
                    ...serviceTiers.map((tier) => ({
                      value: tier.id,
                      label: tier.name || tier.id,
                    })),
                  ]}
                  value={serviceTier || "__default"}
                  onValueChange={(v) => {
                    if (!v) return;
                    setServiceTier(v === "__default" ? "" : v);
                  }}
                >
                  <SelectTrigger id="tier-service">
                    <SelectValue
                      placeholder={t(($) => $.tiers.optional_placeholder)}
                    />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__default">
                      {t(($) => $.tiers.runtime_default)}
                    </SelectItem>
                    {serviceTiers.map((tier) => (
                      <SelectItem key={tier.id} value={tier.id}>
                        {tier.name || tier.id}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : (
                <Input
                  id="tier-service"
                  value={serviceTier}
                  onChange={(e) => setServiceTier(e.target.value)}
                  placeholder={t(($) => $.tiers.optional_placeholder)}
                />
              )}
            </div>
          </div>

          <div className="grid grid-cols-3 gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="tier-cost">{t(($) => $.tiers.cost_label)}</Label>
              <Input
                id="tier-cost"
                inputMode="decimal"
                value={cost}
                onChange={(e) => setCost(e.target.value)}
                placeholder={t(($) => $.tiers.optional_placeholder)}
              />
              {fieldErrors.cost && (
                <p className="text-caption text-destructive">
                  {fieldErrors.cost}
                </p>
              )}
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="tier-quality">
                {t(($) => $.tiers.quality_label)}
              </Label>
              <Input
                id="tier-quality"
                inputMode="numeric"
                value={quality}
                onChange={(e) => setQuality(e.target.value)}
                placeholder="0–100"
              />
              {fieldErrors.quality && (
                <p className="text-caption text-destructive">
                  {fieldErrors.quality}
                </p>
              )}
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="tier-slots">{t(($) => $.tiers.slots_label)}</Label>
              <Input
                id="tier-slots"
                inputMode="numeric"
                value={providerSlots}
                onChange={(e) => setProviderSlots(e.target.value)}
              />
              {fieldErrors.provider_slots && (
                <p className="text-caption text-destructive">
                  {fieldErrors.provider_slots}
                </p>
              )}
            </div>
          </div>

          <div className="flex items-center justify-between gap-4">
            <Label htmlFor="tier-enabled">
              {t(($) => $.tiers.enabled_label)}
            </Label>
            <Switch
              id="tier-enabled"
              checked={enabled}
              onCheckedChange={setEnabled}
            />
          </div>

          {error && <p className="text-body text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose} disabled={pending}>
            {t(($) => $.tiers.cancel)}
          </Button>
          <Button onClick={() => void handleSave()} disabled={pending}>
            {pending
              ? t(($) => $.tiers.saving)
              : t(($) => $.tiers.save_action)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
