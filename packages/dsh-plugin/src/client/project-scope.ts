/**
 * The island's one global project selection (M-173).
 *
 * The header picker is not part of tab history: it is a scope every MMX view
 * reads, so a new tab opens inside the selected project instead of resetting
 * the filter. Only the project id is persisted — the list it came from is the
 * workspace's, and re-reading the stored id against the live list is what lets
 * a deleted project fall back to "All projects" instead of a dead filter.
 *
 * A selection that no longer exists is therefore dropped on read, and the
 * caller writes the cleared value back, so the stale id does not survive a
 * reload either.
 */

import type { Project } from "../fork";
import type { IslandStorage } from "./storage";

/** Storage key of the selection; suffix-free because the island is one workspace. */
export const PROJECT_SCOPE_KEY = "project.v1";

/** The sentinel for "no project filter". */
export const ALL_PROJECTS = null;

/**
 * Read the stored selection for a project list.
 *
 * @param storage - the island's namespaced storage.
 * @param projects - the workspace's projects, or `undefined` while loading.
 * @returns the stored id when the list contains it; otherwise "All projects".
 *   While the list is still loading the stored id is returned as-is, so the
 *   panel does not flash back to "All projects" on every mount.
 */
export function readProjectScope(
  storage: IslandStorage,
  projects: readonly Pick<Project, "id">[] | undefined,
): string | null {
  const stored = storage.getItem(PROJECT_SCOPE_KEY);
  if (stored === null || stored === "") return ALL_PROJECTS;
  if (projects === undefined) return stored;
  return projects.some((project) => project.id === stored) ? stored : ALL_PROJECTS;
}

/**
 * Persist a selection, or clear the key for "All projects".
 *
 * @param storage - the island's namespaced storage.
 * @param projectId - the selected project, or `null` for every project.
 */
export function writeProjectScope(
  storage: IslandStorage,
  projectId: string | null,
): void {
  if (projectId === ALL_PROJECTS) {
    storage.removeItem(PROJECT_SCOPE_KEY);
    return;
  }
  storage.setItem(PROJECT_SCOPE_KEY, projectId);
}
