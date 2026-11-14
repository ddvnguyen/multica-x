// Per-card answer drafts — ask + plan free text, options, plan comments.
//
// The ask form (`AgentQuestionCard`) and the plan panel (`PlanReviewPanel`)
// unmount whenever the reader navigates away, switches the rail entry, or the
// keep-alive island detaches — and their `useState` drafts go with them. This
// store keeps the in-progress answer keyed by `(issue, card)` in workspace
// localStorage so it survives all of that, and drops it once the card closes.
//
// Shape mirrors `comment-draft-store.ts` but with the ask form's own value:
// picked option indexes plus free text (and the plan panel's pending comments
// + request-changes note, keyed by plan id). A draft is meaningful when any
// part of it carries intent — a pick, non-blank text, or a pending comment —
// so clears on send/withdraw prüfe the whole record, not one field.
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import {
  createWorkspaceAwareStorage,
  registerForWorkspaceRehydration,
} from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";
import { registerDraftCleanup } from "../../drafts/cleanup-registry";
import type { AgentAnswerDraft } from "../agent-question";

export interface AskCardDraft {
  /** Per-question answers, in the card's question order. */
  answers: AgentAnswerDraft[];
  /** Plan panel only: staged comments awaiting a verdict. */
  planComments: string[];
  /** Plan panel only: the request-changes note. */
  planNote: string;
  updatedAt: number;
}

/** Keys: card drafts by card id, plan-panel drafts by plan id. */
export type AskDraftKey = `ask:${string}` | `plan:${string}`;

interface AskDraftStore {
  drafts: Record<string, AskCardDraft>;
  getDraft: (key: AskDraftKey) => AskCardDraft | undefined;
  getAnswers: (key: AskDraftKey) => AgentAnswerDraft[] | undefined;
  setAnswers: (key: AskDraftKey, answers: readonly AgentAnswerDraft[]) => void;
  setPlanComments: (key: AskDraftKey, comments: readonly string[]) => void;
  setPlanNote: (key: AskDraftKey, note: string) => void;
  clearDraft: (key: AskDraftKey) => void;
  /** Drop every draft whose card/plan is no longer open. */
  pruneToOpen: (open: ReadonlySet<string>) => void;
}

// Drafts older than 30 days are dropped on store init — same TTL as the
// comment drafts, for the same quota reason.
const TTL_MS = 30 * 24 * 60 * 60 * 1000;

function isMeaningful(draft: AskCardDraft): boolean {
  return (
    draft.answers.some(
      (answer) => answer.selected.length > 0 || answer.text.trim() !== "",
    ) ||
    draft.planComments.some((comment) => comment.trim() !== "") ||
    draft.planNote.trim() !== ""
  );
}

function writeDraft(
  drafts: Record<string, AskCardDraft>,
  key: string,
  patch: Partial<Omit<AskCardDraft, "updatedAt">>,
): Record<string, AskCardDraft> {
  const current = drafts[key];
  const next: AskCardDraft = {
    answers: patch.answers !== undefined ? [...patch.answers] : (current?.answers ?? []),
    planComments: patch.planComments !== undefined ? [...patch.planComments] : (current?.planComments ?? []),
    planNote: patch.planNote !== undefined ? patch.planNote : (current?.planNote ?? ""),
    updatedAt: Date.now(),
  };
  if (!isMeaningful(next)) {
    if (!(key in drafts)) return drafts;
    const pruned = { ...drafts };
    delete pruned[key];
    return pruned;
  }
  return { ...drafts, [key]: next };
}

function pruneStaleDrafts(drafts: Record<string, AskCardDraft>): Record<string, AskCardDraft> {
  const cutoff = Date.now() - TTL_MS;
  let changed = false;
  const next: Record<string, AskCardDraft> = {};
  for (const [key, draft] of Object.entries(drafts)) {
    if (draft.updatedAt < cutoff || !isMeaningful(draft)) {
      changed = true;
      continue;
    }
    next[key] = draft;
  }
  return changed ? next : drafts;
}

export const ASK_DRAFT_STORAGE_KEY = "multica_ask_drafts";

export const useAskDraftStore = create<AskDraftStore>()(
  persist(
    (set, get) => ({
      drafts: {},
      getDraft: (key) => get().drafts[key],
      getAnswers: (key) => get().drafts[key]?.answers,
      setAnswers: (key, answers) =>
        set((s) => ({ drafts: writeDraft(s.drafts, key, { answers: [...answers] }) })),
      setPlanComments: (key, comments) =>
        set((s) => ({ drafts: writeDraft(s.drafts, key, { planComments: [...comments] }) })),
      setPlanNote: (key, note) =>
        set((s) => ({ drafts: writeDraft(s.drafts, key, { planNote: note }) })),
      clearDraft: (key) =>
        set((s) => {
          if (!(key in s.drafts)) return s;
          const next = { ...s.drafts };
          delete next[key];
          return { drafts: next };
        }),
      pruneToOpen: (open) =>
        set((s) => {
          let changed = false;
          const next: Record<string, AskCardDraft> = {};
          for (const [key, draft] of Object.entries(s.drafts)) {
            const id = key.includes(":") ? key.slice(key.indexOf(":") + 1) : key;
            if (open.has(id) || open.has(key)) {
              next[key] = draft;
            } else {
              changed = true;
            }
          }
          return changed ? { drafts: next } : s;
        }),
    }),
    {
      name: ASK_DRAFT_STORAGE_KEY,
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      merge: (persisted, current) => ({
        ...current,
        drafts: (persisted as { drafts?: Record<string, AskCardDraft> } | undefined)?.drafts ?? {},
      }),
      onRehydrateStorage: () => (state) => {
        if (state) {
          state.drafts = pruneStaleDrafts(state.drafts);
        }
      },
    },
  ),
);

registerForWorkspaceRehydration(() => useAskDraftStore.persist.rehydrate());

registerDraftCleanup({
  storageKey: ASK_DRAFT_STORAGE_KEY,
  workspaceScoped: true,
  resetInMemory: () => useAskDraftStore.setState({ drafts: {} }),
});
