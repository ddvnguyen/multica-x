"use client";

import { useMemo, useState } from "react";
import { Lock, UserMinus } from "@multica/ui/icons";
import type { Agent, IssueAssigneeType, UpdateIssueRequest } from "@multica/core/types";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { isAgentRuntimeBound } from "@multica/core/agents";
import { canAssignAgentToIssue } from "@multica/core/permissions";
import { useActorName } from "@multica/core/workspace/hooks";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  memberListOptions,
  agentListOptions,
  squadListOptions,
  assigneeFrequencyOptions,
  modelTiersOptions,
} from "@multica/core/workspace/queries";
import { ActorAvatar } from "../../../common/actor-avatar";
import { DeferredPopup } from "../../../common/deferred-popup";
import {
  PropertyPicker,
  PickerPanel,
  PickerItem,
  PickerSection,
  PickerEmpty,
  PICKER_TRIGGER_CLASS,
} from "./property-picker";
import { useT } from "../../../i18n";
import { matchesPinyin } from "../../../editor/extensions/pinyin-match";
import { TierPickFlow } from "./tier-pick-flow";

/**
 * Legacy boolean shape kept around for callers (e.g. `use-issue-actions.ts`)
 * that haven't migrated to the new `canAssignAgentToIssue` Decision API yet.
 * Internally redirects to the canonical rule so behaviour stays in sync.
 */
export function canAssignAgent(
  agent: Agent,
  userId: string | undefined,
  memberRole: string | undefined,
): boolean {
  return canAssignAgentToIssue(agent, {
    userId: userId ?? null,
    role: memberRole === "owner" || memberRole === "admin" || memberRole === "member"
      ? memberRole
      : null,
  }).allowed;
}

interface AssigneePickerProps {
  assigneeType: IssueAssigneeType | null;
  assigneeId: string | null;
  /**
   * `true` when a batch selection spans different assignees ("mixed"): no row
   * is checked, including the unassigned row. Distinct from `assigneeType` /
   * `assigneeId` both being `null`, which means every selected issue is
   * genuinely unassigned and the unassigned row should be checked.
   */
  mixed?: boolean;
  onUpdate: (updates: Partial<UpdateIssueRequest>) => void;
  trigger?: React.ReactNode;
  triggerRender?: React.ReactElement<Record<string, unknown>>;
  /** Hover tooltip naming the property; hidden while the picker is open. */
  tooltip?: React.ReactNode;
  open?: boolean;
  onOpenChange?: (v: boolean) => void;
  align?: "start" | "center" | "end";
  /**
   * M-359: when true, picking an agent advances to a tier step (Auto default
   * + enabled tiers) instead of confirming immediately; the confirm sends
   * `{assignee_type, assignee_id, tier_id?}` in one update. Member/squad and
   * unassigned picks still confirm immediately.
   */
  tierPick?: boolean;
}

/**
 * Mounting the real picker subscribes to members/agents/squads/frequency
 * queries — multiplied per board card / list row that cost froze tab
 * switches. Uncontrolled callers that bring their own trigger content get a
 * deferred lookalike trigger instead; the picker mounts on first interaction.
 * The default trigger needs `getActorName` (a members/agents subscription
 * itself), so trigger-less callers stay eager.
 */
export function AssigneePicker(props: AssigneePickerProps) {
  const hasDeferredTriggerContent =
    props.trigger !== undefined || props.triggerRender?.props.children != null;
  const canDefer =
    props.open === undefined &&
    props.onOpenChange === undefined &&
    hasDeferredTriggerContent;
  if (!canDefer) {
    return <AssigneePickerImpl {...props} />;
  }
  return (
    <DeferredPopup
      trigger={props.trigger}
      triggerRender={props.triggerRender}
      triggerClassName={PICKER_TRIGGER_CLASS}
      tooltip={props.tooltip}
    >
      {(open, onOpenChange) => (
        <AssigneePickerImpl {...props} open={open} onOpenChange={onOpenChange} />
      )}
    </DeferredPopup>
  );
}

