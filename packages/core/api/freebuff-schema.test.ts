// @vitest-environment node
import { describe, it, expect } from "vitest";
import { FreebuffAccountsSchema } from "./schemas";
import { parseWithFallback } from "./schema";

const parse = (raw: unknown) =>
  parseWithFallback(raw, FreebuffAccountsSchema, { accounts: [] }, {
    endpoint: "GET /api/runtimes/:id/freebuff",
  });

describe("freebuff accounts response compatibility", () => {
  it("reads a current response", () => {
    const out = parse({
      accounts: [
        {
          id: "a1",
          label: "me@example.com",
          state: "awaiting_login",
          login_url: "https://freebuff.com/login?auth_code=x",
          status: { message: "hi", credit: { dailyRemaining: 3 } },
          future_field: true,
        },
      ],
      default_account_id: "a1",
    });
    expect(out.accounts[0]?.login_url).toBe("https://freebuff.com/login?auth_code=x");
    expect(out.accounts[0]?.status?.credit?.dailyRemaining).toBe(3);
    expect(out.default_account_id).toBe("a1");
  });

  it("falls back to an empty list for a missing or malformed body", () => {
    for (const raw of [undefined, null, 42, "x", {}, { accounts: "nope" }, { accounts: null }]) {
      expect(parse(raw).accounts).toEqual([]);
    }
  });

  it("keeps an account whose fields are malformed", () => {
    const out = parse({ accounts: [{ id: 7, state: null, status: "x", expires_at: 5 }] });
    expect(out.accounts).toHaveLength(1);
    expect(out.accounts[0]?.state).toBe("");
    expect(out.accounts[0]?.status ?? null).toBeNull();
  });

  it("ignores the pre-multi-account shape", () => {
    expect(parse({ connect: { state: "connected" }, status: { message: "x" } }).accounts).toEqual([]);
  });
});
