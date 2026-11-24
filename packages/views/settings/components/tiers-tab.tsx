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
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@multica/ui/components/ui/tabs";
import { Loader2, Plus, X } from "@multica/ui/icons";
import { cn } from "@multica/ui/lib/utils";
import { ApiError } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import {
  modelTierMembersOptions,
  modelTiersOptions,
  tierRolesOptions,
} from "@multica/core/workspace/queries";
import {
  useCreateModelTier,
  useCreateModelTierMember,
  useCreateTierRole,
  useDeleteModelTier,
  useDeleteModelTierMember,
  useDeleteTierRole,
  useReorderModelTiers,
  useRenameTierRole,
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
  TierRole,
} from "@multica/core/types";
import { useT } from "../../i18n";
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
 * Harnesses a tier member may name (M-545). A member's harness is an id the
 * runtime catalog can resolve, never free text: these are the providers the
 * product ships plus whatever this workspace actually runs. A stored value
 * outside the list still appears, so an older row loads and saves unchanged.
 */
const KNOWN_HARNESSES = [
  "freebuff",
  "opencode-go",
  "opencode-zen",
  "codebuddy",
  "claude",
  "codex",
  "pi",
  "omp",
  "dsh",
] as const;

function harnessOptions(
  provider: string,
  runtimes: Array<{ provider: string }>,
): string[] {
  const options: string[] = [];
  const seen = new Set<string>();
  const add = (value: string) => {
    const harness = value.trim();
    if (!harness || seen.has(harness)) return;
    seen.add(harness);
    options.push(harness);
  };
  for (const harness of KNOWN_HARNESSES) add(harness);
  for (const runtime of runtimes) add(runtime.provider);
  add(provider);
  return options;
}

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
  role,
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
  role: string;
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
  const autoPick =
    tier.role_settings?.find((setting) => setting.role === role)?.auto_pick ??
    tier.auto_pick ??
    !tier.manual_only;
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

/**
 * The registry seeds these for every workspace (M-497); a workspace that has
 * none yet still opens on the three classic tabs instead of an empty list.
 */
const FALLBACK_TIER_ROLES = ["architect", "management", "engineer"] as const;

/** Same key shape the server enforces for a registry entry. */
const TIER_ROLE_KEY_PATTERN = /^[a-z0-9][a-z0-9-]{0,39}$/;

/** The server's machine-readable error code, when it sent one. */
function apiErrorCode(error: unknown): string {
  if (!(error instanceof ApiError)) return "";
  const code = (error.body as { code?: unknown } | null)?.code;
  return typeof code === "string" ? code : "";
}

/**
 * Workspace role registry editor (M-543). Roles are free workspace-defined
 * slugs; the Tiers tab builds one tab per registry entry and the tier dialog's
 * role picker reads the same list. Owner/admin only, like the tier writes.
 */
