"use client";

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { ChevronRight, Pencil, X } from "@multica/ui/icons";
import type { Agent, RoleCompositionPreview } from "@multica/core/types";
import {
  isRoleAgent,
  useAgentRoleTypeEnabled,
  useRolePreview,
} from "@multica/core/agents";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { cn } from "@multica/ui/lib/utils";
import { AppLink } from "../../navigation";
import { ReadonlyContent } from "../../editor/readonly-content";
import {
  ObjectSection,
  ObjectSectionAction,
} from "../../layout/object-page";
import { useT } from "../../i18n";
import { InstructionsTab } from "./tabs/instructions-tab";

function collapsedStorageKey(agentId: string): string {
  return `multica:agent-instructions-collapsed:${agentId}`;
}

function readCollapsed(agentId: string): boolean {
  if (typeof window === "undefined" || !agentId) return false;
  try {
    return window.localStorage.getItem(collapsedStorageKey(agentId)) === "1";
  } catch {
    return false;
  }
}

function writeCollapsed(agentId: string, collapsed: boolean): void {
  try {
    if (collapsed) {
      window.localStorage.setItem(collapsedStorageKey(agentId), "1");
    } else {
      window.localStorage.removeItem(collapsedStorageKey(agentId));
    }
  } catch {
    // A full or blocked store must never break the Agent page.
  }
}

/**
 * The agent's instructions: the prompt as it reads, clamped to a few lines,
 * and the full editor in place of it while editing. The whole section
 * collapses behind its title (chevron + aria-expanded, same affordance as
 * the properties panel), remembered per agent, default expanded. An Agent
 * implementing a Role also shows what it inherits above its own additions.
 */
