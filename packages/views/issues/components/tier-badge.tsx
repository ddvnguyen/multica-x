"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Layers } from "lucide-react";
import { modelTiersOptions } from "@multica/core/workspace/queries";
import { useCurrentWorkspace } from "@multica/core/paths";
import { Badge } from "@multica/ui/components/ui/badge";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { useT } from "../../i18n";

/**
 * TierBadge renders the M-359 run record: which workspace tier stamped a
 * task, whether the sender pinned it (explicit `@agent#tier` / picker pick)
 * or the idx-ranked resolver chose it (auto), and whether the row is parked
 * waiting on that tier's capacity. Renders nothing when the task carries no
 * tier stamp (legacy single-runtime run) or tiers fail to load — the name
 * falls back to the id prefix so an unknown tier never renders blank.
 */
export function TierBadge({
  tierId,
  explicit,
  parked,
}: {
  tierId?: string;
  explicit?: boolean;
  parked?: boolean;
}) {
  const { t } = useT("issues");
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const tiersQuery = useQuery(modelTiersOptions(wsId));
  const tiers = useMemo(
    () => (tiersQuery.isSuccess ? (tiersQuery.data ?? []) : []),
    [tiersQuery.data, tiersQuery.isSuccess],
  );
  if (!tierId) return null;
  const name = tiers.find((tier) => tier.id === tierId)?.name ?? tierId.slice(0, 8);
  const detail = parked
    ? t(($) => $.execution_log.tier_parked_detail, { name })
    : explicit
      ? t(($) => $.execution_log.tier_explicit_detail, { name })
      : t(($) => $.execution_log.tier_auto_detail, { name });
  const short = parked
    ? t(($) => $.execution_log.tier_parked_short, { name })
    : explicit
      ? t(($) => $.execution_log.tier_explicit_short, { name })
      : name;
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Badge variant={parked ? "secondary" : "outline"} className="gap-1">
            <Layers className="size-3" aria-hidden />
            {short}
          </Badge>
        }
      />
      <TooltipContent>{detail}</TooltipContent>
    </Tooltip>
  );
}
