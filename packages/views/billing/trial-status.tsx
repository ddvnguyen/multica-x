"use client";

import { Hourglass, Sparkles } from "@multica/ui/icons";
import { useWorkspaceTrial } from "@multica/core/billing";
import { useFeatureEnabled } from "@multica/core/config";
import { BILLING_WORKSPACE_SUBSCRIPTIONS_FLAG } from "@multica/core/feature-flags";
import { useWorkspacePaths } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { AppLink } from "../navigation";
import { BOTTOM_BAR_ITEM_CLASS } from "../layout/bottom-bar-styles";

/**
 * Whether `TrialStatus` renders anything for this workspace. Callers that
 * decide whether the status bar has content at all — the bar disappears when
 * the tab strip hosts the chat (M-413) — need the answer without rendering
 * the link. Same query, so the two stay in step.
 */
export function useTrialStatusVisible(wsId: string | undefined): boolean {
  const enabled = useFeatureEnabled(BILLING_WORKSPACE_SUBSCRIPTIONS_FLAG, false);
  const trial = useWorkspaceTrial(wsId ?? "", enabled);
  if (!wsId) return false;
  return (
    trial.kind === "active" ||
    (trial.kind === "ended" && (!trial.confirmed || trial.recent))
  );
}

/**
 * The trial's place in the status bar (MUL-9485): a countdown while the
 * Business trial runs, a warning for its last three days, and an upgrade
 * entry for a week after it ends. Every member sees it; it opens Billing.
 */
export function TrialStatus({ wsId }: { wsId: string }) {
  const { t } = useT("billing");
  const paths = useWorkspacePaths();
  const enabled = useFeatureEnabled(BILLING_WORKSPACE_SUBSCRIPTIONS_FLAG, false);
  const trial = useWorkspaceTrial(wsId, enabled);
  const href = `${paths.settings()}?tab=billing`;

  if (trial.kind === "active") {
    const Icon = trial.warning ? Hourglass : Sparkles;
    return (
      <AppLink
        href={href}
        data-slot="trial-status"
        data-warning={trial.warning ? "" : undefined}
        aria-label={t(($) => $.trial.status.active_label, { count: trial.daysLeft })}
        className={cn(
          BOTTOM_BAR_ITEM_CLASS,
          trial.warning
            ? "bg-warning/10 text-warning hover:bg-warning/15 hover:text-warning"
            : "bg-brand/10 text-brand hover:bg-brand/15 hover:text-brand",
        )}
      >
        <Icon aria-hidden="true" />
        <span className="font-medium text-foreground">{t(($) => $.trial.status.active)}</span>
        <span className="truncate tabular-nums">
          {t(($) => $.trial.status.days_left, { count: trial.daysLeft })}
        </span>
      </AppLink>
    );
  }

  // Past the end on this clock, but Cloud has not confirmed it: the workspace
  // may have subscribed, so say it is ending rather than that it is on Free.
  if (trial.kind === "ended" && !trial.confirmed) {
    return (
      <AppLink
        href={href}
        data-slot="trial-status"
        data-warning=""
        aria-label={t(($) => $.trial.status.ending_label)}
        className={cn(
          BOTTOM_BAR_ITEM_CLASS,
          "bg-warning/10 text-warning hover:bg-warning/15 hover:text-warning",
        )}
      >
        <Hourglass aria-hidden="true" />
        <span className="font-medium text-foreground">{t(($) => $.trial.status.active)}</span>
        <span className="truncate">{t(($) => $.trial.status.ending)}</span>
      </AppLink>
    );
  }

  if (trial.kind === "ended" && trial.recent) {
    return (
      <AppLink
        href={href}
        data-slot="trial-status"
        aria-label={t(($) => $.trial.status.ended_label)}
        className={BOTTOM_BAR_ITEM_CLASS}
      >
        <Sparkles aria-hidden="true" className="text-brand" />
        <span className="font-medium text-foreground">{t(($) => $.trial.status.free)}</span>
        <span>{t(($) => $.trial.status.upgrade)}</span>
      </AppLink>
    );
  }

  return null;
}
