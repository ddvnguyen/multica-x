"use client";

import { useT } from "../../../i18n";

/**
 * Role select for the agent inspector (M-423). The role is the agent's
 * position selecting its tier table: the resolver only considers tiers
 * carrying the agent's role. Empty = legacy: no role filtering until set.
 */
const ROLES = [
  "architect",
  "leader",
  "reviewer",
  "orchestrator",
  "engineer",
  "planning",
] as const;

export function RolePicker({
  value,
  canEdit = true,
  onChange,
}: {
  /** Agent role, or "" for legacy (unset). */
  value: string;
  canEdit?: boolean;
  onChange: (role: string) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  if (!canEdit) {
    return (
      <span className="text-sm text-muted-foreground">
        {value || t(($) => $.inspector.role_none)}
      </span>
    );
  }
  return (
    <select
      aria-label={t(($) => $.inspector.prop_role)}
      value={value}
      disabled={!canEdit}
      onChange={(e) => {
        void onChange(e.target.value);
      }}
      className="max-w-44 truncate rounded-md border bg-background px-2 py-1 text-sm"
    >
      <option value="">{t(($) => $.inspector.role_none)}</option>
      {ROLES.map((r) => (
        <option key={r} value={r}>
          {t(($) => $.inspector[`role_${r}`] ?? r)}
        </option>
      ))}
    </select>
  );
}
