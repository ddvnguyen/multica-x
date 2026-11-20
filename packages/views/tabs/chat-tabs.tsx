"use client";

/**
 * Where a chat's open sessions live in the tabbed shell (M-413).
 *
 * The quick-chat chips have always sat in the bottom bar, beside Ask Multica.
 * That is a second tab strip: it shows the same "switch to that conversation"
 * affordance as the header strip, in a second place, at a second size. When the
 * placement is `header`, the sessions become compact tabs in the header strip
 * (`HeaderChatTabs` below) and the bottom bar keeps only Ask Multica and its
 * history — one strip for "where am I", one bar for "start something".
 *
 * The placement is a provider rather than a prop chain because the two halves
 * are siblings in different trees: the strip is rendered by the shell, the chips
 * by the shared bottom bar. Default `false`: web and desktop keep the footer
 * chips until their shell opts in.
 */

import { createContext, useContext, useMemo, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2, MessageSquare, MessageSquarePlus, X } from "@multica/ui/icons";
import { useChatStore } from "@multica/core/chat";
import {
  chatSessionsOptions,
  pendingChatTasksOptions,
} from "@multica/core/chat/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { ChatSession } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { TooltipHint } from "@multica/ui/components/common/tooltip-hint";
import { useT } from "../i18n";
import { useNavigation } from "../navigation";
import { ChatBarHistory } from "../chat/components/chat-bar-history";
import {
  chatActivity,
  selectChatBarSessions,
  type ChatBarSession,
} from "../chat/components/chat-bar-sessions";

const ChatTabsInHeaderContext = createContext(false);

/** True when the shell shows chat sessions as header tabs. */
export function useChatTabsInHeader(): boolean {
  return useContext(ChatTabsInHeaderContext);
}

/** Opt a shell into the header placement for its chat sessions. */
export function ChatTabsInHeaderProvider({
  value = true,
  children,
}: {
  value?: boolean;
  children: ReactNode;
}) {
  return (
    <ChatTabsInHeaderContext.Provider value={value}>
      {children}
    </ChatTabsInHeaderContext.Provider>
  );
}

const EMPTY_SESSIONS: ChatSession[] = [];

/**
 * How many chat tabs the strip shows. A session tab is the place the reader was
 * working, not an inbox: four keeps the strip's own tabs in view on a narrow
 * window, and the Chat page plus the bottom bar's history reach the rest.
 */
const MAX_CHAT_TABS = 4;

/**
 * The open chats, as tabs of the header strip: same tab shape as a page tab,
 * chat icon, a dot for unread and a spinner while a task runs. The close
 * affordance appears on hover only, so a row of them stays quiet.
 *
 * Clicking one opens that conversation on the Chat page, in the current tab,
 * which is what a tab does everywhere else in the strip.
 */
export function HeaderChatTabs() {
  const inHeader = useChatTabsInHeader();
  if (!inHeader) return null;
  return <HeaderChatTabsInner />;
}

/**
 * The chats the header strip shows, and the one action they share: open a
 * conversation as the page. Both the tab row and the strip's new-chat/
 * history controls read this, so they agree on which chat is open.
 */
function useHeaderChatSessions() {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const isOpen = useChatStore((s) => s.isOpen);
  const activeSessionId = useChatStore((s) => s.activeSessionId);
  const selectedAgentId = useChatStore((s) => s.selectedAgentId);
  const kept = useChatStore((s) => s.chatBarKept);
  const dismissed = useChatStore((s) => s.chatBarDismissed);
  const setActiveSession = useChatStore((s) => s.setActiveSession);
  const setSelectedAgentId = useChatStore((s) => s.setSelectedAgentId);
  const setOpen = useChatStore((s) => s.setOpen);
  const keepChatInBar = useChatStore((s) => s.keepChatInBar);

  const { data: sessions = EMPTY_SESSIONS } = useQuery(chatSessionsOptions(wsId));
  const { data: pending } = useQuery(pendingChatTasksOptions(wsId));
  const runningSessionIds = useMemo(
    () => new Set((pending?.tasks ?? []).map((task) => task.chat_session_id)),
    [pending],
  );

  const { visible } = useMemo(
    () =>
      selectChatBarSessions({
        sessions,
        runningSessionIds,
        // The strip marks the conversation the Chat page shows, not one that
        // only lives in the quick-chat window.
        openSessionId: isOpen ? activeSessionId : null,
        kept,
        dismissed,
        maxVisible: MAX_CHAT_TABS,
      }),
    [sessions, runningSessionIds, isOpen, activeSessionId, kept, dismissed],
  );

  const chatHref = paths.chat();

  const openSession = (session: ChatSession) => {
    if (session.agent_id !== selectedAgentId) setSelectedAgentId(session.agent_id);
    keepChatInBar(session.id);
    setActiveSession(session.id);
    // The conversation is about to be the page, so a floating window over it
    // would be a second copy of what the strip just navigated to.
    if (isOpen) setOpen(false);
    navigation.push(`${chatHref}?session=${encodeURIComponent(session.id)}`);
  };

  return {
    sessions,
    runningSessionIds,
    visible,
    chatHref,
    isOpen,
    openSession,
  };
}

