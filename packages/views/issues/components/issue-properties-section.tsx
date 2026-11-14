"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { propertyListOptions } from "@multica/core/properties";
import { issueLabelsOptions } from "@multica/core/labels";
import { useProjectWorkflow } from "@multica/core/issue-workflows";
import type { Issue } from "@multica/core/types";
import {
  CalendarClock,
  CalendarDays,
  Milestone,
  Plus,
  SlidersHorizontal,
  Tag,
} from "@multica/ui/icons";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";
import { TruncatedTooltip } from "@multica/ui/components/common/truncated-tooltip";
import { PropRow } from "../../common/prop-row";
import { PropertyIcon } from "../../common/property-icon";
import { IssueFlowCard } from "../../workflows/issue-flow-card";
import { IssueProjectPicker } from "../../projects/components/issue-project-picker";
import { useT } from "../../i18n";
import { useIssueActions } from "../actions";
import { isDuplicateIssue } from "./issue-duplicates";
import { PriorityIcon } from "./priority-icon";
import { StatusPicker } from "./pickers/status-picker";
import { PriorityPicker } from "./pickers/priority-picker";
import { StagePicker } from "./pickers/stage-picker";
import { StartDatePicker } from "./pickers/start-date-picker";
import { DueDatePicker } from "./pickers/due-date-picker";
import { AssigneePicker } from "./pickers/assignee-picker";
import { LabelPicker } from "./pickers/label-picker";
import { CustomPropertyValueEditor } from "./pickers/custom-property-picker";
import { IssueSidebarPropertyGrid, IssueSidebarSectionHeader } from "./issue-sidebar-section";

/**
 * The issue sidebar's Properties section, on its own.
 *
 * Every field row here edits the issue through `useIssueActions`, so the
 * section is the whole read/write of the block: the same component is the
 * issue page's sidebar header and the dsh side preview's first section (M-336),
 * one implementation for both hosts.
 *
 * `maxStage` is the highest stage among the issue's siblings, which only the
 * page can know (it already holds the parent's children); a host without it
 * lets the stage picker fall back to its own floor, exactly as it does when an
 * issue has no staged siblings.
 */

// The open set of optional property rows. Adding a new optional field means
// appending here, wiring its row in the JSX switch below, and adding a locale
// key. The picker, visibility rules, and add-property menu all flow from this
// one list.
//
// `stage` is only meaningful for a sub-issue (relative to its siblings), so its
// row and add-property entry are gated on `issue.parent_issue_id` at the render
// site below.
const OPTIONAL_PROP_KEYS = ["priority", "stage", "start_date", "due_date", "labels"] as const;
type OptionalPropKey = (typeof OPTIONAL_PROP_KEYS)[number];

function isOptionalPropSet(
  issue: Issue,
  key: OptionalPropKey,
  attachedLabelsCount: number,
): boolean {
  switch (key) {
    case "priority":
      return issue.priority !== "none";
    case "stage":
      return issue.stage !== null && issue.stage !== undefined;
    case "start_date":
      return !!issue.start_date;
    case "due_date":
      return !!issue.due_date;
    case "labels":
      return attachedLabelsCount > 0;
  }
}

