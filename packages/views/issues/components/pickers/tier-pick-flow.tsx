"use client";

import { useCallback, useMemo, useState } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import type { ModelTier, ModelTierMember } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { runtimeDisplayLabel } from "@multica/core/runtimes";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { modelTierMembersOptions } from "@multica/core/workspace/queries";
import { useT } from "../../../i18n";
import { PickerItem, PickerSection } from "./property-picker";
import { buildTierPickTree, tierPickModelCount } from "./tier-pick-tree";

/**
 * Fetches the member lists and runtime names a tier flow needs. The tier list
 * response intentionally carries only profiles (M-481), so one cached member
 * query per visible tier is the established pattern (tiers-tab.tsx does the
 * same). Callers pass the already agent-filtered tiers.
 */
export function useTierPickData(tiers: readonly ModelTier[]) {
  const wsId = useWorkspaceId();
  const memberQueries = useQueries({
    queries: tiers.map((tier) => modelTierMembersOptions(wsId, tier.id)),
  });
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));

  const membersByTier = useMemo(() => {
    const map = new Map<string, ModelTierMember[]>();
    tiers.forEach((tier, index) => {
      map.set(tier.id, memberQueries[index]?.data ?? []);
    });
    return map;
  }, [tiers, memberQueries]);

  const runtimeById = useMemo(
    () => new Map(runtimes.map((runtime) => [runtime.id, runtime])),
    [runtimes],
  );
  const runtimeLabel = useCallback(
    (runtimeId: string) => {
      const runtime = runtimeById.get(runtimeId);
      return runtime ? runtimeDisplayLabel(runtime) : undefined;
    },
    [runtimeById],
  );

  return { membersByTier, runtimeLabel };
}

/**
 * M-493: the shared model → harness → runtime step flow for picking one tier.
 *
 * The old flat list showed `#tier · model`, which hides that one model can be
 * served by several harnesses and that a tier is now a profile of ordered
 * members. Here the first step picks the model, the second the harness
 * (provider), and the third the runtime — the leaf renders the tier config
 * (`#name`, harness/model, runtime) so the caller only needs the tier id.
 *
 * The component is presentational: callers supply the already agent-filtered
 * tier list, their member map, and what to do with the pick. `onAuto` renders
 * the resolver row; omit it when Auto is not a valid choice.
 */
export function TierPickFlow({
  tiers,
  membersByTier,
  runtimeLabel,
  selectedTierId,
  onPick,
  onAuto,
  autoLabel,
  selectedModelKey,
  onModelChange,
}: {
  tiers: readonly ModelTier[];
  /** Override the member data (e.g. an admin view that includes disabled rows). */
  membersByTier?: ReadonlyMap<string, readonly ModelTierMember[]>;
  /** Override runtime labels; otherwise they are fetched from the workspace. */
  runtimeLabel?: (runtimeId: string) => string | undefined;
  selectedTierId?: string | null;
  onPick: (tierId: string) => void;
  /** When supplied, an "Auto (resolver picks)" row is rendered first. */
  onAuto?: () => void;
  autoLabel?: string;
  /** Optional controlled model step (used when the flow is embedded in a
   *  larger picker that wants to remember where the user was). */
  selectedModelKey?: string | null;
  onModelChange?: (modelKey: string | null) => void;
}) {
  const { t } = useT("issues");
  const autoData = useTierPickData(tiers);
  const effectiveMembers = membersByTier ?? autoData.membersByTier;
  const effectiveRuntimeLabel = runtimeLabel ?? autoData.runtimeLabel;
  const tree = useMemo(
    () => buildTierPickTree(tiers, effectiveMembers, { runtimeLabel: effectiveRuntimeLabel }),
    [tiers, effectiveMembers, effectiveRuntimeLabel],
  );
  const [localModel, setLocalModel] = useState<string | null>(null);
  const [harnessKey, setHarnessKey] = useState<string | null>(null);

  const model = selectedModelKey === undefined ? localModel : selectedModelKey;
  const setModel = (next: string | null) => {
    if (selectedModelKey === undefined) setLocalModel(next);
    else onModelChange?.(next);
    setHarnessKey(null);
  };

  const currentModel = tree.find((candidate) => candidate.key === model);
  const harness = currentModel?.harnesses.find((candidate) => candidate.key === harnessKey);

  if (tree.length === 0) {
    return (
      <div className="px-3 py-4 text-center text-caption text-muted-foreground">
        {t(($) => $.pickers.tier.tier_none_hint)}
      </div>
    );
  }

  if (!currentModel) {
    return (
      <>
        <PickerSection label={t(($) => $.pickers.tier.pick_model)}>
          {onAuto && (
            <PickerItem selected={!selectedTierId} onClick={onAuto}>
              <span className="truncate">{autoLabel ?? t(($) => $.pickers.tier.tier_auto)}</span>
            </PickerItem>
          )}
          {tree.map((candidate) => (
            <PickerItem
              key={candidate.key}
              selected={false}
              onClick={() => setModel(candidate.key)}
              tooltip={t(($) => $.pickers.tier.pick_model_count, {
                count: tierPickModelCount(candidate),
              })}
            >
              <span className="block min-w-0 truncate">{candidate.label}</span>
            </PickerItem>
          ))}
        </PickerSection>
      </>
    );
  }

  if (!harness) {
    return (
      <>
        <PickerItem selected={false} onClick={() => setModel(null)}>
          <span className="text-muted-foreground">{t(($) => $.pickers.tier.back_to_models)}</span>
        </PickerItem>
        <PickerSection label={t(($) => $.pickers.tier.pick_harness)}>
          {currentModel.harnesses.map((candidate) => (
            <PickerItem
              key={candidate.key}
              selected={false}
              onClick={() => setHarnessKey(candidate.key)}
            >
              <span className="block min-w-0 truncate">{candidate.label}</span>
            </PickerItem>
          ))}
        </PickerSection>
      </>
    );
  }

  return (
    <>
      <PickerItem selected={false} onClick={() => setHarnessKey(null)}>
        <span className="text-muted-foreground">{currentModel.label}</span>
      </PickerItem>
      <PickerSection label={t(($) => $.pickers.tier.pick_runtime)}>
        {harness.entries.map((entry) => (
          <PickerItem
            key={entry.key}
            selected={selectedTierId === entry.tier.id}
            onClick={() => onPick(entry.tier.id)}
            tooltip={`${entry.provider} · ${entry.model}`}
          >
            <span className="block min-w-0 flex-1 text-left">
              <span className="block truncate text-label font-medium">{entry.runtimeLabel}</span>
              <span className="mt-0.5 block truncate font-mono text-micro text-muted-foreground">
                {entry.tierLabel} · {entry.provider}/{entry.model}
              </span>
            </span>
          </PickerItem>
        ))}
      </PickerSection>
    </>
  );
}
