"use client";

import type { MemberWithUser } from "@multica/core/types";
import { ActorAvatar } from "../../common/actor-avatar";
import { ObjectPanelSection, ObjectProperty } from "../../layout/object-page";
import { useT, useTimeAgo } from "../../i18n";
import type { RuntimeMachine } from "./runtime-machines";
import { HealthIcon, ProviderIconStack, shortDaemonId, useHealthLabel } from "./shared";

export function MachinePropertiesPanel({
  machine,
  members,
}: {
  machine: RuntimeMachine;
  members: MemberWithUser[];
}) {
  const { t } = useT("runtimes");
  const { t: tLayout } = useT("layout");
  const healthLabel = useHealthLabel();
  const timeAgo = useTimeAgo();
  const ownerId = machine.runtimes[0]?.owner_id ?? null;
  const owner = ownerId
    ? members.find((member) => member.user_id === ownerId) ?? null
    : null;
  const busy = machine.runningCount + machine.queuedCount > 0;
  const daemonShort = shortDaemonId(machine.daemonId);

  return (
    <ObjectPanelSection title={tLayout(($) => $.object_page.properties)}>
      <ObjectProperty label={t(($) => $.page.columns.status)}>
        <HealthIcon health={machine.health} />
        <span className="truncate">
          {healthLabel(machine.health)}
          {machine.lastSeenAt && machine.health !== "online"
            ? ` · ${timeAgo(machine.lastSeenAt)}`
            : null}
        </span>
      </ObjectProperty>
      <ObjectProperty label={t(($) => $.detail.fact_owner)}>
        {owner ? (
          <>
            <ActorAvatar
              actorType="member"
              actorId={owner.user_id}
              size="xs"
              enableHoverCard
            />
            <span className="truncate">{owner.name}</span>
          </>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </ObjectProperty>
      <ObjectProperty label={t(($) => $.detail.fact_device)}>
        <span className={machine.deviceSummary ? "truncate" : "text-muted-foreground"}>
          {machine.deviceSummary ?? "—"}
        </span>
      </ObjectProperty>
      <ObjectProperty label={t(($) => $.page.columns.harness)}>
        {machine.providerNames.length > 0 ? (
          <ProviderIconStack providers={machine.providerNames} />
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </ObjectProperty>
      <ObjectProperty label={t(($) => $.page.columns.workload)}>
        <span className={busy ? "truncate tabular-nums" : "text-muted-foreground"}>
          {busy
            ? t(($) => $.machine.metrics.workload_hint, {
                running: machine.runningCount,
                queued: machine.queuedCount,
              })
            : t(($) => $.machine.metrics.workload_idle)}
        </span>
      </ObjectProperty>
      {daemonShort && (
        <ObjectProperty label={t(($) => $.detail.fact_daemon_id)}>
          <span className="truncate font-mono">{daemonShort}</span>
        </ObjectProperty>
      )}
    </ObjectPanelSection>
  );
}
