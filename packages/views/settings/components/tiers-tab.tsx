"use client";

import { useEffect, useMemo, useState } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
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
import { Checkbox } from "@multica/ui/components/ui/checkbox";
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
import { ApiError } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import {
  modelTierMembersOptions,
  modelTiersOptions,
} from "@multica/core/workspace/queries";
import {
  useCreateModelTier,
  useCreateModelTierMember,
  useDeleteModelTier,
  useDeleteModelTierMember,
  useReorderModelTiers,
  useUpdateModelTier,
  useUpdateModelTierMember,
} from "@multica/core/workspace/mutations";
import {
  runtimeListOptions,
  runtimeModelsOptions,
} from "@multica/core/runtimes";
import type {
  ModelTier,
  ModelTierInput,
  ModelTierMember,
  ModelTierMemberInput,
} from "@multica/core/types";
import { useT } from "../../i18n";
import { PresetsSection } from "./presets-section";
import {
  SettingsCard,
  SettingsReadOnlyNotice,
  SettingsSection,
  SettingsTab,
} from "./settings-layout";

/**
 * Workspace tier profiles (M-350, M-481). A tier is a profile — index, roles,
 * quality, quota kinds, auto-pick, context ceiling — plus one or more ordered
 * (harness, model) members. The resolver picks the tier by idx, then its first
 * available member, so a rate limit on one harness only cools that member.
 */

/** Existing member positions stay far below this; new rows park here first. */
const POSITION_SCRATCH_GAP = 1000;

/**
 * `HH:MM` in the viewer's own clock for a cooldown deadline (M-352/M-481), or
 * null when it is not cooling. The server sends an absolute timestamp; an
 * already-expired one renders nothing, since the resolver has the row back at
 * its very next pass and a stale label would be misleading.
 */
function cooldownClock(until?: string): string | null {
  if (!until) return null;
  const at = new Date(until);
  if (Number.isNaN(at.getTime()) || at.getTime() <= Date.now()) return null;
  const hours = String(at.getHours()).padStart(2, "0");
  const minutes = String(at.getMinutes()).padStart(2, "0");
  return `${hours}:${minutes}`;
}

function TierMemberChip({
  member,
  tierId,
}: {
  member: ModelTierMember;
  tierId: string;
}) {
  const { t } = useT("settings");
  const cooling = cooldownClock(member.cooldown_until);
  return (
    <span
      data-testid={`tier-member-${tierId}-${member.id}`}
      title={
        cooling
          ? t(($) => $.tiers.member_cooling_until, { time: cooling })
          : [
              member.runtime_id
                ? null
                : t(($) => $.tiers.member_unbound),
              member.thinking_level || null,
              `×${member.provider_slots}`,
            ]
              .filter(Boolean)
              .join(" · ")
      }
      className={[
        "inline-flex max-w-full items-center gap-1 rounded-full border border-surface-border px-2 py-0.5 text-caption",
        member.enabled ? "" : "opacity-60 line-through",
        cooling ? "text-muted-foreground" : "",
      ].join(" ")}
    >
      <span className="truncate">
        {member.provider} · {member.model}
      </span>
      {cooling && (
        <span
          aria-hidden
          className="size-1.5 shrink-0 rounded-full bg-warning"
        />
      )}
    </span>
  );
}

