import { useCallback, useEffect, useRef } from "react";
import { toast, useWSEvent, useIntentNavigate } from "../fork";
import type { InboxItem } from "../fork";
/**
 * In-island toast for ask/plan arrivals (M-362).
 *
 * The fork's `handleInboxNew` covers the OS/browser banner when the app is
 * unfocused, but an Owner staring at an open dsh panel gets badge-only: the
 * island mounted no `Toaster`, so `sonner` calls rendered nowhere. This
 * reporter subscribes to the live `inbox:new` the island already receives and
 * toasts `agent_question` / `plan_review` rows only — every other type (a
 * comment, a status change) is inbox business, not an interruption.
 *
 * Shown once per item id: reconnect replays re-fire `inbox:new`, and the
 * ref-set drops the second toast. The action routes to the issue the row
 * points at; a row with neither issue nor id toasts without an action rather
 * than routing nowhere. No browser Notification permission is requested here
 * — that path stays behind the fork's gesture-gated setting.
 */
const ASK_PLAN_TOAST_TYPES: Record<string, true> = { agent_question: true, plan_review: true };

export function AskPlanToastReporter(): null {
  const intentNavigate = useIntentNavigate();
  const seen = useRef(new Set<string>());

  const onItem = useCallback(
    (payload: unknown) => {
      const item = (payload as { item?: InboxItem } | null)?.item;
      if (!item || !ASK_PLAN_TOAST_TYPES[item.type]) return;
      const key = item.id;
      if (!key || seen.current.has(key)) return;
      seen.current.add(key);
      const target = item.issue_id ?? item.id;
      const label = item.type === "plan_review" ? "Review plan" : "Answer";
      if (!target) {
        toast(item.title, { description: item.body ?? undefined });
        return;
      }
      toast(item.title, {
        description: item.body ?? undefined,
        action: {
          label,
          onClick: () => intentNavigate(`issues/${target}`, "push"),
        },
      });
    },
    [intentNavigate],
  );

  useWSEvent("inbox:new", onItem);

  // The set only grows while the island lives; cap it so a long-lived panel
  // cannot grow it without bound.
  useEffect(() => {
    if (seen.current.size > 500) seen.current.clear();
  });

  return null;
}
