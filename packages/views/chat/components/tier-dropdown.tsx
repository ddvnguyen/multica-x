"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useChatStore } from "@multica/core/chat";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentDetailOptions, modelTiersOptions } from "@multica/core/workspace/queries";
import {
  PickerSection,
  PropertyPicker,
  TierPickFlow,
} from "../../issues/components/pickers";
import { useT } from "../../i18n";

/**
 * M-359 tier dropdown for the next chat turn, updated in M-493: Auto
 * (resolver) or one tier chosen through the model → harness → runtime flow.
 * Only tiers the active agent supports are offered; stored per agent in the
 * chat store and sent as `tier_id` with the next message.
 */
export function TierDropdown({ agentId }: { agentId: string | null }) {
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const { data: tiers = [] } = useQuery(modelTiersOptions(wsId));
  const { data: agent } = useQuery(agentDetailOptions(wsId, agentId ?? ""));
  const tierId = useChatStore((s) => (agentId ? ((s.selectedTierIds ?? {})[agentId] ?? null) : null));
  const setSelectedTierId = useChatStore((s) => s.setSelectedTierId);
  const [open, setOpen] = useState(false);

  const eligible = useMemo(() => {
    const enabled = tiers.filter((tier) => tier.enabled);
    if (!agent?.tier_ids?.length) return enabled;
    const allowed = new Set(agent.tier_ids);
    return enabled.filter((tier) => allowed.has(tier.id));
  }, [tiers, agent?.tier_ids]);

  if (!agentId || eligible.length === 0) return null;
  const label = tierId
    ? `#${eligible.find((tier) => tier.id === tierId)?.name ?? tierId.slice(0, 8)}`
    : "Auto";
  const pick = (next: string | null) => {
    setSelectedTierId(agentId, next);
    setOpen(false);
  };

  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-64"
      align="start"
      side="top"
      trigger={
        <span className="text-caption text-muted-foreground max-w-24 truncate">{label}</span>
      }
    >
      <PickerSection label={t(($) => $.window.tier.tier_for_next_turn)}>
        <TierPickFlow
          tiers={eligible}
          selectedTierId={tierId}
          onPick={pick}
          onAuto={() => pick(null)}
        />
      </PickerSection>
    </PropertyPicker>
  );
}
