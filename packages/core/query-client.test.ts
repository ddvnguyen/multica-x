import { describe, expect, it } from "vitest";
import { ApiError } from "./api/client";
import { createQueryClient, shouldRetryQuery } from "./query-client";

function httpError(status: number): ApiError {
  return new ApiError(`request failed with status ${status}`, status, "", null);
}

describe("shouldRetryQuery", () => {
  it.each([400, 401, 403, 404, 422])(
    "never retries a %i: the server answered, retrying cannot change it (M-373)",
    (status) => {
      expect(shouldRetryQuery(0, httpError(status))).toBe(false);
      expect(shouldRetryQuery(1, httpError(status))).toBe(false);
    },
  );

  it("retries a 500 once, then gives up", () => {
    expect(shouldRetryQuery(0, httpError(500))).toBe(true);
    expect(shouldRetryQuery(1, httpError(500))).toBe(false);
  });

  it("retries a transport failure once, then gives up", () => {
    expect(shouldRetryQuery(0, new Error("Failed to fetch"))).toBe(true);
    expect(shouldRetryQuery(1, new Error("Failed to fetch"))).toBe(false);
  });
});

describe("createQueryClient defaults", () => {
  it("wires the 4xx guard as the query retry policy", () => {
    const client = createQueryClient();
    expect(client.getDefaultOptions().queries?.retry).toBe(shouldRetryQuery);
    client.unmount();
  });
});
