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

describe("Freebuff model catalog in the account status", () => {
  it("keeps the models the daemon reports", () => {
    const parsed = FreebuffAccountsSchema.parse({
      accounts: [
        {
          id: "a1",
          state: "connected",
          status: {
            models: [
              { id: "m-1", name: "GLM 5.3 Flash", price: 20, locked: false, recommended: true, notes: ["Promotional"] },
              { id: "m-2", name: "Claude Haiku 5.5", price: null, locked: true },
            ],
          },
        },
      ],
    });
    const models = parsed.accounts[0]?.status?.models ?? [];
    expect(models).toHaveLength(2);
    expect(models[0]).toMatchObject({ id: "m-1", price: 20, recommended: true, notes: ["Promotional"] });
    expect(models[1]).toMatchObject({ locked: true, price: null });
  });

  it("survives a malformed catalog without losing the account", () => {
    const parsed = FreebuffAccountsSchema.parse({
      accounts: [{ id: "a1", state: "connected", label: "x@y.z", status: { message: "ok", models: "nope" } }],
    });
    expect(parsed.accounts[0]?.label).toBe("x@y.z");
    expect(parsed.accounts[0]?.status?.models ?? null).toBeNull();
  });
});