function AssigneePickerImpl({
  assigneeType,
  assigneeId,
  mixed = false,
  onUpdate,
  trigger: customTrigger,
  triggerRender,
  tooltip,
  open: controlledOpen,
  onOpenChange: controlledOnOpenChange,
  align,
  tierPick = false,
}: AssigneePickerProps) {
  const { t } = useT("issues");
  const [internalOpen, setInternalOpen] = useState(false);
  const open = controlledOpen ?? internalOpen;
  const setOpen = controlledOnOpenChange ?? setInternalOpen;
  const [filter, setFilter] = useState("");
  const choices = useAssigneeChoices(filter);
  const { getActorName } = useActorName();

  const triggerLabel =
    assigneeType && assigneeId
      ? getActorName(assigneeType, assigneeId)
      : t(($) => $.pickers.assignee.trigger_unassigned);

  return (
    <PropertyPicker
      open={open}
      onOpenChange={(v: boolean) => {
        setOpen(v);
        if (!v) setFilter("");
      }}
      width="w-64"
      align={align}
      searchable
      searchPlaceholder={t(($) => $.pickers.assignee.search_placeholder)}
      onSearchChange={setFilter}
      triggerRender={triggerRender}
      tooltip={tooltip}
      trigger={
        customTrigger ? customTrigger : assigneeType && assigneeId ? (
          <>
            <ActorAvatar actorType={assigneeType} actorId={assigneeId} size="sm" enableHoverCard showStatusDot />
            <span className="truncate">{triggerLabel}</span>
          </>
        ) : (
          <span className="text-muted-foreground">{t(($) => $.pickers.assignee.trigger_unassigned)}</span>
        )
      }
    >
      <AssigneeOptions
        choices={choices}
        filter={filter}
        assigneeType={assigneeType}
        assigneeId={assigneeId}
        mixed={mixed}
        tierPick={tierPick}
        onPick={(updates) => {
          onUpdate(updates);
          setOpen(false);
        }}
      />
    </PropertyPicker>
  );
}

/**
 * The assignee picker's search box and list without the popover, for hosting
 * inside a menu. `onPicked` runs after the choice is applied so the host can
 * close itself.
 */
export function AssigneePickerPanel({
  assigneeType,
  assigneeId,
  onUpdate,
  onPicked,
}: {
  assigneeType: IssueAssigneeType | null;
  assigneeId: string | null;
  onUpdate: (updates: Partial<UpdateIssueRequest>) => void;
  onPicked: () => void;
}) {
  const { t } = useT("issues");
  const [filter, setFilter] = useState("");
  const choices = useAssigneeChoices(filter);

  return (
    <PickerPanel
      searchable
      autoFocusSearch
      searchPlaceholder={t(($) => $.pickers.assignee.search_placeholder)}
      onSearchChange={setFilter}
    >
      <AssigneeOptions
        choices={choices}
        filter={filter}
        assigneeType={assigneeType}
        assigneeId={assigneeId}
        tierPick={false}
        onPick={(updates) => {
          onUpdate(updates);
          onPicked();
        }}
      />
    </PickerPanel>
  );
}

function useAssigneeChoices(filter: string) {
  const user = useAuthStore((s) => s.user);
  const wsId = useWorkspaceId();
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: squads = [] } = useQuery(squadListOptions(wsId));
  const { data: frequency = [] } = useQuery(assigneeFrequencyOptions(wsId));

  const currentMember = members.find((m) => m.user_id === user?.id);
  const memberRole = currentMember?.role;

  // Build a lookup map from frequency data for sorting.
  const freqMap = useMemo(() => {
    const map = new Map<string, number>();
    for (const entry of frequency) {
      map.set(`${entry.assignee_type}:${entry.assignee_id}`, entry.frequency);
    }
    return map;
  }, [frequency]);

  const getFreq = (type: string, id: string) => freqMap.get(`${type}:${id}`) ?? 0;

  const query = filter.trim().toLowerCase();
  const filteredMembers = members
    .filter((m) => m.name.toLowerCase().includes(query) || matchesPinyin(m.name, query))
    .sort((a, b) => getFreq("member", b.user_id) - getFreq("member", a.user_id));
  const filteredAgents = agents
    .filter((a) => !a.archived_at && (a.name.toLowerCase().includes(query) || matchesPinyin(a.name, query)))
    .sort((a, b) => getFreq("agent", b.id) - getFreq("agent", a.id));
  const filteredSquads = squads
    .filter((s) => !s.archived_at && (s.name.toLowerCase().includes(query) || matchesPinyin(s.name, query)))
    .sort((a, b) => getFreq("squad", b.id) - getFreq("squad", a.id));
  const runnableAgentIds = new Set(
    agents
      .filter((agent) => !agent.archived_at && isAgentRuntimeBound(agent))
      .map((agent) => agent.id),
  );
  const { data: presets = [] } = useQuery({
    queryKey: ["agent-presets", wsId],
    queryFn: () => api.listAgentPresets(wsId),
    enabled: wsId !== "",
  });
  const filteredPresets = presets.filter((preset) =>
    preset.name.toLowerCase().includes(query) || preset.agent_name.toLowerCase().includes(query),
  );

  return {
    userId: user?.id ?? null,
    memberRole,
    agents,
    filteredMembers,
    filteredAgents,
    filteredSquads,
    runnableAgentIds,
    filteredPresets,
  };
}