function TierRow({
  tier,
  members,
  membersLoading,
  position,
  total,
  canManage,
  onEdit,
  onDelete,
  onMove,
  movePending,
}: {
  tier: ModelTier;
  members: ModelTierMember[];
  membersLoading: boolean;
  position: number;
  total: number;
  canManage: boolean;
  onEdit: () => void;
  onDelete: () => void;
  onMove: (direction: -1 | 1) => void;
  movePending: boolean;
}) {
  const { t } = useT("settings");
  const coolingUntil = cooldownClock(tier.cooldown_until);
  // Older backends omit auto_pick; `manual_only` is its complement.
  const autoPick = tier.auto_pick ?? !tier.manual_only;
  // M-493: show which models share this tier profile next to its metadata,
  // so the list no longer looks like one model per tier.
  const modelSummary = [...new Set(members.map((member) => member.model).filter(Boolean))].join(", ");
  return (
    <li
      data-testid={`tier-row-${tier.id}`}
      className="flex min-w-0 items-start gap-3 px-4 py-3"
    >
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
          <span className="truncate font-medium">{tier.name}</span>
          {!tier.enabled && (
            <Badge variant="secondary">{t(($) => $.tiers.disabled_badge)}</Badge>
          )}
          {autoPick ? (
            <Badge
              variant="outline"
              data-testid={`tier-auto-pick-${tier.id}`}
              title={t(($) => $.tiers.auto_pick_hint)}
            >
              {t(($) => $.tiers.auto_pick_badge)}
            </Badge>
          ) : (
            <Badge
              variant="outline"
              data-testid={`tier-auto-pick-${tier.id}`}
              title={t(($) => $.tiers.manual_hint)}
            >
              {t(($) => $.tiers.auto_pick_badge_manual)}
            </Badge>
          )}
          {(tier.roles ?? []).length === 0 && (
            <Badge variant="secondary">
              {t(($) => $.tiers.unconfigured_badge)}
            </Badge>
          )}
          {coolingUntil && (
            <Badge
              variant="outline"
              data-testid={`tier-cooldown-${tier.id}`}
              title={t(($) => $.tiers.cooling_down_hint)}
            >
              {t(($) => $.tiers.cooling_down_until, { time: coolingUntil })}
            </Badge>
          )}
        </div>
        <span className="truncate text-caption text-muted-foreground">
          {typeof tier.quality === "number" ? `Q${tier.quality}` : ""}
          {(tier.roles ?? []).length > 0
            ? ` · ${(tier.roles ?? []).join(", ")}`
            : ""}
          {tier.quota_kinds.length > 0
            ? ` · ${tier.quota_kinds.length}× quota`
            : ""}
          {modelSummary ? ` · ${modelSummary}` : ""}
        </span>
        <div
          data-testid={`tier-members-${tier.id}`}
          className="flex min-w-0 flex-wrap items-center gap-1"
        >
          {membersLoading ? (
            <span className="text-caption text-faint-foreground">…</span>
          ) : members.length === 0 ? (
            <span
              data-testid={`tier-no-members-${tier.id}`}
              className="text-caption text-muted-foreground"
            >
              {t(($) => $.tiers.members_empty)}
            </span>
          ) : (
            members.map((member) => (
              <TierMemberChip
                key={member.id}
                member={member}
                tierId={tier.id}
              />
            ))
          )}
        </div>
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
  const createMember = useCreateModelTierMember(wsId);
  const updateMember = useUpdateModelTierMember(wsId);
  const deleteMember = useDeleteModelTierMember(wsId);

  const tiers = useMemo(() => tiersQuery.data ?? [], [tiersQuery.data]);

  // One cached member query per visible tier: the list payload carries the
  // profile, and M-481 shows each tier's members as chips.
  const memberQueries = useQueries({
    queries: tiers.map((tier) => modelTierMembersOptions(wsId, tier.id)),
  });
  const membersByTier = useMemo(() => {
    const map = new Map<
      string,
      { members: ModelTierMember[]; loading: boolean }
    >();
    tiers.forEach((tier, index) => {
      const query = memberQueries[index];
      map.set(tier.id, {
        members: query?.data ?? [],
        loading: Boolean(query?.isLoading),
      });
    });
    return map;
  }, [tiers, memberQueries]);

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
                {tiers.map((tier, index) => {
                  const state = membersByTier.get(tier.id);
                  return (
                    <TierRow
                      key={tier.id}
                      tier={tier}
                      members={state?.members ?? []}
                      membersLoading={state?.loading ?? false}
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
                  );
                })}
              </ul>
            </>
          )}
        </SettingsCard>
      </SettingsSection>

      <PresetsSection wsId={wsId} canManage={canManage} />

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
          createMember={createMember}
          updateMember={updateMember}
          deleteMember={deleteMember}
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
export interface TierMemberMutation {
  mutateAsync: (
    input: ModelTierMemberInput & { tierId: string },
  ) => Promise<ModelTierMember>;
  isPending: boolean;
}
export interface TierMemberUpdateMutation {
  mutateAsync: (
    input: ModelTierMemberInput & { tierId: string; memberId: string },
  ) => Promise<ModelTierMember>;
  isPending: boolean;
}
export interface TierMemberDeleteMutation {
  mutateAsync: (input: { tierId: string; memberId: string }) => Promise<void>;
  isPending: boolean;
}

/** One editable member line. `id` is absent until the row is saved. */
interface DraftMember {
  key: string;
  id?: string;
  provider: string;
  model: string;
  thinkingLevel: string;
  runtimeId: string;
  providerSlots: string;
  enabled: boolean;
}

let draftSeq = 0;
function nextDraftKey(): string {
  draftSeq += 1;
  return `draft-${draftSeq}`;
}

function draftFromMember(member: ModelTierMember): DraftMember {
  return {
    key: nextDraftKey(),
    id: member.id,
    provider: member.provider,
    model: member.model,
    thinkingLevel: member.thinking_level ?? "",
    runtimeId: member.runtime_id ?? "",
    providerSlots: String(member.provider_slots),
    enabled: member.enabled,
  };
}