function HeaderChatTabsInner() {
  const { t } = useT("chat");
  const { visible, chatHref, openSession } = useHeaderChatSessions();
  const navigation = useNavigation();
  const dismissChatFromBar = useChatStore((s) => s.dismissChatFromBar);

  if (visible.length === 0) return null;

  const closeTab = (item: ChatBarSession) => {
    dismissChatFromBar(item.session.id, chatActivity(item.session));
  };

  return (
    <>
      {visible.map((item) => {
        const title = item.session.title || t(($) => $.window.untitled);
        const state = item.running
          ? t(($) => $.window.running)
          : item.unread
            ? t(($) => $.window.unread)
            : null;
        const current =
          navigation.pathname === chatHref &&
          navigation.searchParams.get("session") === item.session.id;
        return (
          <div
            key={item.session.id}
            data-chat-tab={item.session.id}
            data-active={current || undefined}
            className="group/chattab relative h-full w-32 min-w-24 shrink-0"
          >
            <span
              aria-hidden
              className={cn(
                "pointer-events-none absolute inset-x-0 inset-y-1 rounded-md transition-colors",
                current
                  ? "bg-page-canvas shadow-[var(--surface-shadow)] ring-1 ring-surface-border"
                  : "bg-page-canvas/50 group-hover/chattab:bg-page-canvas/80",
              )}
            />
            <TooltipHint label={state ? `${title} · ${state}` : title}>
              <button
                type="button"
                aria-pressed={current}
                aria-label={state ? `${title}, ${state}` : title}
                onClick={() => openSession(item.session)}
                onAuxClick={(e) => {
                  if (e.button === 1) {
                    e.preventDefault();
                    closeTab(item);
                  }
                }}
                className={cn(
                  "relative flex size-full min-w-0 items-center gap-1.5 px-2 text-caption transition-colors select-none",
                  current ? "text-foreground" : "text-muted-foreground hover:text-foreground",
                )}
              >
                <span
                  aria-hidden
                  className="flex size-3.5 shrink-0 items-center justify-center [&_svg]:size-3.5"
                >
                  {item.running ? (
                    <Loader2 className="animate-spin" />
                  ) : (
                    <MessageSquare />
                  )}
                </span>
                <span
                  className="min-w-0 flex-1 overflow-hidden text-left whitespace-nowrap"
                  style={{
                    maskImage:
                      "linear-gradient(to right, black calc(100% - 12px), transparent)",
                    WebkitMaskImage:
                      "linear-gradient(to right, black calc(100% - 12px), transparent)",
                  }}
                >
                  {title}
                </span>
              </button>
            </TooltipHint>
            <button
              type="button"
              aria-label={t(($) => $.bar.close_chip, { title })}
              onClick={() => closeTab(item)}
              className="absolute top-1 right-1 hidden size-3.5 cursor-pointer items-center justify-center rounded-sm text-muted-foreground outline-none hover:bg-muted-foreground/20 hover:text-foreground group-hover/chattab:flex focus-visible:flex [&_svg]:size-2.5"
            >
              <X aria-hidden />
            </button>
          </div>
        );
      })}
    </>
  );
}

/**
 * The strip's end of the chat: start a new one, or find an old one. These
 * lived in the bottom bar's Ask Multica + history until the footer went away
 * with the merge (M-413 round 2) — the strip is now the shell's only bar, so
 * starting a conversation has to be reachable from it.
 */
export function HeaderChatControls() {
  const inHeader = useChatTabsInHeader();
  if (!inHeader) return null;
  return <HeaderChatControlsInner />;
}

function HeaderChatControlsInner() {
  const { t } = useT("chat");
  const { sessions, runningSessionIds, visible, openSession } =
    useHeaderChatSessions();
  const isOpen = useChatStore((s) => s.isOpen);
  const startNewChat = useChatStore((s) => s.startNewChat);
  // A brand-new chat has no session yet, so no chat tab reads as current and
  // the floating window is the only place it lives. Skimming the strip's
  // visible chats draws the same line the bottom bar drew for Ask Multica.
  const fresh = isOpen && !visible.some((item) => item.open);
  const newChatLabel = t(($) => $.window.new_chat_tooltip);

  return (
    <div
      data-slot="header-chat-controls"
      className="flex shrink-0 items-center gap-0.5 pr-1"
    >
      <TooltipHint label={newChatLabel} side="bottom">
        <button
          type="button"
          data-chat-anchor="new"
          aria-pressed={fresh}
          aria-label={newChatLabel}
          onClick={() => startNewChat()}
          className="flex size-6 shrink-0 items-center justify-center rounded-md text-faint-foreground transition-colors hover:bg-muted/50 hover:text-muted-foreground focus-visible:outline-2 focus-visible:outline-ring aria-pressed:bg-muted aria-pressed:text-foreground [&_svg]:size-3.5"
        >
          <MessageSquarePlus aria-hidden />
        </button>
      </TooltipHint>
      <ChatBarHistory
        sessions={sessions}
        runningSessionIds={runningSessionIds}
        onSelect={openSession}
      />
    </div>
  );
}
