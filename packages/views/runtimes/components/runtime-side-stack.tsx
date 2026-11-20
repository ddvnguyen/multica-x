"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentTaskSnapshotOptions } from "@multica/core/agents";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { agentListOptions, memberListOptions } from "@multica/core/workspace/queries";
import { IssueSidebarStack } from "../../issues/components/issue-sidebar-section";
import { MachinePropertiesPanel } from "./machine-properties-panel";
import { buildWorkloadIndex } from "./runtime-list";
import { buildRuntimeMachines, type RuntimeMachine } from "./runtime-machines";
import { machineIcon } from "./shared";

function decodeRouteParam(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

function findMachine(machines: RuntimeMachine[], locator: string): RuntimeMachine | null {
  return (
    machines.find(
      (candidate) =>
        candidate.id === locator ||
        candidate.runtimes.some((runtime) => runtime.id === locator),
    ) ?? null
  );
}

/** Side preview stack for a runtime/machine target (M-336 G6). */
export function RuntimeSideStack({ runtimeId }: { runtimeId: string }) {
  const wsId = useWorkspaceId();
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: tasks = [] } = useQuery(agentTaskSnapshotOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));

  const workloadIndex = useMemo(() => buildWorkloadIndex(agents, tasks), [agents, tasks]);
  const machines = useMemo(
    () => buildRuntimeMachines(runtimes, { now: Date.now(), workloadByRuntimeId: workloadIndex }),
    [runtimes, workloadIndex],
  );
  const locator = decodeRouteParam(runtimeId);
  const machine = findMachine(machines, locator);

  if (!machine) return null;
  const Icon = machineIcon(machine);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div
        className="flex items-baseline gap-2 border-b border-border/60 px-3 py-2"
        data-multica-side-preview-header=""
        title={machine.title}
      >
        <span className="inline-flex min-w-0 items-center gap-1.5">
          <Icon aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground" />
          <span className="truncate text-caption font-medium text-foreground">{machine.title}</span>
        </span>
      </div>
      <IssueSidebarStack>
        <MachinePropertiesPanel machine={machine} members={members} />
      </IssueSidebarStack>
    </div>
  );
}
