import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  PWA_ASSET_PREFIXES,
  PWA_NETWORK_ONLY_PREFIXES,
  PWA_PRECACHE_PATHS,
  isPwaAsset,
  isPwaNetworkOnly,
} from "./pwa-cache";

const here = dirname(fileURLToPath(import.meta.url));

/** Extract a `const NAME = [...]` string array from the raw sw.js source. */
function swList(name: string): string[] {
  const src = readFileSync(resolve(here, "../public/sw.js"), "utf8");
  const match = src.match(new RegExp(`const ${name} = \\[([^\\]]*)\\]`));
  if (!match?.[1]) throw new Error(`sw.js has no ${name} list`);
  return [...match[1].matchAll(/"([^"]+)"/g)].map((m) => m[1] ?? "");
}

describe("pwa-cache rules", () => {
  it("never caches /api or /ws", () => {
    for (const path of ["/api", "/api/issues", "/api/avatars/x", "/ws"]) {
      expect(isPwaNetworkOnly(path)).toBe(true);
    }
  });

  it("never caches auth traffic", () => {
    expect(isPwaNetworkOnly("/auth/google")).toBe(true);
  });

  it("does not mistake app routes for network-only", () => {
    expect(isPwaNetworkOnly("/inbox")).toBe(false);
    expect(isPwaNetworkOnly("/apiary")).toBe(false);
    expect(isPwaNetworkOnly("/wsx")).toBe(false);
  });

  it("caches versioned assets only", () => {
    expect(isPwaAsset("/_next/static/chunks/app.js")).toBe(true);
    expect(isPwaAsset("/icons/icon-192.png")).toBe(true);
    expect(isPwaAsset("/icons-mmx/icon-192.png")).toBe(true);
    expect(isPwaAsset("/inbox")).toBe(false);
    expect(isPwaAsset("/api/issues")).toBe(false);
  });

  it("precaches the offline page", () => {
    expect(PWA_PRECACHE_PATHS).toContain("/offline");
  });

  it("sw.js embeds the same lists (single contract, no drift)", () => {
    expect(swList("NETWORK_ONLY")).toEqual([...PWA_NETWORK_ONLY_PREFIXES]);
    expect(swList("ASSET_PREFIXES")).toEqual([...PWA_ASSET_PREFIXES]);
    expect(swList("PRECACHE_PATHS")).toEqual([...PWA_PRECACHE_PATHS]);
  });

  it("sw.js update flow uses SKIP_WAITING + controllerchange contract", () => {
    const src = readFileSync(resolve(here, "../public/sw.js"), "utf8");
    expect(src).toContain("SKIP_WAITING");
    expect(src).toContain("self.skipWaiting()");
    expect(src).toContain("self.clients.claim()");
  });
});
