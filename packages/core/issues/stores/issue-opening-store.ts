import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import {
  defaultStorage,
  onDefaultStorageAdapterChanged,
} from "../../platform/storage";

export type IssueOpenMode = "page" | "peek" | "tab";

interface IssueOpeningState {
  openMode: IssueOpenMode;
  hasUserChoice: boolean;
  setOpenMode: (openMode: IssueOpenMode) => void;
  setDefaultOpenMode: (openMode: IssueOpenMode) => void;
}

// Like theme and comment-composer preferences, this personal navigation habit
// is saved on this device across workspaces. Existing users keep full pages.
export const useIssueOpeningStore = create<IssueOpeningState>()(
  persist(
    (set) => ({
      openMode: "page",
      hasUserChoice: false,
      setOpenMode: (openMode) => set({ openMode, hasUserChoice: true }),
      setDefaultOpenMode: (openMode) => set({ openMode }),
    }),
    {
      name: "multica_issue_opening",
      storage: createJSONStorage(() => defaultStorage),
      partialize: ({ openMode }) => ({ openMode }),
      merge: (persisted, current) => ({
        ...current,
        openMode: persisted && typeof persisted === "object" &&
          "openMode" in persisted &&
          (persisted.openMode === "peek" || persisted.openMode === "tab")
          ? persisted.openMode
          : "page",
        hasUserChoice: Boolean(persisted && typeof persisted === "object" &&
          "openMode" in persisted &&
          (persisted.openMode === "page" || persisted.openMode === "peek" || persisted.openMode === "tab")),
      }),
    },
  ),
);

// The dsh island installs its namespaced adapter (`multica.dsh.*`, M-360) when
// CoreProvider mounts — after this module's persist hydration has already read
// the bare origin and found nothing. Without the re-read, the opening
// capability's migration effect then sees the defaults and writes "tab" over
// a Side preview choice it never read, so the choice died on the island's
// every reload (M-336). The re-read follows the adapter swap the way the
// workspace-scoped stores follow a workspace switch.
onDefaultStorageAdapterChanged(() => void useIssueOpeningStore.persist.rehydrate());
