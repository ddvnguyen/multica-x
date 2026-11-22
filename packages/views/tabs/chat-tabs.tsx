"use client";

/**
 * Chats in the tab strip (M-413 round 5).
 *
 * A conversation is a tab, not a second kind of tab. `/{ws}/chat?session=<id>`
 * is a normal `TabSession` — same shape, drag order, close, middle-click,
 * Cmd/Ctrl+1..9, window Back/Forward and persistence as any page tab. The store
 * already keys a session's tab by the session (`chatSessionIdForUrl`,
 * `findTabForUrl`), so one conversation is one tab: opening it again focuses
 * that tab instead of adding a copy.
 *
 * What lives here is what the strip needs to know about a conversation:
 *
 * - `openChatSessionTab` — the one way a chat list, a history row or a deep
 *   link puts a conversation on screen.
 * - `useChatTabState` + `ChatTabLeading` — the running spinner and unread dot
 *   a conversation's tab carries, read from the same caches the Chat surfaces
 *   use rather than from a separate list of open chats.
 * - `HeaderChatControls` — start a quick chat, or find an old one.
 *
 * The placement context survives because a shell that lists chats in the strip
 * has no room for the footer's quick-chat chips as well: the bottom bar reads
 * it to stay out of the way (see `AppBottomBar`).
 */

import { createContext, useContext, useMemo, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2, MessageSquarePlus } from "@multica/ui/icons";
import { useChatStore } from "@multica/core/chat";
import {
  chatSessionsOptions,
  pendingChatTasksOptions,
} from "@multica/core/chat/queries";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import { chatSessionIdForUrl, useTabStore } from "@multica/core/tabs";
import type { ChatSession } from "@multica/core/types";
import { TooltipHint } from "@multica/ui/components/common/tooltip-hint";
import { useT } from "../i18n";
import { ChatBarHistory } from "../chat/components/chat-bar-history";

const ChatTabsInHeaderContext = createContext(false);

/** True when the shell lists chat sessions as tabs of its strip. */
export function useChatTabsInHeader(): boolean {
  return useContext(ChatTabsInHeaderContext);
}

/** Opt a shell into listing its chat sessions in the header strip. */
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

/** The strip URL of one conversation. */
export function chatSessionHref(chatHref: string, sessionId: string): string {
  return `${chatHref}?session=${encodeURIComponent(sessionId)}`;
}

/**
 * Put a conversation on screen the way every other place does: focus the tab
 * that already holds it, or open one tab for it. The store's dedupe is the
 * only dedupe — no caller keeps its own list of open chats.
 */
export function openChatSessionTab(
  chatHref: string,
  session: Pick<ChatSession, "id" | "title">,
): string {
  return useTabStore
    .getState()
    .openTab(chatSessionHref(chatHref, session.id), session.title?.trim() || "", {
      activate: true,
    });
}

export interface ChatTabState {
  /** A task is running for this conversation. */
  running: boolean;
  /** A reply landed that the reader has not opened yet. */
  unread: boolean;
}

/**
 * The live state of a tab's conversation, or null when the tab is not one.
 * Derived from the chat caches so the strip cannot drift from the Chat page,
 * the window and the history popover.
 */
export function useChatTabState(url: string): ChatTabState | null {
  return useChatSessionState(chatSessionIdForUrl(url));
}

/**
 * The state of one conversation by id. The strip resolves the id first and
 * mounts this only for a conversation tab, so page tabs never subscribe to
 * the chat caches.
 */
export function useChatSessionState(sessionId: string | null): ChatTabState | null {
  const wsId = useCurrentWorkspace()?.id ?? "";
  const enabled = !!sessionId && !!wsId;
  const { data: sessions = EMPTY_SESSIONS } = useQuery({
    ...chatSessionsOptions(wsId),
    enabled,
  });
  const { data: pending } = useQuery({
    ...pendingChatTasksOptions(wsId),
    enabled,
  });

  return useMemo(() => {
    if (!sessionId) return null;
    return {
      running: (pending?.tasks ?? []).some(
        (task) => task.chat_session_id === sessionId,
      ),
      unread: sessions.find((session) => session.id === sessionId)?.has_unread ?? false,
    };
  }, [sessionId, sessions, pending]);
}

/**
 * A tab's leading slot: the chat icon with a dot when a reply is unread, and a
 * spinner in its place while a task runs. Every other tab keeps its own visual
 * untouched.
 */
export function ChatTabLeading({
  state,
  children,
}: {
  state: ChatTabState | null;
  children: ReactNode;
}) {
  if (!state) return <>{children}</>;
  if (state.running) {
    return (
      <span
        data-chat-tab-running
        className="flex size-4 shrink-0 items-center justify-center"
      >
        <Loader2 aria-hidden className="size-3.5 animate-spin" />
      </span>
    );
  }
  return (
    <span className="relative flex size-4 shrink-0 items-center justify-center">
      {children}
      {state.unread && (
        <span
          aria-hidden
          data-chat-tab-unread
          className="absolute -top-1 -right-1 size-1.5 rounded-full bg-brand"
        />
      )}
    </span>
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
  const wsId = useCurrentWorkspace()?.id ?? "";
  const chatHref = useWorkspacePaths().chat();
  const { data: sessions = EMPTY_SESSIONS } = useQuery(chatSessionsOptions(wsId));
  const { data: pending } = useQuery(pendingChatTasksOptions(wsId));
  const runningSessionIds = useMemo(
    () => new Set((pending?.tasks ?? []).map((task) => task.chat_session_id)),
    [pending],
  );
  const isOpen = useChatStore((s) => s.isOpen);
  const activeSessionId = useChatStore((s) => s.activeSessionId);
  const setOpen = useChatStore((s) => s.setOpen);
  const startNewChat = useChatStore((s) => s.startNewChat);
  // A brand-new chat has no session yet, so no conversation tab reads as
  // current and the floating window is the only place it lives. Skimming the
  // strip's conversations draws the same line the bottom bar drew for Ask
  // Multica.
  const fresh = isOpen && !activeSessionId;
  const newChatLabel = t(($) => $.window.new_chat_tooltip);

  const openSession = (session: ChatSession) => {
    // The conversation is about to be the page, so a floating window over it
    // would be a second copy of what the strip just opened.
    if (isOpen) setOpen(false);
    openChatSessionTab(chatHref, session);
  };

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
