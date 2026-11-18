"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useChatStore } from "@multica/core/chat";
import { useWorkspaceId } from "@multica/core/hooks";
import { modelTiersOptions } from "@multica/core/workspace/queries";
import {
  PickerItem,
  PickerSection,
  PropertyPicker,
} from "../../issues/components/pickers/property-picker";
import { useT } from "../../i18n";

/**
 * M-359 tier dropdown for the next chat turn: Auto (resolver) or one pinned
 * tier. Stored per agent in the chat store and sent as `tier_id` with the
 * next message (unknown/disabled → 409 tier_unavailable, full/offline →
 * queued parked). Sessions are agent-bound, so no per-session key needed.
 */
export function TierDropdown({ agentId }: { agentId: string | null }) {
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const { data: tiers = [] } = useQuery(modelTiersOptions(wsId));
  const tierId = useChatStore((s) => (agentId ? ((s.selectedTierIds ?? {})[agentId] ?? null) : null));
  const setSelectedTierId = useChatStore((s) => s.setSelectedTierId);
  const [open, setOpen] = useState(false);
  const enabled = tiers.filter((tier) => tier.enabled);
  if (!agentId || enabled.length === 0) return null;
  const label = tierId
    ? `#${enabled.find((tier) => tier.id === tierId)?.name ?? tierId.slice(0, 8)}`
    : "Auto";
  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-56"
      align="start"
      side="top"
      trigger={
        <span className="text-caption text-muted-foreground max-w-24 truncate">{label}</span>
      }
    >
      <PickerSection label={t(($) => $.window.tier.tier_for_next_turn)}>
        <PickerItem
          selected={tierId === null}
          onClick={() => {
            setSelectedTierId(agentId, null);
            setOpen(false);
          }}
        >
          <span className="truncate">{t(($) => $.window.tier.tier_auto)}</span>
        </PickerItem>
        {enabled.map((tier) => (
          <PickerItem
            key={tier.id}
            selected={tierId === tier.id}
            onClick={() => {
              setSelectedTierId(agentId, tier.id);
              setOpen(false);
            }}
          >
            <span className="truncate">
              #{tier.name}
              {tier.model ? <span className="text-muted-foreground"> · {tier.model}</span> : null}
            </span>
          </PickerItem>
        ))}
      </PickerSection>
    </PropertyPicker>
  );
}

