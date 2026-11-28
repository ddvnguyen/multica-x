"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useChatStore } from "@multica/core/chat";
import { useWorkspaceId } from "@multica/core/hooks";
import { isRoleAgent, useAgentRoleTypeEnabled } from "@multica/core/agents";
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
 * M-633 made this Role-only: renders nothing for plain Agents; a stored
 * `selectedTierIds` entry for a plain Agent is ignored at send time.
 * The list is scoped to the Role's tier_role table.
 */
export function TierDropdown({ agentId }: { agentId: string | null }) {
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const roleTypeEnabled = useAgentRoleTypeEnabled(wsId);
  const { data: agent } = useQuery(agentDetailOptions(wsId, agentId ?? ""));
  const isRole = roleTypeEnabled && isRoleAgent(agent);
  const tierRole = isRole ? agent?.tier_role || undefined : undefined;
  // A Role reads its own role table; a Role with no tier_role set falls
  // back to the unscoped list. Plain Agents never reach the render below.
  const { data: tiers = [] } = useQuery(modelTiersOptions(wsId));
  const { data: roleTiers } = useQuery({
    ...modelTiersOptions(wsId, tierRole),
    enabled: !!tierRole,
  });
  const tierId = useChatStore((s) => (agentId ? ((s.selectedTierIds ?? {})[agentId] ?? null) : null));
  const setSelectedTierId = useChatStore((s) => s.setSelectedTierId);
  const [open, setOpen] = useState(false);

  const eligible = useMemo(() => {
    const source = tierRole ? (roleTiers ?? []) : tiers;
    return source.filter((tier) => tier.enabled);
  }, [tiers, roleTiers, tierRole]);

  // M-633: Role-only. Plain Agents message directly — no tier UI.
  if (!agentId || !isRole || eligible.length === 0) return null;
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
