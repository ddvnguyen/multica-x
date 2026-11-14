import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { projectResourcesOptions, useCanonicalIssue, useViewStore } from "../fork";
import type { IslandRoute } from "./navigation";
import {
  folderForSelection,
  folderOfProjectResources,
  type FolderReason,
} from "./shell-link";

/**
 * Which folder dsh's right column addresses right now (M-159, M-316).
 *
 * The chain is the reader's own context, most specific first: the project the
 * header picked, the project the viewed issue belongs to, the one project the
 * board/list filter names, then the host's `workspaceFolder`. Two surfaces ask
 * the question — the header's panel toggle and the side preview (M-336) — and
 * they must answer it the same way, or a preview would open onto one folder
 * while the column shown beside it edits another.
 */

/**
 * One project's folder, or `undefined` while its resources are not known.
 *
 * `projectId === null` addresses nothing and the query stays disabled, so a
 * surface costs one request per distinct project a view actually names — and
 * none for "All projects" on a board with no filter.
 */
function useProjectFolder(
  workspaceId: string,
  projectId: string | null,
  daemonId: string,
): string | null | undefined {
  const { data } = useQuery({
    ...projectResourcesOptions(workspaceId, projectId ?? ""),
    enabled: projectId !== null,
  });
  if (projectId === null || data === undefined) return undefined;
  return folderOfProjectResources(data, daemonId);
}

export interface SidebarFolderInput {
  workspaceId: string;
  /** The header picker's project, or `null` for "All projects". */
  projects: readonly { id: string }[] | undefined;
  projectId: string | null;
  /** The host's default for "All projects"; may be empty. */
  workspaceFolder: string;
  /** The tab on screen; the viewed issue's project is one rung of the chain. */
  route: IslandRoute;
  daemonId: string;
}

/** The folder the column addresses, and which rung answered. */
export function useSidebarFolder(input: SidebarFolderInput): {
  folder: string | null;
  reason: FolderReason;
} {
  const { workspaceId, projects, projectId, workspaceFolder, route, daemonId } = input;

  // The viewed issue's project is the second rung (M-316): an issue page whose
  // picker still says "All projects" belongs to exactly one project, and the
  // toggle must work there. The identifier resolution shares the detail page's
  // own request, so this is one cache entry, not a second read.
  const issueSegment = route.view === "issue" ? route.issueId : null;
  const { issue } = useCanonicalIssue(workspaceId, issueSegment ?? "", {
    enabled: issueSegment !== null,
  });
  const issueProjectId = issueSegment === null ? null : (issue?.project_id ?? null);

  // The board/list filter is the third rung: one project named by the filter is
  // unambiguous context, more than one is not, and no other view has it.
  const projectFilters = useViewStore((state) => state.projectFilters);
  const viewProjectId =
    route.view === "board" && projectFilters.length === 1
      ? (projectFilters[0] ?? null)
      : null;

  const pickedFolder = useProjectFolder(workspaceId, projectId, daemonId);
  const issueProjectFolder = useProjectFolder(workspaceId, issueProjectId, daemonId);
  const viewProjectFolder = useProjectFolder(workspaceId, viewProjectId, daemonId);

  return useMemo(() => {
    // The picked project is set last: when two rungs name the same project, the
    // picker's own resources answer for it.
    const folders = new Map<string, string | null | undefined>();
    if (issueProjectId !== null) folders.set(issueProjectId, issueProjectFolder);
    if (viewProjectId !== null) folders.set(viewProjectId, viewProjectFolder);
    if (projectId !== null) folders.set(projectId, pickedFolder);
    return folderForSelection({
      projectId,
      projects,
      resourceFor: (id) => folders.get(id),
      workspaceFolder,
      issueProjectId,
      viewProjectId,
    });
  }, [
    projectId,
    projects,
    workspaceFolder,
    issueProjectId,
    viewProjectId,
    pickedFolder,
    issueProjectFolder,
    viewProjectFolder,
  ]);
}