function TierRolesDialog({
  wsId,
  roles,
  roleLabel,
  onClose,
}: {
  wsId: string;
  roles: TierRole[];
  roleLabel: (key: string) => string;
  onClose: () => void;
}) {
  const { t } = useT("settings");
  const createRole = useCreateTierRole(wsId);
  const renameRole = useRenameTierRole(wsId);
  const deleteRole = useDeleteTierRole(wsId);
  const [newKey, setNewKey] = useState("");
  const [renaming, setRenaming] = useState<string | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [deleting, setDeleting] = useState<string | null>(null);
  const [keyError, setKeyError] = useState("");
  const [error, setError] = useState("");
  const pending =
    createRole.isPending || renameRole.isPending || deleteRole.isPending;

  const handleCreate = async () => {
    setError("");
    setKeyError("");
    const key = newKey.trim().toLowerCase();
    if (!TIER_ROLE_KEY_PATTERN.test(key)) {
      setKeyError(t(($) => $.tiers.role_key_invalid));
      return;
    }
    try {
      await createRole.mutateAsync(key);
      setNewKey("");
    } catch (e) {
      setKeyError(
        e instanceof ApiError && e.status === 409
          ? t(($) => $.tiers.role_exists)
          : t(($) => $.tiers.role_save_failed),
      );
    }
  };

  const handleRename = async (key: string) => {
    setError("");
    const next = renameValue.trim().toLowerCase();
    if (!TIER_ROLE_KEY_PATTERN.test(next)) {
      setError(t(($) => $.tiers.role_key_invalid));
      return;
    }
    if (next === key) {
      setRenaming(null);
      return;
    }
    try {
      await renameRole.mutateAsync({ key, newKey: next });
      setRenaming(null);
    } catch (e) {
      setError(
        e instanceof ApiError && e.status === 409
          ? t(($) => $.tiers.role_exists)
          : t(($) => $.tiers.role_save_failed),
      );
    }
  };

  const handleDelete = async (key: string) => {
    setError("");
    try {
      await deleteRole.mutateAsync(key);
      setDeleting(null);
      toast.success(t(($) => $.tiers.role_deleted_toast));
    } catch (e) {
      if (apiErrorCode(e) === "role_in_use") {
        setError(t(($) => $.tiers.role_in_use));
        return;
      }
      setError(t(($) => $.tiers.role_save_failed));
    }
  };

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent data-testid="tier-roles-dialog" className="sm:max-w-[520px]">
        <DialogHeader>
          <DialogTitle>{t(($) => $.tiers.roles_dialog_title)}</DialogTitle>
        </DialogHeader>
        <p className="text-caption text-muted-foreground">
          {t(($) => $.tiers.roles_dialog_description)}
        </p>
        <ul className="divide-y rounded-md border">
          {roles.length === 0 ? (
            <li className="px-3 py-3 text-body text-muted-foreground">
              {t(($) => $.tiers.roles_empty)}
            </li>
          ) : (
            roles.map((entry) => (
              <li
                key={entry.key}
                data-testid={`tier-role-row-${entry.key}`}
                className="flex items-center justify-between gap-2 px-3 py-2"
              >
                {renaming === entry.key ? (
                  <div className="flex flex-1 items-center gap-2">
                    <Input
                      autoFocus
                      value={renameValue}
                      aria-label={t(($) => $.tiers.role_rename_input)}
                      data-testid="tier-role-rename-input"
                      onChange={(e) => setRenameValue(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === "Enter") void handleRename(entry.key);
                      }}
                    />
                    <Button
                      size="sm"
                      disabled={pending}
                      onClick={() => void handleRename(entry.key)}
                    >
                      {t(($) => $.tiers.role_save)}
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={pending}
                      onClick={() => setRenaming(null)}
                    >
                      {t(($) => $.tiers.cancel)}
                    </Button>
                  </div>
                ) : deleting === entry.key ? (
                  <div className="flex flex-1 items-center justify-between gap-2">
                    <p className="text-body">
                      {t(($) => $.tiers.role_delete_confirm, {
                        name: roleLabel(entry.key),
                      })}
                    </p>
                    <div className="flex shrink-0 items-center gap-1">
                      <Button
                        size="sm"
                        variant="destructive"
                        data-testid="tier-role-delete-confirm"
                        disabled={pending}
                        onClick={() => void handleDelete(entry.key)}
                      >
                        {t(($) => $.tiers.remove_action)}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={pending}
                        onClick={() => setDeleting(null)}
                      >
                        {t(($) => $.tiers.cancel)}
                      </Button>
                    </div>
                  </div>
                ) : (
                  <>
                    <div className="min-w-0">
                      <p className="truncate text-sm font-medium">
                        {roleLabel(entry.key)}
                      </p>
                      <p className="truncate text-caption text-muted-foreground">
                        {entry.key}
                      </p>
                    </div>
                    <div className="flex shrink-0 items-center gap-1">
                      <Button
                        size="sm"
                        variant="ghost"
                        aria-label={t(($) => $.tiers.role_rename, {
                          name: roleLabel(entry.key),
                        })}
                        disabled={pending}
                        onClick={() => {
                          setRenaming(entry.key);
                          setRenameValue(entry.key);
                        }}
                      >
                        {t(($) => $.tiers.role_rename_action)}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        aria-label={t(($) => $.tiers.role_tag_remove, {
                          name: roleLabel(entry.key),
                        })}
                        disabled={pending}
                        onClick={() => setDeleting(entry.key)}
                      >
                        {t(($) => $.tiers.remove_action)}
                      </Button>
                    </div>
                  </>
                )}
              </li>
            ))
          )}
        </ul>
        <div className="flex items-end gap-2">
          <div className="flex-1 space-y-1.5">
            <Label htmlFor="tier-role-new">{t(($) => $.tiers.role_add)}</Label>
            <Input
              id="tier-role-new"
              data-testid="tier-role-new"
              value={newKey}
              placeholder={t(($) => $.tiers.role_key_placeholder)}
              onChange={(e) => setNewKey(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") void handleCreate();
              }}
            />
            {keyError && (
              <p className="text-caption text-destructive">{keyError}</p>
            )}
          </div>
          <Button disabled={pending} onClick={() => void handleCreate()}>
            {t(($) => $.tiers.role_add)}
          </Button>
        </div>
        {error && <p className="text-body text-destructive">{error}</p>}
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            {t(($) => $.tiers.cancel)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function TiersTab() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const { role } = useCurrentMember(wsId);
  const canManage = role === "owner" || role === "admin";

  // Tabs come from the workspace's role registry (M-498), so a custom role a
  // tier registered opens its own tab without a code change.
  const rolesQuery = useQuery(tierRolesOptions(wsId));
  const roles = useMemo(() => {
    const keys = (rolesQuery.data ?? []).map((entry) => entry.key);
    return keys.length > 0 ? keys : [...FALLBACK_TIER_ROLES];
  }, [rolesQuery.data]);
  const [selectedRole, setSelectedRole] = useState<string | null>(null);
  // A renamed or deleted role falls back to the first tab rather than to a
  // tab that no longer exists.
  const activeRole =
    selectedRole && roles.includes(selectedRole) ? selectedRole : roles[0]!;
  const roleLabels = useMemo<Record<string, string>>(
    () => ({
      architect: t(($) => $.tiers.role_architect),
      management: t(($) => $.tiers.role_management),
      engineer: t(($) => $.tiers.role_engineer),
    }),
    [t],
  );
  const roleLabel = (key: string) => roleLabels[key] ?? key;

  // Only this tab's tiers, in this role's own order: a tier in two roles
  // appears in both tabs, each with its own reorder arrows.
  const tiersQuery = useQuery(modelTiersOptions(wsId, activeRole));
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

  const [rolesOpen, setRolesOpen] = useState(false);
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingTier, setEditingTier] = useState<ModelTier | null>(null);
  // The tab "Add tier" was pressed on: the new tier starts in that role.
  const [editorRole, setEditorRole] = useState<string>(roles[0]!);
  const [deletingTier, setDeletingTier] = useState<ModelTier | null>(null);

  const openEditor = (tier: ModelTier | null) => {
    setEditingTier(tier);
    setEditorRole(activeRole);
    setEditorOpen(true);
  };

  // The arrows reorder inside the active tab only, so a tier's position in
  // another role is untouched.
  const handleMove = async (tier: ModelTier, direction: -1 | 1) => {
    const ids = tiers.map((row) => row.id);
    const from = ids.indexOf(tier.id);
    const to = from + direction;
    if (from < 0 || to < 0 || to >= ids.length) return;
    const next = [...ids];
    next.splice(from, 1);
    next.splice(to, 0, tier.id);
    try {
      await reorderTiers.mutateAsync({ ids: next, role: activeRole });
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
          <Tabs
            value={activeRole}
            onValueChange={(value) => setSelectedRole(String(value))}
            className="gap-0"
          >
            <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
              <TabsList
                aria-label={t(($) => $.tiers.role_tabs_label)}
                data-testid="tier-role-tabs"
              >
                {roles.map((entry) => (
                  <TabsTrigger
                    key={entry}
                    value={entry}
                    data-testid={`tier-role-tab-${entry}`}
                  >
                    {roleLabel(entry)}
                  </TabsTrigger>
                ))}
              </TabsList>
              {canManage && (
                <div className="flex items-center gap-2">
                  <Button
                    variant="outline"
                    data-testid="tier-manage-roles"
                    onClick={() => setRolesOpen(true)}
                  >
                    {t(($) => $.tiers.manage_roles)}
                  </Button>
                  <Button onClick={() => openEditor(null)}>
                    {t(($) => $.tiers.add_tier)}
                  </Button>
                </div>
              )}
            </div>
            <TabsContent value={activeRole} className="min-h-0">
              {tiersQuery.isLoading ? (
                <p className="px-4 py-6 text-body text-muted-foreground">
                  {t(($) => $.tiers.loading)}
                </p>
              ) : tiers.length === 0 ? (
                <div className="space-y-2 px-4 py-6">
                  <p className="font-medium">
                    {t(($) => $.tiers.empty_role_title, {
                      role: roleLabel(activeRole),
                    })}
                  </p>
                  <p className="text-body text-muted-foreground">
                    {t(($) => $.tiers.empty_description)}
                  </p>
                </div>
              ) : (
                <ul className="divide-y">
                  {tiers.map((tier, index) => {
                    const state = membersByTier.get(tier.id);
                    return (
                      <TierRow
                        key={tier.id}
                        tier={tier}
                        role={activeRole}
                        members={state?.members ?? []}
                        membersLoading={state?.loading ?? false}
                        position={index}
                        total={tiers.length}
                        canManage={canManage}
                        onEdit={() => openEditor(tier)}
                        onDelete={() => setDeletingTier(tier)}
                        onMove={(direction) => void handleMove(tier, direction)}
                        movePending={reorderTiers.isPending}
                      />
                    );
                  })}
                </ul>
              )}
            </TabsContent>
          </Tabs>
        </SettingsCard>
      </SettingsSection>

      {rolesOpen && (
        <TierRolesDialog
          wsId={wsId}
          roles={rolesQuery.data ?? []}
          roleLabel={roleLabel}
          onClose={() => setRolesOpen(false)}
        />
      )}

      {editorOpen && (
        <TierEditorDialog
          wsId={wsId}
          tier={editingTier}
          defaultRole={editorRole}
          roleOptions={roles}
          roleLabel={roleLabel}
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
  defaultRole,
  roleOptions,
  roleLabel,
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
  /** The tab "Add tier" was pressed on; a new tier starts in that role. */
  defaultRole: string;
  /** The workspace registry, so a custom role is assignable too (M-498). */
  roleOptions: string[];
  roleLabel: (key: string) => string;
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
  const [roles, setRoles] = useState<string[]>(
    tier?.roles ?? (defaultRole ? [defaultRole] : []),
  );
  const [roleAutoPick, setRoleAutoPick] = useState<Record<string, boolean>>(
    () => Object.fromEntries(
      (tier?.roles ?? (defaultRole ? [defaultRole] : [])).map((role) => [
        role,
        tier?.role_settings?.find((setting) => setting.role === role)?.auto_pick ??
          tier?.auto_pick ??
          !(tier?.manual_only ?? false),
      ]),
    ),
  );
  const rolePositions = Object.fromEntries(
    (tier?.role_settings ?? []).map((setting) => [setting.role, setting.idx]),
  );
  const nextRolePosition = Math.max(-1, ...Object.values(rolePositions)) + 1;
  const positionForRole = (role: string) => {
    if (rolePositions[role] !== undefined) return rolePositions[role]!;
    const before = roles.slice(0, roles.indexOf(role));
    return nextRolePosition + before.filter((item) => rolePositions[item] === undefined).length;
  };
  const [quotaKinds, setQuotaKinds] = useState<string[]>(
    tier?.quota_kinds ?? [],
  );
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
      role_settings: roles.map((role) => ({
        role,
        auto_pick: roleAutoPick[role] ?? tier?.auto_pick ?? true,
        idx: positionForRole(role),
      })),
      // Compatibility for older servers and clients: aggregate enabled roles.
      auto_pick: roles.some((role) => roleAutoPick[role] ?? tier?.auto_pick ?? true),
      // M-497: is_free is no longer an input — the server derives it from
      // quota_kinds, so the dialog has no Free tier switch.
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
  const quotaLabel = (kind: string) =>
    quotaOptions.find((entry) => entry.kind === kind)?.label ?? kind;
  // Selection order is kept: a newly ticked kind joins the end of the tags.
  const toggleQuota = (kind: string, checked: boolean) =>
    setQuotaKinds((prev) =>
      checked
        ? [...prev.filter((k) => k !== kind), kind]
        : prev.filter((k) => k !== kind),
    );

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

          {/* M-498: Enabled sits at the top next to the name. Everything else
              is inside the fieldset below, which the browser disables and the
              class grays out while the tier is off; Enabled, Cancel and Save
              stay live. */}
          <div className="grid grid-cols-2 items-start gap-4 md:grid-cols-4">
            <div className="col-span-2 space-y-1.5 md:col-span-3">
              <Label htmlFor="tier-name">{t(($) => $.tiers.name_label)}</Label>
              <Input
                id="tier-name"
                value={name}
                disabled={!enabled}
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
              <Label htmlFor="tier-enabled">
                {t(($) => $.tiers.enabled_label)}
              </Label>
              <div className="flex h-9 items-center">
                <Switch
                  id="tier-enabled"
                  data-testid="tier-enabled-switch"
                  checked={enabled}
                  onCheckedChange={setEnabled}
                />
              </div>
              <p className="text-caption text-muted-foreground">
                {t(($) => $.tiers.enabled_hint)}
              </p>
            </div>
          </div>

          <fieldset
            disabled={!enabled}
            data-testid="tier-editor-fields"
            className={cn("space-y-5", !enabled && "opacity-60")}
          >
          <div className="grid gap-4 md:grid-cols-3">
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

          <div className="space-y-4">
            {/* M-543: the same tag multi-select the quota kinds use. Roles
                come from the workspace registry, so a custom role is
                assignable without a code change. */}
            <div className="space-y-1.5" data-testid="tier-roles-select">
              <Label>{t(($) => $.tiers.roles_label)}</Label>
              <div className="flex flex-wrap items-center gap-1.5">
                {roles.length === 0 ? (
                  <span
                    data-testid="tier-roles-none"
                    className="text-sm text-muted-foreground"
                  >
                    {t(($) => $.tiers.roles_none)}
                  </span>
                ) : (
                  roles.map((entry) => (
                    <Badge
                      key={entry}
                      variant="secondary"
                      data-testid={`tier-role-tag-${entry}`}
                      className="gap-1 pr-1"
                    >
                      {roleLabel(entry)}
                      <button
                        type="button"
                        aria-label={t(($) => $.tiers.role_tag_remove, {
                          name: roleLabel(entry),
                        })}
                        className="rounded-sm p-0.5 hover:bg-surface-hover"
                        onClick={() =>
                          setRoles((prev) => prev.filter((r) => r !== entry))
                        }
                      >
                        <X aria-hidden="true" className="size-3" />
                      </button>
                    </Badge>
                  ))
                )}
                <Popover>
                  <PopoverTrigger
                    render={
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        data-testid="tier-roles-add"
                        aria-label={t(($) => $.tiers.roles_select)}
                      >
                        <Plus aria-hidden="true" className="size-3.5" />
                        {t(($) => $.tiers.roles_add)}
                      </Button>
                    }
                  />
                  <PopoverContent align="start" className="w-60 p-1">
                    {roleOptions.length === 0 ? (
                      <p className="px-2 py-1.5 text-caption text-muted-foreground">
                        {t(($) => $.tiers.roles_empty)}
                      </p>
                    ) : (
                      roleOptions.map((entry) => (
                        <label
                          key={entry}
                          htmlFor={`tier-role-${entry}`}
                          className="flex items-center gap-2 rounded-sm px-2 py-1.5 text-sm hover:bg-surface-hover"
                        >
                          <Checkbox
                            id={`tier-role-${entry}`}
                            checked={roles.includes(entry)}
                            onCheckedChange={(checked) => {
                              if (checked === true) {
                                setRoles((prev) => [...prev.filter((r) => r !== entry), entry]);
                                setRoleAutoPick((prev) => ({
                                  ...prev,
                                  [entry]: tier?.auto_pick ?? true,
                                }));
                              } else {
                                setRoles((prev) => prev.filter((r) => r !== entry));
                                setRoleAutoPick((prev) => {
                                  const next = { ...prev };
                                  delete next[entry];
                                  return next;
                                });
                              }
                            }}
                          />
                          {roleLabel(entry)}
                        </label>
                      ))
                    )}
                  </PopoverContent>
                </Popover>
              </div>
              <p className="text-caption text-muted-foreground">
                {t(($) => $.tiers.roles_hint)}
              </p>
            </div>
            <div className="space-y-1.5" data-testid="tier-role-auto-pick-rows">
              <Label>{t(($) => $.tiers.auto_pick_label)}</Label>
              {roles.length === 0 ? (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.tiers.roles_none)}
                </p>
              ) : (
                <div className="space-y-1">
                  {roles.map((role) => {
                    const autoPick = roleAutoPick[role] ?? tier?.auto_pick ?? true;
                    const settingId = `tier-role-auto-pick-${role}`;
                    return (
                      <div
                        key={role}
                        data-testid={`tier-role-setting-${role}`}
                        className="flex items-center gap-2 rounded-md border px-2 py-1.5"
                      >
                        <Label htmlFor={settingId} className="min-w-0 flex-1 truncate">
                          {roleLabel(role)}
                        </Label>
                        <span className="text-caption text-muted-foreground">
                          #{positionForRole(role)}
                        </span>
                        <Switch
                          id={settingId}
                          data-testid={`tier-role-auto-pick-switch-${role}`}
                          aria-label={`${t(($) => $.tiers.auto_pick_label)} ${roleLabel(role)}`}
                          checked={autoPick}
                          onCheckedChange={(checked) =>
                            setRoleAutoPick((prev) => ({ ...prev, [role]: checked }))
                          }
                        />
                        <span className="text-caption text-muted-foreground">
                          {autoPick
                            ? t(($) => $.tiers.auto_pick_badge)
                            : t(($) => $.tiers.auto_pick_badge_manual)}
                        </span>
                      </div>
                    );
                  })}
                </div>
              )}
              <p className="text-caption text-muted-foreground">
                {t(($) => $.tiers.auto_pick_hint)}
              </p>
            </div>
          </div>

          {/* M-498: a multi-select whose ticks render as tags, instead of a
              row of seven checkboxes. Empty reads "None". */}
          <div className="space-y-1.5" data-testid="tier-quota-select">
            <Label>{t(($) => $.tiers.quota_label)}</Label>
            <div className="flex flex-wrap items-center gap-1.5">
              {quotaKinds.length === 0 ? (
                <span
                  data-testid="tier-quota-none"
                  className="text-sm text-muted-foreground"
                >
                  {t(($) => $.tiers.quota_none)}
                </span>
              ) : (
                quotaKinds.map((kind) => (
                  <Badge
                    key={kind}
                    variant="secondary"
                    data-testid={`tier-quota-tag-${kind}`}
                    className="gap-1 pr-1"
                  >
                    {quotaLabel(kind)}
                    <button
                      type="button"
                      aria-label={t(($) => $.tiers.quota_remove, {
                        name: quotaLabel(kind),
                      })}
                      className="rounded-sm p-0.5 hover:bg-surface-hover"
                      onClick={() => toggleQuota(kind, false)}
                    >
                      <X aria-hidden="true" className="size-3" />
                    </button>
                  </Badge>
                ))
              )}
              <Popover>
                <PopoverTrigger
                  render={
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      data-testid="tier-quota-add"
                      aria-label={t(($) => $.tiers.quota_select)}
                    >
                      <Plus aria-hidden="true" className="size-3.5" />
                      {t(($) => $.tiers.quota_add)}
                    </Button>
                  }
                />
                <PopoverContent align="start" className="w-60 p-1">
                  {quotaOptions.map((entry) => (
                    <label
                      key={entry.kind}
                      htmlFor={`tier-quota-${entry.kind}`}
                      className="flex items-center gap-2 rounded-sm px-2 py-1.5 text-sm hover:bg-surface-hover"
                    >
                      <Checkbox
                        id={`tier-quota-${entry.kind}`}
                        checked={quotaKinds.includes(entry.kind)}
                        onCheckedChange={(checked) =>
                          toggleQuota(entry.kind, checked === true)
                        }
                      />
                      {entry.label}
                    </label>
                  ))}
                </PopoverContent>
              </Popover>
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
              {/* M-543: the badge already says "advisory"; a second label with
                  the same word above it read as a duplicate (M-498). */}
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

          </fieldset>

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
 * One mapping line. The harness is a selector (M-545): a free-text provider
 * said nothing the resolver could act on. The model list comes from the
 * runtime catalog — the bound runtime, or the one runtime whose provider is
 * the chosen harness (discovery stays optional: the field falls back to free
 * text when discovery is offline or the model is custom).
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
  // A harness that resolves to exactly one workspace runtime gives that
  // runtime's catalog, so the model is a picker even before a runtime is bound.
  // Ambiguous providers (several machines) stay free text until one is picked.
  const catalogRuntimeId = useMemo(() => {
    if (row.runtimeId) return row.runtimeId;
    const matches = runtimes.filter((r) => r.provider === row.provider.trim());
    return matches.length === 1 ? matches[0]!.id : "";
  }, [row.runtimeId, row.provider, runtimes]);
  const modelsQuery = useQuery({
    ...runtimeModelsOptions(catalogRuntimeId || null),
    enabled: catalogRuntimeId !== "",
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
          <select
            id={`member-provider-${row.key}`}
            value={row.provider}
            onChange={(e) => onChange({ provider: e.target.value })}
            className="h-8 w-full min-w-0 truncate rounded-md border bg-background px-2 text-sm"
          >
            <option value="" disabled>
              {t(($) => $.tiers.member_harness_select)}
            </option>
            {harnessOptions(row.provider, runtimes).map((harness) => (
              <option key={harness} value={harness}>
                {harness}
              </option>
            ))}
          </select>
        </div>
        <div className="col-start-2 row-start-2 min-w-0 space-y-1 @[46rem]:col-start-3 @[46rem]:row-start-1">
          <Label htmlFor={`member-model-${row.key}`} className="text-caption">
            {t(($) => $.tiers.member_model_label)}
          </Label>
          {catalogRuntimeId !== "" && !modelCustom && !modelsQuery.isError ? (
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