export function AgentInstructionsSection({
  agent,
  canEdit,
  onUpdate,
}: {
  agent: Agent;
  canEdit: boolean;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
}) {
  const { t } = useT("agents");
  const [editing, setEditing] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  // Effect-read (not a lazy initializer) so the server render and the first
  // client render agree; the stored value applies right after hydration.
  const [collapsed, setCollapsed] = useState(false);

  useEffect(() => {
    setCollapsed(readCollapsed(agent.id));
    setEditing(false);
    setDirty(false);
    // Reset only when moving to another agent; a cache update from the form
    // must not erase a newer local draft while an autosave is in flight.
  }, [agent.id]);

  const toggleCollapsed = () => {
    setCollapsed((previous) => {
      const next = !previous;
      writeCollapsed(agent.id, next);
      return next;
    });
  };

  const startEditing = () => {
    setCollapsed(false);
    writeCollapsed(agent.id, false);
    setEditing(true);
  };

  const stopEditing = () => {
    setEditing(false);
    setDirty(false);
  };

  const wsId = useCurrentWorkspace()?.id ?? "";
  const roleTypeEnabled = useAgentRoleTypeEnabled(wsId);
  const showingRole = roleTypeEnabled && isRoleAgent(agent);
  const inheritedRoleId =
    roleTypeEnabled && !isRoleAgent(agent) ? (agent.implements_role_id ?? null) : null;

  const title = t(($) => $.tabs.instructions);
  const ownInstructions = agent.instructions ?? "";

  return (
    <ObjectSection
      title={
        <button
          type="button"
          onClick={toggleCollapsed}
          aria-expanded={!collapsed}
          className="flex items-center gap-1 rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <ChevronRight
            aria-hidden="true"
            className={cn(
              "!size-3.5 shrink-0 stroke-[2.5] text-muted-foreground transition-transform",
              !collapsed && "rotate-90",
            )}
          />
          {title}
        </button>
      }
      action={
        !canEdit ? null : editing ? (
          <ObjectSectionAction
            icon={X}
            onClick={() => (dirty ? setConfirmDiscard(true) : stopEditing())}
          >
            {t(($) => $.inspector.cancel)}
          </ObjectSectionAction>
        ) : (
          <ObjectSectionAction icon={Pencil} onClick={startEditing}>
            {t(($) => $.sections.edit)}
          </ObjectSectionAction>
        )
      }
    >
      {collapsed ? null : (
        <>
          {inheritedRoleId ? (
            <InheritedRoleBlock agentId={agent.id} roleId={inheritedRoleId} />
          ) : null}
          {showingRole ? (
            <p className="text-body text-muted-foreground">
              {t(($) => $.sections.instructions_role_note)}
            </p>
          ) : null}
          {editing ? (
            <InstructionsTab
              agent={agent}
              onSave={async (updates) => {
                await onUpdate(agent.id, updates);
                stopEditing();
              }}
              onDirtyChange={setDirty}
            />
          ) : inheritedRoleId ? (
            ownInstructions.trim() ? (
              <InstructionsPreview content={ownInstructions} />
            ) : (
              <p className="text-body text-muted-foreground">
                {t(($) => $.sections.instructions_no_additions)}
              </p>
            )
          ) : (
            <InstructionsPreview content={ownInstructions} />
          )}
        </>
      )}

      {confirmDiscard && (
        <AlertDialog
          open
          onOpenChange={(open) => {
            if (!open) setConfirmDiscard(false);
          }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>
                {t(($) => $.tabs.discard_dialog_title)}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {t(($) => $.tabs.discard_dialog_description)}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{t(($) => $.tabs.discard_keep)}</AlertDialogCancel>
              <AlertDialogAction
                variant="destructive"
                onClick={() => {
                  setConfirmDiscard(false);
                  stopEditing();
                }}
              >
                {t(($) => $.tabs.discard_confirm)}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </ObjectSection>
  );
}

/**
 * Read-only inherited view for an Agent implementing a Role: the Role's name
 * (linked), its instructions, and the union of both agents' skills. Loading
 * and error (including a 403 where the caller lost Role access) render as
 * hidden, never partial — same contract as the settings preview.
 */
function InheritedRoleBlock({
  agentId,
  roleId,
}: {
  agentId: string;
  roleId: string;
}) {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  const { data, isLoading, isError } = useRolePreview(agentId, true);
  if (isLoading || isError || !data) return null;
  return (
    <div className="rounded-lg border bg-card px-4 py-3">
      <AppLink
        href={paths.agentDetail(roleId)}
        className="text-caption font-medium text-brand hover:underline"
      >
        {t(($) => $.sections.instructions_inherited_from, {
          name: data.role_name ?? roleId.slice(0, 8),
        })}
      </AppLink>
      {data.role_instructions ? (
        <div className="mt-1.5 text-body text-muted-foreground">
          <ReadonlyContent content={data.role_instructions} />
        </div>
      ) : null}
      <RoleSkills skills={data.skills} />
    </div>
  );
}

function RoleSkills({ skills }: { skills: RoleCompositionPreview["skills"] }) {
  const { t } = useT("agents");
  if (skills.length === 0) return null;
  return (
    <ul className="mt-1.5 space-y-0.5">
      {skills.map((skill) => (
        <li key={skill.id} className="flex items-center gap-1.5 text-caption">
          <span className="rounded-md bg-muted px-1.5 py-0.5">
            {skill.source === "role"
              ? t(($) => $.inspector.role_preview_skill_role)
              : t(($) => $.inspector.role_preview_skill_agent)}
          </span>
          <span className="truncate">{skill.name}</span>
        </li>
      ))}
    </ul>
  );
}

function InstructionsPreview({ content }: { content: string }) {
  const { t } = useT("agents");
  const { t: tc } = useT("common");
  const bodyRef = useRef<HTMLDivElement>(null);
  const [expanded, setExpanded] = useState(false);
  const [overflows, setOverflows] = useState(false);

  // Measured rather than assumed: only a prompt taller than the clamp gets
  // the toggle. Re-measured on resize, because a narrower column wraps more.
  useLayoutEffect(() => {
    const el = bodyRef.current;
    if (!el || expanded) return;
    const measure = () => setOverflows(el.scrollHeight > el.clientHeight + 1);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [content, expanded]);

  if (!content.trim()) {
    return (
      <p className="text-body text-muted-foreground">
        {t(($) => $.sections.instructions_empty)}
      </p>
    );
  }

  return (
    <div className="rounded-lg border bg-card px-4 py-3">
      <div
        ref={bodyRef}
        className={cn("text-body", !expanded && "max-h-24 overflow-hidden")}
      >
        <ReadonlyContent content={content} />
      </div>
      {overflows || expanded ? (
        <button
          type="button"
          aria-expanded={expanded}
          onClick={() => setExpanded((value) => !value)}
          className="mt-1 rounded-sm text-caption font-medium text-brand hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {expanded
            ? tc(($) => $.expandable_description.collapse)
            : tc(($) => $.expandable_description.expand)}
        </button>
      ) : null}
    </div>
  );
}
