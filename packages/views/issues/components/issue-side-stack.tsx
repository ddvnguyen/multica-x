"use client";

import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueDetailOptions, childIssuesOptions } from "@multica/core/issues/queries";
import { useGitHubSettings } from "@multica/core/github";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { ExecutionLogSection } from "./execution-log-section";
import { IssuePropertiesSection } from "./issue-properties-section";
import { IssueSidebarStack } from "./issue-sidebar-section";
import { PullRequestsSection } from "./pull-requests-section";
import { WakeupsSection } from "./wakeups-section";
import { maxSiblingStage } from "./pickers/stage-picker";

/**
 * The issue side stack: Properties, Wakeups, Pull requests and Execution log.
 *
 * One composition for both hosts that show it — the issue page's own sidebar
 * (which adds its remaining sections around these) and the dsh side preview
 * (M-336), where these four are the whole column. Each section is its own
 * component above; this file only decides which ones belong together, so a
 * change to a section reaches both hosts at once.
 */
export function IssueSideStack({ issueId }: { issueId: string }) {
  const wsId = useWorkspaceId();
  const githubSettings = useGitHubSettings();
  const { categoryOf } = useIssueStatuses(wsId);

  const { data: issue = null } = useQuery(issueDetailOptions(wsId, issueId));
  const childIssues = useQuery({
    ...childIssuesOptions(wsId, issueId),
    enabled: issue !== null,
  });
  // The stage picker offers stages relative to the issue's siblings, which only
  // the parent's children can name.
  const parentIssueId = issue?.parent_issue_id ?? null;
  const parentChildren = useQuery({
    ...childIssuesOptions(wsId, parentIssueId ?? ""),
    enabled: parentIssueId !== null,
  });

  if (issue === null) return null;

  return (
    <IssueSidebarStack>
      <IssuePropertiesSection
        issue={issue}
        maxStage={maxSiblingStage(parentChildren.data ?? [])}
      />

      <WakeupsSection
        issueId={issue.id}
        closed={["done", "closed"].includes(categoryOf(issue.status))}
        defaultAgentId={issue.assignee_type === "agent" ? (issue.assignee_id ?? undefined) : undefined}
      />

      {/* Hidden when the workspace disables the PR sidebar (or the GitHub
          master switch is off), exactly as on the page. */}
      {githubSettings.prSidebar && (
        <PullRequestsSection
          issueId={issue.id}
          identifier={issue.identifier}
          open
          onOpenChange={() => {}}
        />
      )}

      <ExecutionLogSection
        key={issue.id}
        issueId={issue.id}
        identifier={issue.identifier}
        issueTitle={issue.title}
        hasChildren={(childIssues.data ?? []).length > 0}
      />
    </IssueSidebarStack>
  );
}