function AssigneeOptions({
  choices,
  filter,
  assigneeType,
  assigneeId,
  mixed = false,
  tierPick = false,
  onPick,
}: {
  choices: ReturnType<typeof useAssigneeChoices>;
  filter: string;
  assigneeType: IssueAssigneeType | null;
  assigneeId: string | null;
  mixed?: boolean;
  tierPick?: boolean;
  onPick: (updates: Partial<UpdateIssueRequest>) => void;
}) {
  const [tierAgent, setTierAgent] = useState<{ id: string; name: string } | null>(null);
  const { t } = useT("issues");
  const { userId, memberRole, agents, filteredMembers, filteredAgents, filteredSquads, runnableAgentIds, filteredPresets } =
    choices;

  const isSelected = (type: string, id: string) =>
    assigneeType === type && assigneeId === id;

  if (tierAgent) {
    // M-493: the tier step only offers tiers the chosen agent supports
    // (`agent_tier`, exposed as `Agent.tier_ids`). An agent with no selection
    // keeps the legacy behaviour of offering every enabled tier.
    const supportedTierIds = agents.find((agent) => agent.id === tierAgent.id)?.tier_ids;
    return (
      <TierStep
        agentName={tierAgent.name}
        supportedTierIds={supportedTierIds}
        onBack={() => setTierAgent(null)}
        onPick={(tierId) =>
          onPick({
            assignee_type: "agent",
            assignee_id: tierAgent.id,
            ...(tierId ? { tier_id: tierId } : {}),
          })
        }
      />
    );
  }

  return (
    <>
      {/* Unassigned — always the first row, search active or not. Every
          picker in the app puts the empty value there, so "clear this field"
          never moves. */}
      <PickerItem
        emptyValue
        selected={!mixed && !assigneeType && !assigneeId}
        onClick={() => onPick({ assignee_type: null, assignee_id: null })}
      >
        <UserMinus className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-muted-foreground">{t(($) => $.pickers.assignee.trigger_unassigned)}</span>
      </PickerItem>

      {/* Members */}
      {filteredMembers.length > 0 && (
        <PickerSection label={t(($) => $.pickers.assignee.members_group)}>
          {filteredMembers.map((m) => (
            <PickerItem
              key={m.user_id}
              selected={isSelected("member", m.user_id)}
              onClick={() =>
                onPick({
                  assignee_type: "member",
                  assignee_id: m.user_id,
                })
              }
            >
              <ActorAvatar actorType="member" actorId={m.user_id} size="sm" />
              <span className="truncate">{m.name}</span>
            </PickerItem>
          ))}
        </PickerSection>
      )}

      {/* Agents */}
      {filteredPresets.length > 0 && (
        <PickerSection label={t(($) => $.pickers.assignee.agents_group)}>
          {filteredPresets.map((preset) => {
            const agent = agents.find((candidate) => candidate.id === preset.agent_id && !candidate.archived_at);
            const decision = agent ? canAssignAgentToIssue(agent, {
              userId,
              role: memberRole === "owner" || memberRole === "admin" || memberRole === "member" ? memberRole : null,
            }) : null;
            const allowed = Boolean(agent && decision?.allowed && preset.tier_enabled);
            return (
              <PickerItem
                key={`preset-${preset.id}`}
                selected={assigneeType === "agent" && assigneeId === preset.agent_id}
                disabled={!allowed}
                tooltip={!preset.tier_enabled ? undefined : decision?.message}
                onClick={() => {
                  if (!allowed) return;
                  onPick({ assignee_type: "agent", assignee_id: preset.agent_id, preset_id: preset.id });
                }}
              >
                <ActorAvatar actorType="agent" actorId={preset.agent_id} size="sm" showStatusDot />
                <span className="truncate">{preset.name}</span>
              </PickerItem>
            );
          })}
        </PickerSection>
      )}

      {filteredAgents.length > 0 && (
        <PickerSection label={t(($) => $.pickers.assignee.agents_group)}>
          {filteredAgents.map((a) => {
            const decision = canAssignAgentToIssue(a, {
              userId,
              role:
                memberRole === "owner" ||
                memberRole === "admin" ||
                memberRole === "member"
                  ? memberRole
                  : null,
            });
            const runtimeBound = isAgentRuntimeBound(a);
            const allowed = decision.allowed && runtimeBound;
            return (
              <PickerItem
                key={a.id}
                selected={isSelected("agent", a.id)}
                disabled={!allowed}
                tooltip={
                  !decision.allowed
                    ? decision.message
                    : !runtimeBound
                      ? t(($) => $.pickers.assignee.agent_runtime_required)
                      : undefined
                }
                onClick={() => {
                  if (!allowed) return;
                  if (tierPick) {
                    setTierAgent({ id: a.id, name: a.name });
                    return;
                  }
                  onPick({
                    assignee_type: "agent",
                    assignee_id: a.id,
                  });
                }}
              >
                <ActorAvatar actorType="agent" actorId={a.id} size="sm" showStatusDot />
                <span className={`truncate ${allowed ? "" : "text-muted-foreground"}`}>{a.name}</span>
                {a.visibility === "private" && (
                  <Lock className="ml-auto h-3 w-3 text-muted-foreground" />
                )}
              </PickerItem>
            );
          })}
        </PickerSection>
      )}

      {/* Squads — group ownership; assigning to a squad routes the issue to
          its leader agent on the backend. */}
      {filteredSquads.length > 0 && (
        <PickerSection label={t(($) => $.pickers.assignee.squads_group)}>
          {filteredSquads.map((s) => {
            const runtimeBound = runnableAgentIds.has(s.leader_id);
            return (
              <PickerItem
                key={s.id}
                selected={isSelected("squad", s.id)}
                disabled={!runtimeBound}
                tooltip={
                  runtimeBound
                    ? undefined
                    : t(($) => $.pickers.assignee.squad_runtime_required)
                }
                onClick={() => {
                  if (!runtimeBound) return;
                  onPick({
                    assignee_type: "squad",
                    assignee_id: s.id,
                  });
                }}
              >
                <ActorAvatar actorType="squad" actorId={s.id} size="sm" />
                <span className="truncate">{s.name}</span>
              </PickerItem>
            );
          })}
        </PickerSection>
      )}

      {filteredMembers.length === 0 &&
        filteredAgents.length === 0 &&
        filteredSquads.length === 0 &&
        filter && <PickerEmpty />}
    </>
  );
}

