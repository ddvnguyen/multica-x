"use client";

import { forwardRef, useImperativeHandle, useLayoutEffect, useRef, type KeyboardEvent, type TextareaHTMLAttributes } from "react";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

// The ask/plan free-text field, shared by every surface that answers a parked
// run: `AgentQuestionCard` (thread, Inbox, Decisions) and the plan panel's
// comment + request-changes note (M-361).
//
// A single-line input scrolled sideways past a few words, so this is a
// textarea that grows with its content: 2 rows to start, capped at ~10 rows,
// then scrolls. Enter inserts a newline (Enter is paragraph here, not send);
// Cmd/Ctrl+Enter submits, matching the plan editors elsewhere in this repo.
//
// `maxLength` still caps the value (server: 2000/card answer, 4000/plan
// comment+note). `showCount` renders `n / max` under the field; pass `max`
// only when you pass `showCount`.
//
// Auto-resize is `field-sizing: content` first (the ui Textarea's own class,
// Chromium/Safari, zero JS) with a scrollHeight fallback for engines without
// it (Firefox, jsdom): measure on each value change and clamp between
// minRows and maxRows.
const MIN_ROWS = 2;
const MAX_ROWS = 10;

type AskFreeTextProps = Omit<
  TextareaHTMLAttributes<HTMLTextAreaElement>,
  "rows" | "onKeyDown"
> & {
  onSubmit?: () => void;
  hint?: string;
  showCount?: boolean;
  max?: number;
  onKeyDown?: (event: KeyboardEvent<HTMLTextAreaElement>) => void;
};

// forwardRef (not ref-as-prop): the fork pins React 18, and ref-as-prop
// needs React 19.
export const AskFreeText = forwardRef<HTMLTextAreaElement, AskFreeTextProps>(function AskFreeText(
  {
    value,
    onSubmit,
    hint,
    showCount = false,
    max,
    className,
    onKeyDown,
    id,
    ...props
  },
  outerRef,
) {
  const innerRef = useRef<HTMLTextAreaElement | null>(null);
  useImperativeHandle(outerRef, () => innerRef.current as HTMLTextAreaElement);

  // Clamp the height between minRows and maxRows of the field's own line
  // height, so growth tracks the theme's font rather than a pixel guess.
  useLayoutEffect(() => {
    const node = innerRef.current;
    if (node === null || typeof window === "undefined") return;
    const lineHeight =
      parseFloat(window.getComputedStyle(node).lineHeight || "") || 16;
    const min = lineHeight * MIN_ROWS;
    const limit = lineHeight * MAX_ROWS;
    node.style.height = "auto";
    node.style.height = `${Math.min(Math.max(node.scrollHeight, min), limit)}px`;
    node.style.overflowY = node.scrollHeight > limit ? "auto" : "hidden";
  }, [value]);

  const length = typeof value === "string" ? [...value].length : 0;
  const over = max !== undefined && length > max;
  const hintId = id !== undefined ? `${id}-hint` : undefined;

  return (
    <div className="min-w-0">
      <Textarea
        {...props}
        id={id}
        ref={innerRef}
        value={value}
        rows={MIN_ROWS}
        maxLength={max}
        aria-describedby={hint !== undefined && hintId !== undefined ? hintId : undefined}
        onKeyDown={(event) => {
          onKeyDown?.(event);
          if (event.defaultPrevented || event.nativeEvent.isComposing) return;
          if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
            event.preventDefault();
            onSubmit?.();
          }
        }}
        className={cn("min-h-0 resize-none text-caption", className)}
      />
      {(hint !== undefined || (showCount && max !== undefined)) && (
        <div className="mt-0.5 flex items-baseline justify-between gap-2 text-caption text-muted-foreground">
          <span id={hintId} className="min-w-0">
            {hint}
          </span>
          {showCount && max !== undefined && (
            <span
              aria-live="off"
              className={cn("shrink-0 tabular-nums", over && "text-destructive")}
            >
              {length} / {max}
            </span>
          )}
        </div>
      )}
    </div>
  );
});

// The shortcut hint under the field: "⌘/Ctrl+Enter to send". Locale-owned
// like the plan editors' `edit_shortcut`, not hardcoded.
export function AskSubmitHint({ id }: { id?: string }) {
  const { t } = useT("issues");
  return (
    <span id={id} className="text-caption text-muted-foreground">
      {t(($) => $.agent_question.submit_shortcut)}
    </span>
  );
}
