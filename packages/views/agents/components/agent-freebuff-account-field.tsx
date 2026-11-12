"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { freebuffAccountsOptions } from "@multica/core/runtimes";
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
 * The Freebuff-CLI pick stored on an agent's `runtime_config`:
 * `{"freebuff":{"account_id": "<id>"}}`; `""` means the runtime default.
 */
export function agentFreebuffAccountId(
  config: Record<string, unknown> | null | undefined,
): string {
  const block = config?.freebuff as { account_id?: unknown } | undefined;
  return typeof block?.account_id === "string" ? block.account_id : "";
}

/**
 * Writes the pick into a full `runtime_config`, replacing only
 * `freebuff.account_id` and preserving every other key (including the rest of
 * the `freebuff` block, which carries server-managed tokens).
 */
export function setAgentFreebuffAccountId(
  config: Record<string, unknown> | null | undefined,
  accountId: string,
): Record<string, unknown> {
  const existing = (config?.freebuff ?? {}) as Record<string, unknown>;
  return { ...(config ?? {}), freebuff: { ...existing, account_id: accountId } };
}

/**
 * Account selector for an agent on a Freebuff-CLI (`freebuff`) runtime: which
 * of the runtime's connected Freebuff-CLI accounts runs the agent, or the
 * runtime's own default account. Mirrors `AgentAccountField`, which serves the
 * same role for freebuff-desktop runtimes.
 *
 * Renders nothing on other providers, and nothing on a Freebuff-CLI runtime
 * while it has no connected account to offer and the agent is not already
 * bound to one — the field only appears once it can matter.
 */
export function AgentFreebuffAccountField({
  runtimeProvider,
  runtimeId,
  value,
  onChange,
  disabled,
  showLabel = true,
}: {
  runtimeProvider: string | undefined | null;
  runtimeId: string | null | undefined;
  /** Selected Freebuff-CLI account id, or "" for the runtime default. */
  value: string;
  onChange: (next: string) => void;
  disabled?: boolean;
  showLabel?: boolean;
}) {
  const { t } = useT("agents");
  const supported = runtimeProvider === "freebuff";
  const query = useQuery({ ...freebuffAccountsOptions(runtimeId ?? ""), enabled: supported && !!runtimeId });

  const accounts = useMemo(() => query.data?.accounts ?? [], [query.data]);
  const connected = useMemo(
    () => accounts.filter((a) => a.state === "connected"),
    [accounts],
  );

  const items = useMemo(() => {
    const out = [
      { value: "", label: t(($) => $.freebuff_account_field.runtime_default) },
      ...connected.map((a) => ({ value: a.id, label: a.label?.trim() || a.id })),
    ];
    // An agent bound to an account that has since been disconnected or
    // removed must still show it, or the trigger would render blank and
    // look unset.
    if (value && !out.some((i) => i.value === value)) {
      const bound = accounts.find((a) => a.id === value);
      out.splice(1, 0, { value, label: bound?.label?.trim() || value });
    }
    return out;
  }, [accounts, connected, t, value]);

  if (!supported) return null;
  if (connected.length === 0 && !value) return null;

  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      {showLabel && (
        <Label className="text-caption text-muted-foreground">
          {t(($) => $.freebuff_account_field.label)}
        </Label>
      )}
      <Select
        items={items}
        value={value}
        disabled={disabled}
        onValueChange={(next) => {
          if (typeof next === "string" && next !== value) {
            onChange(next);
          }
        }}
      >
        <SelectTrigger aria-label={t(($) => $.freebuff_account_field.label)}>
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
        <p className="text-caption text-muted-foreground">
          {t(($) => $.freebuff_account_field.hint)}
        </p>
      )}
    </div>
  );
}
