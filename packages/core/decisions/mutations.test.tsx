/** @vitest-environment jsdom */
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { expect, it, vi } from "vitest";
import { ApiError, setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import { issueKeys } from "../issues/queries";
import { decisionKeys } from "./queries";
import { useRespondDecision } from "./mutations";

vi.mock("../hooks", () => ({ useWorkspaceId: () => "ws-1" }));

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

function wrapperFor(qc: QueryClient) {
  function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  }
  return Wrapper;
}

const INTENT = {
  issueId: "issue-1",
  expected_issue_revision: 3,
  decision: "Approve",
  rationale: "",
  next_status: "in_progress",
};

it("submits the answer and invalidates decisions, issue data and its timeline", async () => {
  const qc = new QueryClient();
  const keys = [decisionKeys.all("ws-1"), issueKeys.detail("ws-1", "issue-1"), issueKeys.list("ws-1"), issueKeys.timeline("issue-1")];
  for (const key of keys) qc.setQueryData(key, {});
  const respondDecision = vi.fn().mockResolvedValue({ id: "receipt" });
  setApiInstance({ respondDecision } as unknown as ApiClient);
  const { result } = renderHook(() => useRespondDecision(), { wrapper: wrapperFor(qc) });
  await act(async () => { await result.current.mutateAsync(INTENT); });
  const [issueId, body] = respondDecision.mock.calls[0] as unknown as [string, Record<string, unknown>];
  expect(issueId).toBe("issue-1");
  expect(body).toMatchObject({
    expected_issue_revision: 3,
    decision: "Approve",
    next_status: "in_progress",
  });
  // Minted here, not by the caller: one id per intent.
  expect(body.client_request_id).toMatch(UUID);
  for (const key of keys) expect(qc.getQueryState(key)?.isInvalidated).toBe(true);
  qc.clear();
});

it("keeps one request id across retries of the same intent, and mints a new one for a new answer", async () => {
  const qc = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  const respondDecision = vi
    .fn()
    .mockRejectedValueOnce(new Error("network dropped"))
    .mockRejectedValueOnce(new Error("network dropped"))
    .mockResolvedValue({ id: "receipt" });
  setApiInstance({ respondDecision } as unknown as ApiClient);
  const { result } = renderHook(() => useRespondDecision(), { wrapper: wrapperFor(qc) });

  await act(async () => {
    await expect(result.current.mutateAsync(INTENT)).rejects.toThrow("network dropped");
  });
  await act(async () => {
    await expect(result.current.mutateAsync(INTENT)).rejects.toThrow("network dropped");
  });
  await act(async () => { await result.current.mutateAsync(INTENT); });
  const ids = (respondDecision.mock.calls as unknown as [string, { client_request_id: string }][]).map(
    ([, body]) => body.client_request_id,
  );
  // A dropped transport may still have committed; the retry has to resolve to
  // that receipt instead of recording the decision a second time.
  expect(new Set(ids).size).toBe(1);

  // A settled intent retires its id: the next answer is a new submission.
  await act(async () => { await result.current.mutateAsync({ ...INTENT, decision: "custom", rationale: "do it differently" }); });
  const [, next] = respondDecision.mock.calls.at(-1) as unknown as [string, { client_request_id: string }];
  expect(next.client_request_id).not.toBe(ids[0]);
  qc.clear();
});

it("retires the request id on a conflict, because the issue moved on", async () => {
  const qc = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  const respondDecision = vi
    .fn()
    .mockRejectedValueOnce(new ApiError("issue is not waiting for a decision", 409, "Conflict"))
    .mockResolvedValue({ id: "receipt" });
  setApiInstance({ respondDecision } as unknown as ApiClient);
  const { result } = renderHook(() => useRespondDecision(), { wrapper: wrapperFor(qc) });

  await act(async () => {
    await expect(result.current.mutateAsync(INTENT)).rejects.toThrow();
  });
  await act(async () => { await result.current.mutateAsync(INTENT); });
  const ids = (respondDecision.mock.calls as unknown as [string, { client_request_id: string }][]).map(
    ([, body]) => body.client_request_id,
  );
  expect(ids[0]).not.toBe(ids[1]);
  qc.clear();
});

it("invalidates stale data after a failed response", async () => {
  const qc = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  const keys = [decisionKeys.all("ws-1"), issueKeys.detail("ws-1", "issue-1"), issueKeys.timeline("issue-1")];
  for (const key of keys) qc.setQueryData(key, {});
  setApiInstance({ respondDecision: vi.fn().mockRejectedValue(new Error("conflict")) } as unknown as ApiClient);
  const { result } = renderHook(() => useRespondDecision(), { wrapper: wrapperFor(qc) });
  await act(async () => {
    await expect(result.current.mutateAsync(INTENT)).rejects.toThrow("conflict");
  });
  for (const key of keys) expect(qc.getQueryState(key)?.isInvalidated).toBe(true);
  qc.clear();
});
