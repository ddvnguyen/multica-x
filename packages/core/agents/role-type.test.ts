import { describe, expect, it } from "vitest";
import { isRoleAgent } from "./role-type";

describe("isRoleAgent", () => {
  it("matches only the explicit role subtype", () => {
    expect(isRoleAgent({ subtype: "role" })).toBe(true);
    expect(isRoleAgent({ subtype: "agent" })).toBe(false);
    expect(isRoleAgent({})).toBe(false);
    expect(isRoleAgent({ subtype: "" })).toBe(false);
  });

  it("fails closed for null and undefined", () => {
    expect(isRoleAgent(null)).toBe(false);
    expect(isRoleAgent(undefined)).toBe(false);
  });
});