/**
 * TierStep is the second step of the assignee picker: after an agent is
 * chosen, the sender optionally pins one tier for this run (Auto default =
 * the idx-ranked resolver). M-493 narrows the list to the tiers that agent
 * supports and walks model → harness → runtime through the tier's members;
 * the leaf shows the tier config (`#name`, harness/model, runtime) before the
 * pick is written back as one `tier_id` update. Disabled tiers stay hidden —
 * picking one would only 409 at send.
 */
function TierStep({
  agentName,
  supportedTierIds,
  onBack,
  onPick,
}: {
  agentName: string;
  supportedTierIds?: string[];
  onBack: () => void;
  onPick: (tierId: string | null) => void;
}) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const { data: tiers = [] } = useQuery(modelTiersOptions(wsId));

  // An agent with an explicit tier selection only sees those tiers; an agent
  // without one keeps the legacy behaviour of every enabled tier.
  const eligible = useMemo(() => {
    const enabled = tiers.filter((tier) => tier.enabled);
    if (!supportedTierIds || supportedTierIds.length === 0) return enabled;
    const allowed = new Set(supportedTierIds);
    return enabled.filter((tier) => allowed.has(tier.id));
  }, [tiers, supportedTierIds]);

  return (
    <>
      <PickerItem selected={false} onClick={onBack}>
        <span className="text-muted-foreground">← {agentName}</span>
      </PickerItem>
      <PickerSection label={t(($) => $.pickers.tier.tier_group)}>
        <TierPickFlow
          tiers={eligible}
          onPick={onPick}
          onAuto={() => onPick(null)}
        />
      </PickerSection>
    </>
  );
}
