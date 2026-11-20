import { describe, expect, it } from "vitest";
import { isMmxPwa, pwaBrand } from "./pwa-brand";

describe("pwa-brand", () => {
  it("defaults to Multica branding", () => {
    const brand = pwaBrand({});
    expect(brand.name).toBe("Multica");
    expect(brand.short_name).toBe("Multica");
    expect(brand.id).toBe("/");
    expect(brand.iconDir).toBe("icons");
  });

  it("selects MMX branding with MMX_PWA=1", () => {
    const brand = pwaBrand({ MMX_PWA: "1" });
    expect(brand.name).toBe("MMX");
    expect(brand.short_name).toBe("MMX");
    expect(brand.iconDir).toBe("icons-mmx");
  });

  it("gives the MMX manifest a distinct id so it installs beside multica-web", () => {
    expect(pwaBrand({ MMX_PWA: "1" }).id).not.toBe(pwaBrand({}).id);
  });

  it("isMmxPwa is opt-in only", () => {
    expect(isMmxPwa({})).toBe(false);
    expect(isMmxPwa({ MMX_PWA: "0" })).toBe(false);
    expect(isMmxPwa({ MMX_PWA: "1" })).toBe(true);
  });
});
