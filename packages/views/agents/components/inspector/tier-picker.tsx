"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, ChevronDown, Layers, Loader2 } from "@multica/ui/icons";
import { useCurrentWorkspace } from "@multica/core/paths";
import type { ModelTier } from "@multica/core/types";
import { modelTiersOptions } from "@multica/core/workspace/queries";
import {
  PickerItem,
  PropertyPicker,
} from "../../../issues/components/pickers";
import { useT } from "../../../i18n";

/**
 * Tier multi-select for the agent inspector (M-351). Replaces the model
 * picker when `use_tiers` is on: the selected tier ids are stored on the
 * agent and resolved per task at enqueue (first enabled tier with a free
 * provider slot wins). A tier deleted or disabled after selection renders
 * as "unavailable" — never silently dropped.
 */
export function TierPicker({
  value,
  agentRole = "",
  canEdit = true,
  onChange,
}: {
  /** Selected tier ids. */
  value: string[];
  /**
   * Agent role for match display (M-423). A role-set agent only resolves
   * tiers carrying its role; a legacy ("") agent keeps all selections.
   * This picker never filters — mismatches render a badge but stay
   * selectable, since the server enforces the match at enqueue.
   */
  agentRole?: string;
  /** When false, render a static read-only display and skip the popover. */
  canEdit?: boolean;
  onChange: (next: string[]) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const [open, setOpen] = useState(false);
  const tiersQuery = useQuery(modelTiersOptions(wsId));
  const tiers = useMemo<ModelTier[]>(
    () => (tiersQuery.isSuccess ? (tiersQuery.data ?? []) : []),
    [tiersQuery.data, tiersQuery.isSuccess],
  );
  const byId = useMemo(() => new Map(tiers.map((tier) => [tier.id, tier])), [tiers]);

  const label = useMemo(() => {
    if (value.length === 0) return t(($) => $.inspector.tiers_empty);
    return value.map((id) => byId.get(id)?.name ?? id.slice(0, 8)).join(", ");
  }, [byId, t, value]);
  const hasUnavailable = value.some((id) => {
    const tier = byId.get(id);
    return tier == null || tier.enabled === false;
  });

  if (!canEdit) {
    return (
      <span className="text-sm text-muted-foreground">
        <Layers className="mr-1 inline size-3.5" aria-hidden />
        {label}
      </span>
    );
  }

  const toggle = (id: string) => {
    const next = value.includes(id) ? value.filter((v) => v !== id) : [...value, id];
    void onChange(next);
  };

  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      trigger={
        tiersQuery.isPending ? (
          <Loader2 className="size-3.5 animate-spin" aria-hidden />
        ) : (
          <>
            <Layers className="size-3.5 opacity-60" aria-hidden />
            <span className="max-w-44 truncate">{label}</span>
            <ChevronDown className="size-3.5 opacity-60" aria-hidden />
          </>
        )
      }
    >
      {tiers.length === 0 ? (
        <div className="px-3 py-4 text-center text-caption text-muted-foreground">
          {t(($) => $.inspector.tiers_none_hint)}
        </div>
      ) : (
        tiers.map((tier) => {
          const selected = value.includes(tier.id);
          return (
            <PickerItem
              key={tier.id}
              selected={selected}
              onClick={() => toggle(tier.id)}
              tooltip={`${tier.provider} · ${tier.model}`}
            >
              <span className="block min-w-0 flex-1 text-left">
                <span className="block truncate text-label font-medium">
                  {tier.name}
                  {!tier.enabled && (
                    <span className="ml-1.5 text-micro text-muted-foreground">
                      {t(($) => $.inspector.tiers_disabled_badge)}
                    </span>
                  )}
                  {agentRole !== "" &&
                    !(tier.roles ?? []).includes(agentRole) && (
                      <span className="ml-1.5 text-micro text-amber-600 dark:text-amber-400">
                        {t(($) => $.inspector.tiers_role_mismatch_badge)}
                      </span>
                    )}
                  {tier.manual_only && (
                    <span className="ml-1.5 text-micro text-muted-foreground">
                      {t(($) => $.inspector.tiers_manual_badge)}
                    </span>
                  )}
                </span>
                <span className="mt-0.5 block truncate font-mono text-micro leading-snug text-muted-foreground">
                  {tier.provider} · {tier.model}
                </span>
              </span>
              {selected && <Check className="size-4 shrink-0" aria-hidden />}
            </PickerItem>
          );
        })
      )}
      {hasUnavailable && (
        <div className="px-3 py-2 text-caption text-amber-600 dark:text-amber-400">
          {t(($) => $.inspector.tiers_unavailable_note)}
        </div>
      )}
    </PropertyPicker>
  );
}
