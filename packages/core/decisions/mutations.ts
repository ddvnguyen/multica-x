import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { decisionKeys } from "./queries";
import { useWorkspaceId } from "../hooks";

export function useRespondDecision() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (input: { issueId: string; client_request_id: string; expected_issue_revision: number; decision: string; rationale?: string; next_status: string }) =>
      api.respondDecision(input.issueId, {
        client_request_id: input.client_request_id,
        expected_issue_revision: input.expected_issue_revision,
        decision: input.decision,
        rationale: input.rationale,
        next_status: input.next_status,
      }),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: decisionKeys.all(wsId) });
    },
  });
}
