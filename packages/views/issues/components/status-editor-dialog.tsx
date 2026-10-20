"use client";

import { useEffect, useState } from "react";
import { toast } from "sonner";
import {
  useCreateIssueStatus,
  useUpdateIssueStatus,
} from "@multica/core/issue-statuses/mutations";
import { normalizeIssueStatusCategory } from "@multica/core/issue-statuses/queries";
import { ALL_STATUSES } from "@multica/core/issues/config";
import { ISSUE_STATUS_ICONS } from "@multica/core/types/issue-status";
import type {
  IssueStatusCategory,
  IssueStatusEntry,
  IssueStatusIcon,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Label as FieldLabel } from "@multica/ui/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { ColorPicker, COLOR_PICKER_PRESETS } from "../../common/color-picker";
import { StatusIcon } from "./status-icon";
import { useT } from "../../i18n";

interface StatusDraft {
  name: string;
  description: string;
  category: IssueStatusCategory;
  color: string;
  icon: IssueStatusIcon | "";
}

const EMPTY_DRAFT: StatusDraft = {
  name: "",
  description: "",
  category: "unstarted",
  color: COLOR_PICKER_PRESETS[6]!,
  icon: "",
};

/**
 * The status editor: one dialog that creates a status and one that edits it.
 *
 * Shared by Settings → Issue Statuses and the board, so a column added from
 * the board is created exactly the way the settings page creates one — same
 * fields, same validation, same server contract (the key is derived from the
 * name, never typed by hand).
 */
