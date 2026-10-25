"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, CircleHelp } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { issueKeys, issueOpenQuestionsOptions } from "@multica/core/issues/queries";
import type { Question } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useLocale, useT } from "../../i18n";

// The owner-facing half of structured questions (M-81).
//
// A run whose tool call is parked on a decision writes a question record; this
// card renders the open ones so the issue itself says "someone is waiting on
// you" instead of looking stuck. Answering here and answering from
// `multica question answer` write the same row, so whichever path is used the
// waiting tool call resumes — and `answered_by` records who decided.
//
// Only open questions are fetched: a settled one has nothing to answer, and
// the section disappears entirely once the last one is answered or expires.

export function OpenQuestionsSection({ issueId }: { issueId: string }) {
  const { t } = useT("issues");
  const [open, setOpen] = useState(true);
  const { data: questions = [] } = useQuery(issueOpenQuestionsOptions(issueId));

  if (questions.length === 0) return null;

  return (
    <div>
      <button
        type="button"
        className={`flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium transition-colors mb-2 hover:bg-accent/70 ${open ? "" : "text-muted-foreground hover:text-foreground"}`}
        onClick={() => setOpen(!open)}
      >
        <CircleHelp className="size-3.5 shrink-0 text-warning" />
        {t(($) => $.questions.section_title)}{" "}
        <span className="rounded-xs bg-warning/15 px-1 text-micro font-medium tabular-nums text-warning">
          {questions.length}
        </span>
        <ChevronRight
          className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${open ? "rotate-90" : ""}`}
        />
      </button>
      {open && (
        <div className="space-y-2 pl-2">
          {questions.map((question) => (
            <QuestionCard key={question.id} question={question} />
          ))}
        </div>
      )}
    </div>
  );
}

function QuestionCard({ question }: { question: Question }) {
  const { t } = useT("issues");
  const locale = useLocale();
  const qc = useQueryClient();
  // Multi-select questions accumulate a set and submit once; single-select
  // ones answer on the click, because that click already IS the decision.
  const [selected, setSelected] = useState<string[]>([]);
  const [text, setText] = useState("");

  const answer = useMutation({
    mutationFn: (value: string) => api.answerQuestion(question.id, value),
    onSuccess: () => {
      toast.success(t(($) => $.questions.answered_toast));
      void qc.invalidateQueries({ queryKey: issueKeys.questionsAll() });
    },
    onError: (err) => {
      // The server rejects a stale answer with a 409 whose message ("question
      // has expired") is more useful than a generic failure, so surface it.
      const detail = err instanceof Error ? err.message : "";
      toast.error(detail || t(($) => $.questions.answer_failed));
      void qc.invalidateQueries({ queryKey: issueKeys.questionsAll() });
    },
  });

  const submit = (value: string) => {
    const trimmed = value.trim();
    if (!trimmed || answer.isPending) return;
    answer.mutate(trimmed);
  };

  const toggle = (label: string) => {
    setSelected((current) =>
      current.includes(label) ? current.filter((l) => l !== label) : [...current, label],
    );
  };

  const deadline = new Date(question.expires_at);

  return (
    <div className="rounded-md border border-warning/30 bg-warning/5 px-2.5 py-2">
      {question.header && (
        <div className="text-micro font-medium uppercase tracking-wide text-muted-foreground">
          {question.header}
        </div>
      )}
      <div className="text-caption font-medium">{question.prompt}</div>

      {question.options.length > 0 && (
        <div className="mt-1.5 flex flex-col gap-1">
          {question.options.map((option) => {
            const active = selected.includes(option.label);
            return (
              <Button
                key={option.label}
                type="button"
                variant={active ? "secondary" : "outline"}
                size="sm"
                aria-pressed={question.multi_select ? active : undefined}
                disabled={answer.isPending}
                className="h-auto w-full justify-start whitespace-normal py-1 text-left text-caption"
                title={option.description}
                onClick={() =>
                  question.multi_select ? toggle(option.label) : submit(option.label)
                }
              >
                {option.label}
                {option.description && (
                  <span className="text-muted-foreground"> — {option.description}</span>
                )}
              </Button>
            );
          })}
          {question.multi_select && (
            <Button
              type="button"
              size="sm"
              className="self-start"
              disabled={selected.length === 0 || answer.isPending}
              onClick={() => submit(selected.join(", "))}
            >
              {t(($) => $.questions.submit)}
            </Button>
          )}
        </div>
      )}

      <div className="mt-1.5 flex items-center gap-1">
        <Input
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") submit(text);
          }}
          placeholder={t(($) => $.questions.other_placeholder)}
          disabled={answer.isPending}
          className="h-7 text-caption"
        />
        <Button
          type="button"
          size="sm"
          variant="ghost"
          disabled={!text.trim() || answer.isPending}
          onClick={() => submit(text)}
        >
          {t(($) => $.questions.submit)}
        </Button>
      </div>

      <div className="mt-1 text-micro text-muted-foreground">
        {t(($) => $.questions.expires_at, {
          time: deadline.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" }),
        })}
      </div>
    </div>
  );
}
