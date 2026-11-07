"use client";

import { useRef } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { decisionKeys } from "./queries";
import { useWorkspaceId } from "../hooks";
import { issueKeys } from "../issues/queries";
import { createSafeId } from "../utils";

/**
 * Record a member's answer to a workflow handoff (M-209 endpoint, M-274 form).
 *
 * The request carries three fences the server checks in one transaction: the
 * issue must still be `wait_for_decision`, it must still be at the revision the
 * member was reading, and the destination must leave the waiting step. The
 * `client_request_id` is minted here, once per intent: a dropped response
 * retries the receipt the server already created instead of recording the
 * decision twice.
 */
export interface RespondToDecisionInput {
  issueId: string;
  /** The row's `issue_revision` — what the member was looking at. */
  expected_issue_revision: number;
  /** The chosen option's label, or `custom` for a written answer. */
  decision: string;
  rationale?: string;
  /** The status the issue moves to; never the waiting step itself. */
  next_status: string;
}

const intentKey = (input: RespondToDecisionInput): string =>
  JSON.stringify([
    input.issueId,
    input.expected_issue_revision,
    input.decision,
    input.rationale ?? "",
    input.next_status,
  ]);

const isConflict = (error: unknown): boolean =>
  typeof error === "object" &&
  error !== null &&
  (error as { status?: unknown }).status === 409;

export function useRespondDecision() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  // Ids outlive a failed attempt on purpose: a transport that dropped after the
  // server committed must resolve to that same receipt. A 409 settles the
  // intent instead — the issue moved on, so the next submission is a new one.
  const requests = useRef(new Map<string, string>());
  return useMutation({
    mutationFn: (input: RespondToDecisionInput) => {
      const key = intentKey(input);
      const requestId = requests.current.get(key) ?? createSafeId();
      requests.current.set(key, requestId);
      return api.respondDecision(input.issueId, {
        client_request_id: requestId,
        expected_issue_revision: input.expected_issue_revision,
        decision: input.decision,
        rationale: input.rationale,
        next_status: input.next_status,
      });
    },
    onSuccess: (_receipt, input) => {
      requests.current.delete(intentKey(input));
    },
    onError: (error, input) => {
      if (isConflict(error)) requests.current.delete(intentKey(input));
    },
    onSettled: (_data, _error, input) => {
      void qc.invalidateQueries({ queryKey: decisionKeys.all(wsId) });
      void qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
      void qc.invalidateQueries({ queryKey: issueKeys.timeline(input.issueId) });
    },
  });
}