function blankDraftMember(): DraftMember {
  return {
    key: nextDraftKey(),
    provider: "",
    model: "",
    thinkingLevel: "",
    runtimeId: "",
    // A free provider usually runs one session at a time; the admin can raise
    // it per member (M-481 keeps slots on the member).
    providerSlots: "1",
    enabled: true,
  };
}

/**
 * Create/edit dialog (M-481). Large and scrollable: a profile block on top and
 * the ordered member editor below. Members are reconciled on save — deletes,
 * then per-row writes, then a two-phase position pass — because the API has no
 * atomic "replace members" call and the (tier_id, position) unique index
 * rejects a straight swap.
 */
function TierEditorDialog({
  wsId,
  tier,
  onClose,
  onSaved,
  createTier,
  updateTier,
  createMember,
  updateMember,
  deleteMember,
}: {
  wsId: string;
  tier: ModelTier | null;
  onClose: () => void;
  onSaved: () => void;
  createTier: TierCreateMutation;
  updateTier: TierUpdateMutation;
  createMember: TierMemberMutation;
  updateMember: TierMemberUpdateMutation;
  deleteMember: TierMemberDeleteMutation;
}) {
  const { t } = useT("settings");
  const [name, setName] = useState(tier?.name ?? "");
  const [cost, setCost] = useState(tier?.cost ?? "");
  const [quality, setQuality] = useState(
    tier?.quality !== undefined ? String(tier.quality) : "",
  );
  // M-446: explicit clear-to-unknown. Blank-on-edit is NOT a clear (it keeps
  // the value, same as the API's omitted semantics) — the admin must check
  // this to send quality: null. Only meaningful when editing a measured value.
  const [clearQuality, setClearQuality] = useState(false);
  const [serviceTier, setServiceTier] = useState(tier?.service_tier ?? "");
  const [enabled, setEnabled] = useState(tier?.enabled ?? true);
  const [roles, setRoles] = useState<string[]>(tier?.roles ?? []);
  const [autoPick, setAutoPick] = useState(
    tier?.auto_pick ?? !(tier?.manual_only ?? false),
  );
  const [quotaKinds, setQuotaKinds] = useState<string[]>(
    tier?.quota_kinds ?? [],
  );
  const [isFree, setIsFree] = useState(tier?.is_free ?? true);
  const [ctxMax, setCtxMax] = useState(
    String(tier?.ctx_max_tokens ?? 280000),
  );
  const [compactPct, setCompactPct] = useState(
    String(tier?.compact_at_pct ?? 75),
  );
  const [rows, setRows] = useState<DraftMember[] | null>(
    tier ? null : [blankDraftMember()],
  );
  const [error, setError] = useState("");
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});

  // Existing members land after the dialog opens; seed the editable rows once.
  const membersQuery = useQuery(modelTierMembersOptions(wsId, tier?.id ?? ""));
  useEffect(() => {
    if (rows !== null || !tier) return;
    if (!membersQuery.isSuccess) return;
    setRows(membersQuery.data.map(draftFromMember));
  }, [rows, tier, membersQuery.isSuccess, membersQuery.data]);

  const runtimesQuery = useQuery(runtimeListOptions(wsId));
  const runtimes = useMemo(
    () => runtimesQuery.data ?? [],
    [runtimesQuery.data],
  );

  const pending =
    createTier.isPending ||
    updateTier.isPending ||
    createMember.isPending ||
    updateMember.isPending ||
    deleteMember.isPending;

  const ctxTokens = Number(ctxMax);
  const compactTokens =
    Number.isFinite(ctxTokens) && Number.isFinite(Number(compactPct))
      ? Math.round((ctxTokens * Number(compactPct)) / 100)
      : 0;

  const updateRow = (key: string, patch: Partial<DraftMember>) => {
    setRows((prev) =>
      (prev ?? []).map((row) => (row.key === key ? { ...row, ...patch } : row)),
    );
  };

  const moveRow = (index: number, direction: -1 | 1) => {
    setRows((prev) => {
      if (!prev) return prev;
      const to = index + direction;
      if (to < 0 || to >= prev.length) return prev;
      const next = [...prev];
      const [row] = next.splice(index, 1);
      if (row) next.splice(to, 0, row);
      return next;
    });
  };

  /**
   * Persists the draft rows against a saved tier. `baseIndex` is the first
   * position these rows own: 0 when editing, 1 when creating (the server
   * backfilled member 0 from the profile's first harness/model).
   */
  const reconcileMembers = async (
    tierId: string,
    draft: DraftMember[],
    existing: ModelTierMember[],
    baseIndex = 0,
  ) => {
    const keptIds = new Set(draft.map((row) => row.id).filter(Boolean));
    for (const member of existing) {
      if (!keptIds.has(member.id)) {
        await deleteMember.mutateAsync({ tierId, memberId: member.id });
      }
    }

    // Park new rows above every stored position: their final slot may still be
    // occupied while the swap is in progress. With no stored members every
    // target slot is free, so the rows can be written in final order at once.
    const slotsFree = existing.length === 0;
    const scratch =
      Math.max(0, ...existing.map((member) => member.position)) +
      POSITION_SCRATCH_GAP;
    const positions = new Map<string, number>();
    for (const member of existing) positions.set(member.id, member.position);
    const createdIds = new Map<string, string>();

    for (const [index, row] of draft.entries()) {
      const fields: ModelTierMemberInput = {
        provider: row.provider.trim(),
        model: row.model.trim(),
        thinking_level: row.thinkingLevel.trim(),
        runtime_id: row.runtimeId,
        provider_slots: Number(row.providerSlots),
      };
      if (row.id) {
        await updateMember.mutateAsync({
          tierId,
          memberId: row.id,
          ...fields,
        });
      } else {
        const position = slotsFree ? baseIndex + index : scratch + index;
        const created = await createMember.mutateAsync({
          tierId,
          position,
          ...fields,
        });
        createdIds.set(row.key, created.id);
        positions.set(created.id, position);
      }
    }

    // Final order is the row order. Resolve every row's id first, then move
    // the rows whose position is wrong: park them, then place them.
    const ordered = draft.map((row, index) => ({
      id: row.id ?? createdIds.get(row.key) ?? "",
      target: baseIndex + index,
    }));
    const moving = ordered.filter(
      (entry) => entry.id !== "" && positions.get(entry.id) !== entry.target,
    );
    if (moving.length === 0) return;
    const parkingBase =
      Math.max(0, ...Array.from(positions.values())) + POSITION_SCRATCH_GAP;
    for (const [index, entry] of moving.entries()) {
      await updateMember.mutateAsync({
        tierId,
        memberId: entry.id,
        position: parkingBase + index,
      });
    }
    for (const entry of moving) {
      await updateMember.mutateAsync({
        tierId,
        memberId: entry.id,
        position: entry.target,
      });
    }
  };

  const handleSave = async () => {
    setError("");
    if (rows === null) return;
    const nextErrors: Record<string, string> = {};
    if (!/^[a-z0-9][a-z0-9-]*$/.test(name.trim()) || name.trim().length > 40)
      nextErrors.name = t(($) => $.tiers.name_required);
    if (!clearQuality && quality.trim() !== "") {
      const q = Number(quality);
      if (!Number.isInteger(q) || q < 0 || q > 100)
        nextErrors.quality = t(($) => $.tiers.quality_invalid);
    }
    if (cost.trim() !== "" && !Number.isFinite(Number(cost)))
      nextErrors.cost = t(($) => $.tiers.cost_invalid);
    const ctx = Number(ctxMax);
    if (!Number.isInteger(ctx) || ctx < 1024 || ctx > 2000000)
      nextErrors.ctx = t(($) => $.tiers.ctx_invalid);
    const pct = Number(compactPct);
    if (!Number.isInteger(pct) || pct < 1 || pct > 100)
      nextErrors.compact = t(($) => $.tiers.compact_invalid);
    if (rows.length === 0) nextErrors.members = t(($) => $.tiers.members_empty);
    rows.forEach((row, index) => {
      if (!row.provider.trim() || !row.model.trim())
        nextErrors[`member-${row.key}`] = t(($) => $.tiers.member_required);
      const rowSlots = Number(row.providerSlots);
      if (!Number.isInteger(rowSlots) || rowSlots < 1)
        nextErrors[`member-slots-${row.key}`] = t(
          ($) => $.tiers.member_invalid_slots,
        );
      void index;
    });
    setFieldErrors(nextErrors);
    if (Object.keys(nextErrors).length > 0) return;

    const profile: ModelTierInput = {
      name: name.trim(),
      ...(serviceTier.trim() ? { service_tier: serviceTier.trim() } : {}),
      ...(cost.trim() !== "" ? { cost: cost.trim() } : {}),
      // M-446: checked clear sends an explicit null (unknown). Unchecked blank
      // omits (keeps the stored value); only the checkbox clears.
      ...(clearQuality && tier
        ? { quality: null }
        : quality.trim() !== ""
          ? { quality: Number(quality) }
          : {}),
      enabled,
      roles,
      // M-481: auto_pick replaces manual_only; the server keeps both in sync.
      auto_pick: autoPick,
      is_free: isFree,
      quota_kinds: quotaKinds,
      ctx_max_tokens: ctx,
      compact_at_pct: pct,
    };

    try {
      if (tier) {
        await updateTier.mutateAsync({ tierId: tier.id, ...profile });
        await reconcileMembers(tier.id, rows, membersQuery.data ?? []);
        toast.success(t(($) => $.tiers.updated_toast));
      } else {
        // The create endpoint still needs the profile's first model, and the
        // server backfills member 0 from it; the rest are added below.
        const first = rows[0]!;
        const created = await createTier.mutateAsync({
          ...profile,
          provider: first.provider.trim(),
          model: first.model.trim(),
          runtime_id: first.runtimeId,
          ...(first.thinkingLevel.trim()
            ? { thinking_level: first.thinkingLevel.trim() }
            : {}),
          provider_slots: Number(first.providerSlots),
        });
        await reconcileMembers(created.id, rows.slice(1), [], 1);
        toast.success(t(($) => $.tiers.added_toast));
      }
      onSaved();
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        setFieldErrors((prev) => ({
          ...prev,
          name: t(($) => $.tiers.name_taken),
        }));
        return;
      }
      setError(t(($) => $.tiers.save_failed_toast));
    }
  };

  const quotaOptions = [
    { kind: "free_daily", label: t(($) => $.tiers.quota_free_daily) },
    { kind: "free", label: t(($) => $.tiers.quota_free) },
    { kind: "local", label: t(($) => $.tiers.quota_local) },
    { kind: "quota_5_hours", label: t(($) => $.tiers.quota_5_hours) },
    { kind: "quota_daily", label: t(($) => $.tiers.quota_daily) },
    { kind: "quota_weekly", label: t(($) => $.tiers.quota_weekly) },
    { kind: "quota_monthly", label: t(($) => $.tiers.quota_monthly) },
  ];
  const roleOptions = [
    { role: "architect", label: t(($) => $.tiers.role_architect) },
    { role: "management", label: t(($) => $.tiers.role_management) },
    { role: "engineer", label: t(($) => $.tiers.role_engineer) },
  ];

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent
        data-testid="tier-editor"
        className="grid max-h-[calc(100%-2rem)] grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden sm:max-w-[960px]"
      >
        <DialogHeader>
          <DialogTitle>
            {tier
              ? t(($) => $.tiers.edit_title)
              : t(($) => $.tiers.add_title)}
          </DialogTitle>
        </DialogHeader>
        <div
          data-testid="tier-editor-body"
          className="-mx-1 min-h-0 space-y-5 overflow-y-auto px-1 pb-1"
        >
          <p className="text-caption text-muted-foreground">
            {t(($) => $.tiers.editor_description)}
          </p>

          <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
            <div className="space-y-1.5">
              <Label htmlFor="tier-name">{t(($) => $.tiers.name_label)}</Label>
              <Input
                id="tier-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t(($) => $.tiers.name_placeholder)}
              />
              {fieldErrors.name && (
                <p className="text-caption text-destructive">
                  {fieldErrors.name}
                </p>
              )}
            </div>
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
                value={clearQuality ? "" : quality}
                onChange={(e) => setQuality(e.target.value)}
                placeholder="0–100"
                disabled={clearQuality}
              />
              {tier && typeof tier.quality === "number" && (
                <label
                  htmlFor="tier-quality-clear"
                  className="flex items-center gap-2 text-caption text-muted-foreground"
                >
                  <input
                    id="tier-quality-clear"
                    type="checkbox"
                    checked={clearQuality}
                    onChange={(e) => setClearQuality(e.target.checked)}
                  />
                  {t(($) => $.tiers.quality_clear)}
                </label>
              )}
              {fieldErrors.quality && (
                <p className="text-caption text-destructive">
                  {fieldErrors.quality}
                </p>
              )}
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="tier-service">
                {t(($) => $.tiers.service_label)}
              </Label>
              <Input
                id="tier-service"
                value={serviceTier}
                onChange={(e) => setServiceTier(e.target.value)}
                placeholder={t(($) => $.tiers.optional_placeholder)}
              />
            </div>
          </div>

          <div className="grid gap-4 md:grid-cols-3">
            <div className="space-y-1.5">
              <Label>{t(($) => $.tiers.roles_label)}</Label>
              <div className="flex flex-wrap gap-x-4 gap-y-1">
                {roleOptions.map((entry) => (
                  <label
                    key={entry.role}
                    htmlFor={`tier-role-${entry.role}`}
                    className="flex items-center gap-1.5 text-sm"
                  >
                    <Checkbox
                      id={`tier-role-${entry.role}`}
                      checked={roles.includes(entry.role)}
                      onCheckedChange={(checked) => {
                        setRoles(
                          checked === true
                            ? [...roles, entry.role]
                            : roles.filter((r) => r !== entry.role),
                        );
                      }}
                    />
                    {entry.label}
                  </label>
                ))}
              </div>
              <p className="text-caption text-muted-foreground">
                {t(($) => $.tiers.roles_hint)}
              </p>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="tier-auto-pick">
                {t(($) => $.tiers.auto_pick_label)}
              </Label>
              <div className="flex items-center gap-2">
                <Switch
                  id="tier-auto-pick"
                  checked={autoPick}
                  onCheckedChange={setAutoPick}
                />
                <span className="text-caption text-muted-foreground">
                  {autoPick
                    ? t(($) => $.tiers.auto_pick_badge)
                    : t(($) => $.tiers.auto_pick_badge_manual)}
                </span>
              </div>
              <p className="text-caption text-muted-foreground">
                {t(($) => $.tiers.auto_pick_hint)}
              </p>
            </div>
            <div className="space-y-3">
              <div className="space-y-1.5">
                <Label htmlFor="tier-free">{t(($) => $.tiers.free_label)}</Label>
                <div>
                  <Switch
                    id="tier-free"
                    checked={isFree}
                    onCheckedChange={setIsFree}
                  />
                </div>
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="tier-enabled">
                  {t(($) => $.tiers.enabled_label)}
                </Label>
                <div>
                  <Switch
                    id="tier-enabled"
                    checked={enabled}
                    onCheckedChange={setEnabled}
                  />
                </div>
              </div>
            </div>
          </div>

          <div className="space-y-1.5">
            <Label>{t(($) => $.tiers.quota_label)}</Label>
            <div className="flex flex-wrap gap-x-4 gap-y-1.5">
              {quotaOptions.map((entry) => (
                <label
                  key={entry.kind}
                  htmlFor={`tier-quota-${entry.kind}`}
                  className="flex items-center gap-1.5 text-sm"
                >
                  <Checkbox
                    id={`tier-quota-${entry.kind}`}
                    checked={quotaKinds.includes(entry.kind)}
                    onCheckedChange={(checked) => {
                      setQuotaKinds(
                        checked === true
                          ? [...quotaKinds, entry.kind]
                          : quotaKinds.filter((k) => k !== entry.kind),
                      );
                    }}
                  />
                  {entry.label}
                </label>
              ))}
            </div>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.tiers.quota_helper)}
            </p>
          </div>

          <div className="grid gap-4 md:grid-cols-3">
            <div className="space-y-1.5">
              <Label htmlFor="tier-ctx">{t(($) => $.tiers.ctx_label)}</Label>
              <Input
                id="tier-ctx"
                inputMode="numeric"
                value={ctxMax}
                onChange={(e) => setCtxMax(e.target.value)}
              />
              {fieldErrors.ctx && (
                <p className="text-caption text-destructive">
                  {fieldErrors.ctx}
                </p>
              )}
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="tier-compact">
                {t(($) => $.tiers.compact_label)}
              </Label>
              <Input
                id="tier-compact"
                inputMode="numeric"
                value={compactPct}
                onChange={(e) => setCompactPct(e.target.value)}
              />
              {fieldErrors.compact && (
                <p className="text-caption text-destructive">
                  {fieldErrors.compact}
                </p>
              )}
              {compactTokens > 0 && (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.tiers.compact_tokens_hint, {
                    tokens: compactTokens.toLocaleString(),
                  })}
                </p>
              )}
            </div>
            <div className="space-y-1.5">
              <Label>{t(($) => $.tiers.advisory_badge)}</Label>
              <Badge
                variant="outline"
                data-testid="tier-context-advisory"
                title={t(($) => $.tiers.advisory_hint)}
              >
                {t(($) => $.tiers.advisory_badge)}
              </Badge>
              <p className="text-caption text-muted-foreground">
                {t(($) => $.tiers.advisory_hint)}
              </p>
            </div>
          </div>

          <div className="space-y-2" data-testid="tier-members-editor">
            <div className="flex items-center justify-between gap-3">
              <div>
                <Label>{t(($) => $.tiers.members_label)}</Label>
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.tiers.members_helper)}
                </p>
              </div>
              <Button
                variant="outline"
                size="sm"
                onClick={() =>
                  setRows((prev) => [...(prev ?? []), blankDraftMember()])
                }
                disabled={rows === null}
              >
                {t(($) => $.tiers.add_member)}
              </Button>
            </div>
            {rows === null ? (
              <p className="flex items-center gap-2 px-2 py-3 text-body text-muted-foreground">
                <Loader2 aria-hidden="true" className="size-4 animate-spin" />
                {t(($) => $.tiers.loading)}
              </p>
            ) : rows.length === 0 ? (
              <p className="rounded-md border border-dashed px-3 py-4 text-body text-muted-foreground">
                {t(($) => $.tiers.members_empty)}
              </p>
            ) : (
              <ul className="space-y-2">
                {rows.map((row, index) => (
                  <MemberRowEditor
                    key={row.key}
                    row={row}
                    index={index}
                    total={rows.length}
                    runtimes={runtimes}
                    pending={pending}
                    error={fieldErrors[`member-${row.key}`]}
                    slotsError={fieldErrors[`member-slots-${row.key}`]}
                    onChange={(patch) => updateRow(row.key, patch)}
                    onMove={(direction) => moveRow(index, direction)}
                    onRemove={() =>
                      setRows((prev) =>
                        (prev ?? []).filter((r) => r.key !== row.key),
                      )
                    }
                  />
                ))}
              </ul>
            )}
            {fieldErrors.members && (
              <p className="text-caption text-destructive">
                {fieldErrors.members}
              </p>
            )}
          </div>

          {error && <p className="text-body text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose} disabled={pending}>
            {t(($) => $.tiers.cancel)}
          </Button>
          <Button
            onClick={() => void handleSave()}
            disabled={pending || rows === null}
          >
            {pending
              ? t(($) => $.tiers.saving)
              : t(($) => $.tiers.save_action)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * One mapping line. The model list comes from the runtime catalog (discovery
 * stays optional: the field falls back to free text when discovery is offline
 * or the model is custom), while the harness is free text because a profile
 * spans several harnesses.
 */
function MemberRowEditor({
  row,
  index,
  total,
  runtimes,
  pending,
  error,
  slotsError,
  onChange,
  onMove,
  onRemove,
}: {
  row: DraftMember;
  index: number;
  total: number;
  runtimes: Array<{ id: string; name: string; provider: string; status: string }>;
  pending: boolean;
  error?: string;
  slotsError?: string;
  onChange: (patch: Partial<DraftMember>) => void;
  onMove: (direction: -1 | 1) => void;
  onRemove: () => void;
}) {
  const { t } = useT("settings");
  const [modelCustom, setModelCustom] = useState(false);
  const modelsQuery = useQuery({
    ...runtimeModelsOptions(row.runtimeId || null),
    enabled: row.runtimeId !== "",
  });
  const models = useMemo(
    () => modelsQuery.data?.models ?? [],
    [modelsQuery.data],
  );

  return (
    <li
      data-testid={`tier-member-row-${index}`}
      // M-496: the row is its own container, so the one-line layout keys off
      // the dialog's real width instead of the viewport's. Below 46rem of row
      // the fields stack two-up and the order badge shares a line with the
      // move/remove buttons; at or above it every field owns one column.
      className="@container space-y-1 rounded-md border bg-surface-raised/40 px-2 py-2"
    >
      <div className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)] items-start gap-2 @[46rem]:grid-cols-[1.25rem_minmax(0,1.35fr)_minmax(0,1.1fr)_6.5rem_minmax(0,8.5rem)_4.5rem_auto]">
        <span
          aria-hidden
          className="col-start-1 row-start-1 flex h-8 w-5 items-center text-caption text-muted-foreground"
          title={t(($) => $.tiers.member_position, { position: index + 1 })}
        >
          {index + 1}
        </span>
        <div className="col-start-1 row-start-2 min-w-0 space-y-1 @[46rem]:col-start-2 @[46rem]:row-start-1">
          <Label htmlFor={`member-provider-${row.key}`} className="text-caption">
            {t(($) => $.tiers.member_provider_label)}
          </Label>
          <Input
            id={`member-provider-${row.key}`}
            value={row.provider}
            onChange={(e) => onChange({ provider: e.target.value })}
            placeholder={t(($) => $.tiers.member_provider_placeholder)}
          />
        </div>
        <div className="col-start-2 row-start-2 min-w-0 space-y-1 @[46rem]:col-start-3 @[46rem]:row-start-1">
          <Label htmlFor={`member-model-${row.key}`} className="text-caption">
            {t(($) => $.tiers.member_model_label)}
          </Label>
          {row.runtimeId !== "" && !modelCustom && !modelsQuery.isError ? (
            <Select
              items={[
                ...models.map((m) => ({
                  value: m.id,
                  label: m.label || m.id,
                })),
                { value: "__custom", label: t(($) => $.tiers.model_custom) },
              ]}
              value={row.model || "__custom"}
              onValueChange={(value) => {
                if (!value) return;
                if (value === "__custom") {
                  setModelCustom(true);
                  return;
                }
                onChange({ model: value });
              }}
            >
              <SelectTrigger
                id={`member-model-${row.key}`}
                className="w-full min-w-0"
              >
                <SelectValue
                  className="truncate"
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
          ) : (
            <Input
              id={`member-model-${row.key}`}
              value={row.model}
              onChange={(e) => onChange({ model: e.target.value })}
              placeholder={t(($) => $.tiers.member_model_placeholder)}
            />
          )}
        </div>
        <div className="col-start-1 row-start-3 min-w-0 space-y-1 @[46rem]:col-start-4 @[46rem]:row-start-1">
          <Label htmlFor={`member-thinking-${row.key}`} className="text-caption">
            {t(($) => $.tiers.member_thinking_label)}
          </Label>
          <Input
            id={`member-thinking-${row.key}`}
            value={row.thinkingLevel}
            onChange={(e) => onChange({ thinkingLevel: e.target.value })}
            placeholder={t(($) => $.tiers.member_thinking_placeholder)}
          />
        </div>
        <div className="col-start-2 row-start-3 min-w-0 space-y-1 @[46rem]:col-start-5 @[46rem]:row-start-1">
          <Label htmlFor={`member-runtime-${row.key}`} className="text-caption">
            {t(($) => $.tiers.member_runtime_label)}
          </Label>
          <Select
            items={[
              { value: "__none", label: t(($) => $.tiers.member_runtime_none) },
              ...runtimes.map((runtime) => ({
                value: runtime.id,
                label:
                  runtime.status === "online"
                    ? `${runtime.name} · ${runtime.provider}`
                    : `${runtime.name} · ${t(($) => $.tiers.member_runtime_offline)}`,
              })),
            ]}
            value={row.runtimeId || "__none"}
            onValueChange={(value) => {
              if (!value) return;
              const next = value === "__none" ? "" : value;
              const runtime = runtimes.find((r) => r.id === next);
              setModelCustom(false);
              onChange({
                runtimeId: next,
                ...(runtime && !row.provider.trim()
                  ? { provider: runtime.provider }
                  : {}),
              });
            }}
          >
            {/* M-496: the trigger is `w-fit` by default, so a long runtime
                label grew past its column and covered Slots. Fill the column
                and let the label clamp. */}
            <SelectTrigger
              id={`member-runtime-${row.key}`}
              className="w-full min-w-0"
            >
              <SelectValue
                className="truncate"
                placeholder={t(($) => $.tiers.runtime_placeholder)}
              />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__none">
                {t(($) => $.tiers.member_runtime_none)}
              </SelectItem>
              {runtimes.map((runtime) => (
                <SelectItem key={runtime.id} value={runtime.id}>
                  {runtime.name} ·{" "}
                  {runtime.status === "online"
                    ? runtime.provider
                    : t(($) => $.tiers.member_runtime_offline)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="col-start-1 row-start-4 min-w-0 space-y-1 @[46rem]:col-start-6 @[46rem]:row-start-1">
          <Label htmlFor={`member-slots-${row.key}`} className="text-caption">
            {t(($) => $.tiers.member_slots_label)}
          </Label>
          <Input
            id={`member-slots-${row.key}`}
            type="text"
            inputMode="numeric"
            pattern="[0-9]*"
            maxLength={3}
            className="max-w-24 text-center tabular-nums"
            value={row.providerSlots}
            onChange={(e) => onChange({ providerSlots: e.target.value })}
          />
        </div>
        <div className="col-start-2 row-start-1 flex items-start justify-end gap-1 @[46rem]:col-start-7 @[46rem]:justify-start @[46rem]:pt-5">
          <Button
            variant="ghost"
            size="sm"
            disabled={index === 0 || pending}
            onClick={() => onMove(-1)}
            aria-label={t(($) => $.tiers.member_move_up, {
              name: `${row.provider} ${row.model}`.trim(),
            })}
          >
            ↑
          </Button>
          <Button
            variant="ghost"
            size="sm"
            disabled={index === total - 1 || pending}
            onClick={() => onMove(1)}
            aria-label={t(($) => $.tiers.member_move_down, {
              name: `${row.provider} ${row.model}`.trim(),
            })}
          >
            ↓
          </Button>
          <Button
            variant="ghost"
            size="sm"
            disabled={pending}
            onClick={onRemove}
            aria-label={t(($) => $.tiers.remove_member)}
          >
            ×
          </Button>
        </div>
      </div>
      {error && <p className="text-caption text-destructive">{error}</p>}
      {slotsError && (
        <p className="text-caption text-destructive">{slotsError}</p>
      )}
    </li>
  );
}
