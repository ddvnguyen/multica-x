"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { runtimeListOptions } from "@multica/core/runtimes";
import {
  memberListOptions,
  skillDetailOptions,
  skillVersionsOptions,
} from "@multica/core/workspace/queries";
import { IssueSidebarStack } from "../../issues/components/issue-sidebar-section";
import { useNavigation } from "../../navigation";
import { SkillIcon } from "../lib/skill-icon";
import { readOrigin } from "../lib/origin";
import { SkillPropertiesPanel } from "./skill-properties-panel";

/** Side preview stack for a skill target (M-336 G6). */
export function SkillSideStack({ skillId }: { skillId: string }) {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const { data: skill } = useQuery(skillDetailOptions(wsId, skillId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const { data: versions } = useQuery(skillVersionsOptions(wsId, skillId));

  const origin = useMemo(() => (skill ? readOrigin(skill) : null), [skill]);
  const originRuntime = useMemo(() => {
    if (!origin || origin.type !== "runtime_local" || !origin.runtime_id) return null;
    return runtimes.find((r) => r.id === origin.runtime_id) ?? null;
  }, [origin, runtimes]);
  const creator = useMemo(
    () =>
      skill?.created_by ? members.find((m) => m.user_id === skill.created_by) ?? null : null,
    [members, skill?.created_by],
  );
  const latestVersion: number | null | undefined = versions
    ? (versions[0]?.version ?? null)
    : undefined;

  if (!skill || !origin) return null;

  const openVersions = () => {
    if (!navigation.openInNewTab) return;
    navigation.openInNewTab(`${paths.skillDetail(skillId)}?view=versions`, skillId, {
      activate: true,
    });
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div
        className="flex items-baseline gap-2 border-b border-border/60 px-3 py-2"
        data-multica-side-preview-header=""
        title={skill.name}
      >
        <span className="inline-flex min-w-0 items-center gap-1.5">
          <SkillIcon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
          <span className="truncate text-caption font-medium text-foreground">{skill.name}</span>
        </span>
      </div>
      <IssueSidebarStack>
        <SkillPropertiesPanel
          skill={skill}
          name={skill.name}
          description={skill.description}
          canEdit={false}
          origin={origin}
          originRuntime={originRuntime}
          creator={creator}
          latestVersion={latestVersion}
          onNameChange={() => {}}
          onDescriptionChange={() => {}}
          onOpenVersions={openVersions}
        />
      </IssueSidebarStack>
    </div>
  );
}