export function StatusEditorDialog({
  open,
  onOpenChange,
  category,
  status,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Pre-selected category for a new status; null = the editor picks it. */
  category: IssueStatusCategory | null;
  /** The status being edited; null = create mode. */
  status?: IssueStatusEntry | null;
}) {
  const { t } = useT("settings");
  const create = useCreateIssueStatus();
  const update = useUpdateIssueStatus();
  const [draft, setDraft] = useState<StatusDraft>(EMPTY_DRAFT);
  const [iconChanged, setIconChanged] = useState(false);

  const categoryItems = ALL_STATUSES.map((c) => ({
    value: c,
    label: t(($) => $.issue_statuses.category_labels[c]),
  }));

  useEffect(() => {
    if (!open) return;
    setIconChanged(false);
    setDraft(
      status
        ? {
            name: status.name,
            description: status.description ?? "",
            category: normalizeIssueStatusCategory(status.category) ?? "unstarted",
            color: status.color,
            icon: ISSUE_STATUS_ICONS.includes(status.icon as IssueStatusIcon)
              ? status.icon as IssueStatusIcon : "",
          }
        : { ...EMPTY_DRAFT, category: category ?? "unstarted" },
    );
  }, [status, category, open]);

  const submit = () => {
    const name = draft.name.trim();
    if (!name || create.isPending || update.isPending) return;
    const onError = (error: unknown) =>
      toast.error(
        error instanceof Error ? error.message : t(($) => $.issue_statuses.editor.save_failed),
      );

    if (status) {
      update.mutate(
        {
          id: status.id,
          name,
          description: draft.description.trim(),
          color: draft.color,
          // Preserve an unknown future icon when only other fields are edited.
          ...(iconChanged ? { icon: draft.icon } : {}),
        },
        { onSuccess: () => onOpenChange(false), onError },
      );
      return;
    }
    create.mutate(
      {
        name,
        description: draft.description.trim(),
        category: draft.category,
        color: draft.color,
        icon: draft.icon,
      },
      {
        onSuccess: (created) => {
          onOpenChange(false);
          // The key is derived server-side and is the only handle the API and
          // the CLI accept, so creation has to say what it minted. A name with
          // no ASCII to slug gets one that cannot be guessed back from the name
          // — "客户确认" becomes `started_2` (MUL-6749) — so staying silent
          // would leave the admin no way to learn it short of reopening the
          // row. The dialog is already closing; a toast is the one surface
          // still visible.
          if (created?.key) {
            toast.success(t(($) => $.issue_statuses.editor.created, { key: created.key }));
          }
        },
        onError,
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <form
          className="grid gap-4"
          onSubmit={(event) => {
            event.preventDefault();
            submit();
          }}
        >
          <DialogHeader>
            <DialogTitle>
              {status
                ? t(($) => $.issue_statuses.editor.edit_title)
                : t(($) => $.issue_statuses.editor.create_title)}
            </DialogTitle>
          </DialogHeader>
          <div className="space-y-5 py-2">
            <div className="space-y-2">
              <FieldLabel htmlFor="status-name">
                {t(($) => $.issue_statuses.editor.name)}
              </FieldLabel>
              <Input
                id="status-name"
                type="text"
                autoComplete="off"
                data-1p-ignore
                autoFocus
                maxLength={64}
                value={draft.name}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, name: event.target.value }))
                }
                placeholder={t(($) => $.issue_statuses.editor.name_placeholder)}
              />
              {/* The key is the string the API and the CLI take, and renaming a
                  status does not move it — so it has to be readable somewhere.
                  Here, not as a chip on every row: the list is for scanning
                  names, and a slug beside each one is what turned it into a
                  table of internals. (MUL-6422) */}
              {status && (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.issue_statuses.editor.key_hint, { key: status.key })}
                </p>
              )}
            </div>
            <div className="space-y-2">
              <FieldLabel htmlFor="status-category">{t(($) => $.issue_statuses.editor.category)}</FieldLabel>
              {/* Immutable after creation: changing it would alter the
                  lifecycle of every issue already on this status. */}
              <Select
                items={categoryItems}
                value={draft.category}
                onValueChange={(value) =>
                  value &&
                  setDraft((current) => ({ ...current, category: value as IssueStatusCategory }))
                }
                disabled={Boolean(status)}
              >
                <SelectTrigger id="status-category">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {categoryItems.map((item) => (
                    <SelectItem key={item.value} value={item.value}>
                      {item.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className="text-caption text-muted-foreground">
                {status
                  ? t(($) => $.issue_statuses.editor.category_locked)
                  : t(($) => $.issue_statuses.categories[draft.category])}
              </p>
            </div>
            <div className="space-y-2">
              <FieldLabel htmlFor="status-description">
                {t(($) => $.issue_statuses.editor.description)}
              </FieldLabel>
              <Textarea
                id="status-description"
                rows={3}
                maxLength={256}
                value={draft.description}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, description: event.target.value }))
                }
                onKeyDown={(event) => {
                  if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
                    event.currentTarget.form?.requestSubmit();
                  }
                }}
                placeholder={t(($) => $.issue_statuses.editor.description_placeholder)}
              />
            </div>
            <div className="space-y-2">
              <FieldLabel>{t(($) => $.issue_statuses.editor.color)}</FieldLabel>
              <ColorPicker
                value={draft.color}
                onChange={(color) => setDraft((current) => ({ ...current, color }))}
                trigger={
                  <button
                    type="button"
                    aria-label={t(($) => $.issue_statuses.editor.color)}
                    className="flex h-9 items-center gap-2.5 rounded-md border border-surface-border px-2.5 transition-colors hover:bg-surface-hover"
                  >
                    <StatusIcon
                      status={status?.key ?? ""}
                      category={draft.category}
                      color={draft.color}
                      icon={draft.icon}
                      className="size-5"
                    />
                    <span className="font-mono text-caption uppercase text-muted-foreground">
                      {draft.color}
                    </span>
                  </button>
                }
              />
            </div>
            <fieldset className="space-y-2">
              <legend className="text-caption font-medium">
                {t(($) => $.issue_statuses.editor.icon)}
              </legend>
              <div className="flex flex-wrap gap-2">
                {(["", ...ISSUE_STATUS_ICONS] as const).map((icon) => (
                  <Button
                    key={icon}
                    type="button"
                    variant={draft.icon === icon ? "secondary" : "outline"}
                    size={icon ? "icon" : "default"}
                    className={`${icon ? "size-11" : "h-11"} aria-pressed:ring-2 aria-pressed:ring-ring`}
                    aria-label={t(($) => $.issue_statuses.editor.icon_shapes[icon || "default"])}
                    aria-pressed={draft.icon === icon}
                    title={t(($) => $.issue_statuses.editor.icon_shapes[icon || "default"])}
                    onClick={() => {
                      setIconChanged(true);
                      setDraft((current) => ({ ...current, icon }));
                    }}
                  >
                    {icon
                      ? <StatusIcon status="" category={draft.category} color={draft.color} icon={icon} className="size-5" />
                      : t(($) => $.issue_statuses.editor.icon_shapes.default)}
                  </Button>
                ))}
              </div>
            </fieldset>
          </div>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
              {t(($) => $.issue_statuses.editor.cancel)}
            </Button>
            <Button
              type="submit"
              disabled={!draft.name.trim() || create.isPending || update.isPending}
            >
              {create.isPending || update.isPending
                ? t(($) => $.issue_statuses.editor.saving)
                : t(($) => $.issue_statuses.editor.save)}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
