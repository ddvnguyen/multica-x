"use client";

/**
 * Back and Forward for the tab strip (M-413 round 2).
 *
 * The strip is the browser chrome of the SPA, so it carries the two buttons a
 * browser puts next to the tabs. They step the active tab's virtual history
 * through the navigation adapter — the same calls the keyboard chords and the
 * mouse side buttons make — so every entry point moves through one stack.
 *
 * The dsh island's strip is the model: right-clicking either button opens the
 * tab's history, so a long session can jump straight to an older page instead
 * of pressing Back repeatedly.
 */

import { useMemo } from "react";
import { ArrowLeft, ArrowRight } from "@multica/ui/icons";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuTrigger,
} from "@multica/ui/components/ui/context-menu";
import { TooltipHint } from "@multica/ui/components/common/tooltip-hint";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { useNavigation } from "../navigation";
import { useTabHistory } from "./use-tab-history";

/** A 28px strip has 24px of room for a control once its padding is counted. */
const NAV_BUTTON_CLASS =
  "flex size-6 shrink-0 items-center justify-center rounded-md text-faint-foreground transition-colors hover:bg-muted/50 hover:text-muted-foreground focus-visible:outline-2 focus-visible:outline-ring disabled:pointer-events-none disabled:opacity-30 [&_svg]:size-3.5";

/** A page with no resolved title still has to read as something. */
function historyEntryLabel(
  url: string,
  titles: ReadonlyMap<string, string>,
): string {
  const titled = titles.get(url);
  if (titled) return titled;
  const path = url.replace(/[?#].*$/, "");
  const segment = path.split("/").filter(Boolean).at(-1);
  return segment && segment !== "issues" ? segment : path;
}

export function TabHistoryButtons() {
  const { t } = useT("layout");
  const navigation = useNavigation();
  const {
    canGoForward,
    historyEntries,
    historyIndex,
    browsingHistory,
    goToHistoryIndex,
  } = useTabHistory();

  // Either stack is somewhere to go back to: the tab's own, or — on web — the
  // page that was open before the shell mounted (the adapter reads the
  // browser's session history for that).
  const canGoBack = historyIndex > 0 || (navigation.canGoBack?.() ?? false);
  const titles = useMemo(
    () =>
      new Map(
        browsingHistory.flatMap((entry) =>
          entry.title ? [[entry.url, entry.title] as const] : [],
        ),
      ),
    [browsingHistory],
  );

  const historyMenu = (label: string) =>
    historyEntries.length > 0 ? (
      <ContextMenuContent
        aria-label={label}
        className="max-h-80 w-64 overflow-y-auto"
      >
        {historyEntries.map((url, index) => (
          <ContextMenuItem
            key={`${index}:${url}`}
            aria-current={index === historyIndex ? "true" : undefined}
            data-history-index={index}
            onClick={() => goToHistoryIndex(index)}
            className={cn(
              "flex min-w-0 items-center gap-2",
              index === historyIndex && "font-medium",
            )}
          >
            <span
              aria-hidden
              className={cn(
                "size-1 shrink-0 rounded-full bg-current",
                index !== historyIndex && "opacity-0",
              )}
            />
            <span className="min-w-0 flex-1 truncate">
              {historyEntryLabel(url, titles)}
            </span>
          </ContextMenuItem>
        ))}
      </ContextMenuContent>
    ) : null;

  return (
    <div
      data-slot="tab-history"
      className="flex shrink-0 items-center gap-0.5 pl-1"
    >
      <ContextMenu>
        <TooltipHint label={t(($) => $.window.go_back)} side="bottom">
          <ContextMenuTrigger
            render={
              <button
                type="button"
                aria-label={t(($) => $.window.go_back)}
                disabled={!canGoBack}
                onClick={() => navigation.back()}
                className={NAV_BUTTON_CLASS}
              >
                <ArrowLeft aria-hidden />
              </button>
            }
          />
        </TooltipHint>
        {historyMenu(t(($) => $.window.back_history))}
      </ContextMenu>
      <ContextMenu>
        <TooltipHint label={t(($) => $.window.go_forward)} side="bottom">
          <ContextMenuTrigger
            render={
              <button
                type="button"
                aria-label={t(($) => $.window.go_forward)}
                disabled={!canGoForward}
                onClick={() => navigation.forward?.()}
                className={NAV_BUTTON_CLASS}
              >
                <ArrowRight aria-hidden />
              </button>
            }
          />
        </TooltipHint>
        {historyMenu(t(($) => $.window.forward_history))}
      </ContextMenu>
    </div>
  );
}
