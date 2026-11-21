"use client";

/**
 * Back and Forward for the tab strip (M-413 round 2).
 *
 * The strip is the browser chrome of the SPA, so it carries the two buttons a
 * browser puts next to the tabs. They walk the window's one history (M-413
 * round 4) through the navigation adapter — the same calls the keyboard
 * chords and the mouse side buttons make — so every entry point moves through
 * one history: the previous location across tabs, focusing that tab if it is
 * open, skipping entries of closed tabs.
 *
 * The dsh island's strip is the model (M-197): right-clicking either button
 * opens the window's history, so a long path can jump straight to an older
 * page instead of pressing Back repeatedly.
 */

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
import { useWindowTabHistory } from "./use-tab-history";

/** A 28px strip has 24px of room for a control once its padding is counted. */
const NAV_BUTTON_CLASS =
  "flex size-6 shrink-0 items-center justify-center rounded-md text-faint-foreground transition-colors hover:bg-muted/50 hover:text-muted-foreground focus-visible:outline-2 focus-visible:outline-ring disabled:pointer-events-none disabled:opacity-30 [&_svg]:size-3.5";

export function TabHistoryButtons() {
  const { t } = useT("layout");
  const navigation = useNavigation();
  const {
    canGoForward,
    history,
    historyIndex,
    goToHistoryIndex,
  } = useWindowTabHistory();

  // Either history is somewhere to go back to: the window's, or the page that
  // was open before the shell mounted (the adapter reads the browser's own
  // session history for that).
  const canGoBack = navigation.canGoBack?.() ?? false;

  const historyMenu = (label: string) =>
    history.length > 0 ? (
      <ContextMenuContent
        aria-label={label}
        className="max-h-80 w-64 overflow-y-auto"
      >
        {history.map((entry, index) => (
          <ContextMenuItem
            key={`${index}:${entry.url}`}
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
            <span className="min-w-0 flex-1 truncate">{entry.label}</span>
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
