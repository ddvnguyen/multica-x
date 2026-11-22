import { describe, expect, it } from "vitest";
import { agentRoleTypeProbeOptions, isRoleAgent } from "./role-type";

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

describe("agentRoleTypeProbeOptions", () => {
  it("is disabled without a workspace", () => {
    expect(agentRoleTypeProbeOptions("").enabled).toBe(false);
    expect(agentRoleTypeProbeOptions("ws-1").enabled).not.toBe(false);
  });

  it("fails closed when the endpoint errors", async () => {
    const options = agentRoleTypeProbeOptions("ws-1");
    const queryFn = options.queryFn as () => Promise<boolean>;
    // No QueryClient/api in unit scope: the fetch rejects, the probe
    // resolves false.
    await expect(queryFn()).resolves.toBe(false);
  });
});
