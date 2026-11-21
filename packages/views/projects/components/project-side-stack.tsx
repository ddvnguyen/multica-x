"use client";

import { useQuery } from "@tanstack/react-query";
import { projectDetailOptions } from "@multica/core/projects/queries";
import { PROJECT_STATUS_CONFIG } from "@multica/core/projects/config";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useActorName } from "@multica/core/workspace/hooks";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { useProjectPriorityLabels, useProjectStatusLabels } from "./labels";
import { IssueSidebarPropertyGrid, IssueSidebarStack } from "../../issues/components/issue-sidebar-section";

/** Read-only project properties for the host side preview (M-336 G6). */
export function ProjectSideStack({ projectId }: { projectId: string }) {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const { t } = useT("projects");
  const { getActorName } = useActorName();
  const { data: project, isLoading } = useQuery({
    ...projectDetailOptions(wsId, projectId),
    enabled: wsId !== "",
  });
  const statusLabels = useProjectStatusLabels();
  const priorityLabels = useProjectPriorityLabels();

  if (isLoading || !project) return null;

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
      <button
        type="button"
        className="flex min-w-0 items-center gap-2 border-b border-border/60 px-3 py-2 text-left"
        data-multica-side-preview-header=""
        title={project.title}
        onClick={() => navigation.openInNewTab?.(paths.projectDetail(projectId), projectId, { activate: true })}
      >
        <span className="shrink-0">{project.icon || "📁"}</span>
        <span className="truncate text-caption font-medium text-foreground">{project.title}</span>
      </button>
      <IssueSidebarStack className="py-4">
        <section aria-label={t(($) => $.detail.section_properties)}>
          <h2 className="px-4 pb-2 text-caption font-medium text-foreground">{t(($) => $.detail.section_properties)}</h2>
          <IssueSidebarPropertyGrid>
            <span className="text-caption text-muted-foreground">{t(($) => $.table.status)}</span>
            <span className="flex min-w-0 items-center gap-1.5 text-caption text-foreground">
              <span className={`size-2 shrink-0 rounded-full ${PROJECT_STATUS_CONFIG[project.status].dotColor}`} />
              <span className="truncate">{statusLabels[project.status]}</span>
            </span>
            <span className="text-caption text-muted-foreground">{t(($) => $.table.priority)}</span>
            <span className="truncate text-caption text-foreground">{priorityLabels[project.priority]}</span>
            {project.lead_type && project.lead_id && (
              <>
                <span className="text-caption text-muted-foreground">{t(($) => $.table.lead)}</span>
                <span className="truncate text-caption text-foreground">{getActorName(project.lead_type, project.lead_id)}</span>
              </>
            )}
            {project.start_date && <><span className="text-caption text-muted-foreground">{t(($) => $.detail.prop_start_date)}</span><span className="text-caption text-foreground">{project.start_date}</span></>}
            {project.due_date && <><span className="text-caption text-muted-foreground">{t(($) => $.detail.prop_due_date)}</span><span className="text-caption text-foreground">{project.due_date}</span></>}
          </IssueSidebarPropertyGrid>
        </section>
        {project.description && (
          <section aria-label={t(($) => $.detail.section_description)}>
            <h2 className="px-4 pb-2 text-caption font-medium text-foreground">{t(($) => $.detail.section_description)}</h2>
            <p className="whitespace-pre-wrap px-4 text-caption text-muted-foreground">{project.description}</p>
          </section>
        )}
      </IssueSidebarStack>
    </div>
  );
}