export function IssuePropertiesSection({
  issue,
  maxStage,
}: {
  issue: Issue;
  /** Highest stage among the issue's siblings; `0` when nothing staged them. */
  maxStage?: number;
}) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const actions = useIssueActions(issue);
  const handleUpdateField = actions.updateField;

  const [propertiesOpen, setPropertiesOpen] = useState(true);

  // Per-issue, per-session set of optional properties currently visible in the
  // sidebar Properties section. Seeded on issue switch with whichever fields
  // are already set; "+ Add property" adds an entry, clearing a value does *not*
  // remove one (avoids row-flicker on edit → clear). Resets when the reader
  // navigates to a different issue.
  const [visibleOptionalProps, setVisibleOptionalProps] = useState<Set<OptionalPropKey>>(
    () => new Set(),
  );
  // Same progressive-disclosure machinery for custom properties, keyed by
  // property definition id instead of a static key union.
  const [visibleCustomProps, setVisibleCustomProps] = useState<Set<string>>(() => new Set());
  const [autoOpenCustomProp, setAutoOpenCustomProp] = useState<string | null>(null);
  // Optional property to auto-open as soon as it's mounted (the reader just
  // picked it from "+ Add property" and we want them dropped straight into
  // edit state). Consumed by the row that matches this key, cleared after.
  const [autoOpenProp, setAutoOpenProp] = useState<OptionalPropKey | null>(null);
  // Controlled state for the "+ Add property" popover. Base UI's Popover
  // doesn't auto-dismiss on item click (it's not a Menu primitive), so the
  // popover would stay open behind the newly auto-opened picker — two popovers
  // stacked. We close it explicitly in `addOptionalProp`.
  const [addPropPopoverOpen, setAddPropPopoverOpen] = useState(false);

  // Labels live in their own query (not on the issue body) — fetch the count
  // here so seeding can decide whether the "Labels" optional row should be
  // shown for an issue that already has labels attached.
  const { data: attachedLabels = [] } = useQuery(issueLabelsOptions(wsId, issue.id));
  const attachedLabelsCount = attachedLabels.length;

  // Custom property catalog. Includes archived definitions: an issue can still
  // carry a value written before the archive, and that row must stay
  // renderable (read-only) until someone clears it.
  const { data: workspaceProperties = [] } = useQuery(propertyListOptions(wsId, true));

  // Seed the visible-optional-props set:
  //   - on issue switch, reset to whichever fields are currently set
  //   - on the SAME issue, additively pick up fields the reader just set
  //     (so the row stays visible after they edit + clear in one session)
  // Removal happens only on issue switch — never on clear.
  const seededIssueIdRef = useRef<string | null>(null);
  useEffect(() => {
    if (seededIssueIdRef.current !== issue.id) {
      seededIssueIdRef.current = issue.id;
      setAutoOpenProp(null);
      setAutoOpenCustomProp(null);
      const seed = new Set<OptionalPropKey>();
      for (const k of OPTIONAL_PROP_KEYS) {
        if (isOptionalPropSet(issue, k, attachedLabelsCount)) seed.add(k);
      }
      setVisibleOptionalProps(seed);
      setVisibleCustomProps(new Set(Object.keys(issue.properties ?? {})));
      return;
    }
    setVisibleOptionalProps((prev) => {
      let next = prev;
      for (const k of OPTIONAL_PROP_KEYS) {
        if (isOptionalPropSet(issue, k, attachedLabelsCount) && !next.has(k)) {
          if (next === prev) next = new Set(prev);
          next.add(k);
        }
      }
      return next;
    });
    setVisibleCustomProps((prev) => {
      let next = prev;
      for (const propertyId of Object.keys(issue.properties ?? {})) {
        if (!next.has(propertyId)) {
          if (next === prev) next = new Set(prev);
          next.add(propertyId);
        }
      }
      return next;
    });
  }, [issue, attachedLabelsCount]);

  const addOptionalProp = useCallback(
    (key: OptionalPropKey) => {
      setVisibleOptionalProps((prev) => {
        if (prev.has(key)) return prev;
        const next = new Set(prev);
        next.add(key);
        return next;
      });
      setAutoOpenProp(key);
      // Dismiss the "+ Add property" popover so it doesn't sit stacked behind
      // the picker we're about to auto-open.
      setAddPropPopoverOpen(false);
    },
    [],
  );

  const addCustomProp = useCallback((propertyId: string) => {
    setVisibleCustomProps((prev) => {
      if (prev.has(propertyId)) return prev;
      const next = new Set(prev);
      next.add(propertyId);
      return next;
    });
    setAutoOpenCustomProp(propertyId);
    setAddPropPopoverOpen(false);
  }, []);

  // Clear the auto-open flag after the next render so pickers (which read
  // `defaultOpen` once via a useState initializer) keep the open state they
  // captured on mount, but later interactions don't re-trigger it.
  useEffect(() => {
    if (autoOpenProp === null) return;
    setAutoOpenProp(null);
  }, [autoOpenProp]);

  useEffect(() => {
    if (autoOpenCustomProp === null) return;
    setAutoOpenCustomProp(null);
  }, [autoOpenCustomProp]);

  const canAddProperty = useMemo(
    () =>
      OPTIONAL_PROP_KEYS.some(
        (key) => !visibleOptionalProps.has(key) && (key !== "stage" || issue.parent_issue_id != null),
      ) ||
      workspaceProperties.some(
        (property) =>
          !property.archived &&
          !visibleCustomProps.has(property.id) &&
          issue.properties?.[property.id] === undefined,
      ),
    [issue, visibleOptionalProps, visibleCustomProps, workspaceProperties],
  );

  // "+ Add property" — an inline row at the bottom of the Properties grid that
  // opens a Popover listing optional fields not yet displayed. Hidden once every
  // optional field is on screen.
  const addPropertyRow = canAddProperty && (
    <div className="col-span-2 mt-1">
      <Popover open={addPropPopoverOpen} onOpenChange={setAddPropPopoverOpen}>
        <PopoverTrigger
          className="-mx-2 flex items-center gap-1.5 rounded-md px-2 py-1 text-label text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
        >
          <Plus className="h-3 w-3 shrink-0" />
          <span>{t(($) => $.detail.add_property_action)}</span>
        </PopoverTrigger>
        {/* Each option leads with the icon the resulting picker uses, so the
            list reads as a preview of what will show up above. 388px is the
            sidebar's 420px resize limit minus its 16px padding on each side.
            Popover (unlike DropdownMenu) has no height bound of its own, so a
            long property catalog must be capped to the space the positioner
            reports and scroll inside, or its tail renders off-screen. */}
        <PopoverContent
          align="start"
          data-testid="add-property-popover"
          className="max-h-(--available-height) w-max min-w-44 max-w-[min(388px,var(--available-width))] overflow-y-auto overscroll-contain p-1"
        >
          {OPTIONAL_PROP_KEYS.filter((k) => !visibleOptionalProps.has(k) && (k !== "stage" || issue.parent_issue_id != null)).map((k) => (
            <button
              key={k}
              type="button"
              onClick={() => addOptionalProp(k)}
              className="flex w-full items-center gap-2 rounded-md px-2 py-1 text-left text-caption text-foreground transition-colors hover:bg-accent focus-visible:bg-accent focus-visible:outline-none"
            >
              {k === "priority" && (
                <PriorityIcon priority="medium" inheritColor className="text-muted-foreground" />
              )}
              {k === "stage" && (
                <Milestone className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              )}
              {k === "start_date" && (
                <CalendarClock className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              )}
              {k === "due_date" && (
                <CalendarDays className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              )}
              {k === "labels" && (
                <Tag className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              )}
              <span className="truncate">
                {k === "priority" && t(($) => $.detail.prop_priority)}
                {k === "stage" && t(($) => $.detail.prop_stage)}
                {k === "start_date" && t(($) => $.detail.prop_start_date)}
                {k === "due_date" && t(($) => $.detail.prop_due_date)}
                {k === "labels" && t(($) => $.detail.prop_labels)}
              </span>
            </button>
          ))}
          {(() => {
            const addable = workspaceProperties.filter(
              (p) =>
                !p.archived &&
                !visibleCustomProps.has(p.id) &&
                issue.properties?.[p.id] === undefined,
            );
            if (addable.length === 0) return null;
            return (
              <>
                <div className="my-1 h-px bg-border" />
                {addable.map((p) => (
                  <TruncatedTooltip key={p.id} content={p.name}>
                    <button
                      type="button"
                      onClick={() => addCustomProp(p.id)}
                      className="flex w-full items-center gap-2 rounded-md px-2 py-1 text-left text-caption text-foreground transition-colors hover:bg-accent focus-visible:bg-accent focus-visible:outline-none"
                    >
                      {p.icon ? (
                        <PropertyIcon property={p} className="size-3.5 text-caption" />
                      ) : (
                        <SlidersHorizontal className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      )}
                      <span className="line-clamp-2 min-w-0 break-words [overflow-wrap:anywhere]">{p.name}</span>
                    </button>
                  </TruncatedTooltip>
                ))}
              </>
            );
          })()}
        </PopoverContent>
      </Popover>
    </div>
  );

  return (
    <div>
      <IssueSidebarSectionHeader title={t(($) => $.detail.section_properties)} open={propertiesOpen} onOpenChange={setPropertiesOpen}  />
      {propertiesOpen && <IssueSidebarPropertyGrid>
        {/* Core props — always rendered. */}
        <PropRow label={t(($) => $.detail.prop_status)}>
          <StatusPicker
            status={issue.status}
            onUpdate={handleUpdateField}
            align="start"
            projectId={issue.project_id}
            issue={issue}
            onMarkDuplicate={actions.openMarkDuplicate}
            isDuplicate={isDuplicateIssue(issue)}
          />
        </PropRow>
        <PropRow label={t(($) => $.detail.prop_assignee)}>
          <AssigneePicker assigneeType={issue.assignee_type} assigneeId={issue.assignee_id} onUpdate={handleUpdateField} align="start" />
        </PropRow>
        <PropRow label={t(($) => $.detail.prop_project)}>
          <IssueProjectPicker
            projectId={issue.project_id}
            onUpdate={handleUpdateField}
            issue={issue}
          />
        </PropRow>
        <IssueWorkflowPropRow projectId={issue.project_id} status={issue.status} label={t(($) => $.workflows.project.label)} />

        {/* Optional props — rendered only when set on the issue OR added
            via "+ Add property" in this session. Row order follows the
            order of `OPTIONAL_PROP_KEYS`. */}
        {visibleOptionalProps.has("priority") && (
          <PropRow label={t(($) => $.detail.prop_priority)}>
            <PriorityPicker
              priority={issue.priority}
              onUpdate={handleUpdateField}
              align="start"
              defaultOpen={autoOpenProp === "priority"}
            />
          </PropRow>
        )}
        {issue.parent_issue_id != null && visibleOptionalProps.has("stage") && (
          <PropRow label={t(($) => $.detail.prop_stage)}>
            <StagePicker
              stage={issue.stage}
              onUpdate={handleUpdateField}
              maxStage={maxStage}
              align="start"
              defaultOpen={autoOpenProp === "stage"}
            />
          </PropRow>
        )}
        {visibleOptionalProps.has("start_date") && (
          <PropRow label={t(($) => $.detail.prop_start_date)}>
            <StartDatePicker
              startDate={issue.start_date}
              onUpdate={handleUpdateField}
              defaultOpen={autoOpenProp === "start_date"}
            />
          </PropRow>
        )}
        {visibleOptionalProps.has("due_date") && (
          <PropRow label={t(($) => $.detail.prop_due_date)}>
            <DueDatePicker
              dueDate={issue.due_date}
              onUpdate={handleUpdateField}
              defaultOpen={autoOpenProp === "due_date"}
            />
          </PropRow>
        )}
        {visibleOptionalProps.has("labels") && (
          <PropRow label={t(($) => $.detail.prop_labels)}>
            <LabelPicker
              issueId={issue.id}
              align="start"
              defaultOpen={autoOpenProp === "labels"}
            />
          </PropRow>
        )}

        {/* Custom properties — same progressive disclosure as the built-in
            optional props: a row renders when the issue has a value OR the
            reader added the property this session. Archived definitions render
            read-only until their value is cleared. */}
        {workspaceProperties
          .filter(
            (p) =>
              issue.properties?.[p.id] !== undefined ||
              (!p.archived && visibleCustomProps.has(p.id)),
          )
          .map((p) => (
            <PropRow
              key={p.id}
              label={
                <>
                  <PropertyIcon property={p} className="size-3.5 text-caption" />
                  <TruncatedTooltip content={p.name} focusable>
                    <span className="line-clamp-2 min-w-0 break-words [overflow-wrap:anywhere]">{p.name}</span>
                  </TruncatedTooltip>
                </>
              }
            >
              <CustomPropertyValueEditor
                issue={issue}
                property={p}
                defaultOpen={autoOpenCustomProp === p.id}
              />
            </PropRow>
          ))}

        {addPropertyRow}
      </IssueSidebarPropertyGrid>}
    </div>
  );
}

/** The workflow the issue follows through its project (MUL-7420). */
function IssueWorkflowPropRow({
  projectId,
  status,
  label,
}: {
  projectId: string | null;
  status: string;
  label: string;
}) {
  const wsId = useWorkspaceId();
  const workflow = useProjectWorkflow(wsId, projectId);
  if (!workflow) return null;
  return (
    <PropRow label={label}>
      <IssueFlowCard projectId={projectId} currentStatus={status} />
    </PropRow>
  );
}
