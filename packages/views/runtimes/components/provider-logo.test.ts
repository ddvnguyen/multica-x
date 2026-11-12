import { describe, expect, it } from "vitest";
import { runtimeLogoProvider } from "./provider-logo";

describe("runtimeLogoProvider", () => {
  it("uses the Freebuff mark for a freebuff profile runtime wrapping codebuddy", () => {
    expect(
      runtimeLogoProvider({ provider: "codebuddy", name: "Freebuff Desktop (acct)", profile_id: "p1" }),
    ).toBe("freebuff");
  });

  it("keeps the wrapped provider for other runtimes", () => {
    expect(runtimeLogoProvider({ provider: "codebuddy", name: "CodeBuddy", profile_id: "p1" })).toBe("codebuddy");
    expect(runtimeLogoProvider({ provider: "codebuddy", name: "freebuff local" })).toBe("codebuddy");
  });
});
