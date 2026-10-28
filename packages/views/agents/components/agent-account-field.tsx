"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  ACCOUNT_VALUE_ANY,
  ACCOUNT_VALUE_DEFAULT,
  accountSelectionValue,
  parseAccountSelectionValue,
  runtimeSupportsAccounts,
  selectableAccounts,
  workspaceAccountListOptions,
  type AgentAccountSelection,
} from "@multica/core/runtimes";
import { Label } from "@multica/ui/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { useT } from "../../i18n";

/**
 * Account selector for an agent on a freebuff-desktop runtime: the runtime's
 * own configuration, one named workspace account, or any enabled account.
 *
 * Renders nothing when the runtime cannot run accounts, or when the workspace
 * has none to offer and the agent is not already bound to one — the field only
 * appears once it can matter. The same selection scopes the model catalog the
 * model picker lists (see `accountModelScope`).
 */
export function useAgentAccountOptions(runtimeProvider: string | undefined | null) {
  const wsId = useWorkspaceId();
  const supported = runtimeSupportsAccounts(runtimeProvider);
  const query = useQuery({ ...workspaceAccountListOptions(wsId), enabled: supported });
  return {
    supported,
    all: query.data ?? [],
    selectable: useMemo(() => selectableAccounts(query.data), [query.data]),
  };
}

export function AgentAccountField({
  runtimeProvider,
  value,
  onChange,
  disabled,
  showLabel = true,
}: {
  runtimeProvider: string | undefined | null;
  value: AgentAccountSelection;
  onChange: (next: AgentAccountSelection) => void;
  disabled?: boolean;
  showLabel?: boolean;
}) {
  const { t } = useT("agents");
  const { supported, all, selectable } = useAgentAccountOptions(runtimeProvider);

  const items = useMemo(() => {
    const out = [
      { value: ACCOUNT_VALUE_DEFAULT, label: t(($) => $.account_field.runtime_default) },
      ...selectable.map((a) => ({ value: a.id, label: a.label })),
      { value: ACCOUNT_VALUE_ANY, label: t(($) => $.account_field.any) },
    ];
    // An agent bound to an account that has since been disabled must still
    // show it, or the trigger would render blank and look unset.
    if (value.mode === "specific" && !out.some((i) => i.value === value.accountId)) {
      const bound = all.find((a) => a.id === value.accountId);
      out.splice(1, 0, { value: value.accountId, label: bound?.label ?? value.accountId });
    }
    return out;
  }, [all, selectable, t, value.accountId, value.mode]);

  if (!supported) return null;
  if (selectable.length === 0 && value.mode === "default") return null;

  const current = accountSelectionValue(value);
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      {showLabel && (
        <Label className="text-caption text-muted-foreground">
          {t(($) => $.account_field.label)}
        </Label>
      )}
      <Select
        items={items}
        value={current}
        disabled={disabled}
        onValueChange={(next) => {
          if (typeof next === "string" && next !== current) {
            onChange(parseAccountSelectionValue(next));
          }
        }}
      >
        <SelectTrigger aria-label={t(($) => $.account_field.label)}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {items.map((item) => (
            <SelectItem key={item.value} value={item.value}>
              {item.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {showLabel && (
        <p className="text-caption text-muted-foreground">{t(($) => $.account_field.hint)}</p>
      )}
    </div>
  );
}
