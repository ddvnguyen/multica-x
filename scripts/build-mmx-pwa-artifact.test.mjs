#!/usr/bin/env node
// Regression test for scripts/build-mmx-pwa-artifact.sh (M-451 item 3):
// the push-only artifact path is exercised with fixture trees, since CI
// skips those steps on pull requests.
//
//   * MMX fixture tree  -> stages, stamps sw.js with the SHA, brand gate passes
//   * default-brand tree (simulated web-dist contamination: the tree the
//     web-dist upload captured, rebuilt without MMX_PWA) -> brand gate
//     rejects, so it can never ship under the mmx-pwa artifact name
//   * unversioned sw.js fixture -> placeholder gate rejects
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { dirname } from "node:path";
import test from "node:test";

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), "..");
const script = join(repoRoot, "scripts", "build-mmx-pwa-artifact.sh");
const SHA = "a".repeat(40);

function fixtureTree({ brand, stamp }) {
  const web = mkdtempSync(join(tmpdir(), "mmx-pwa-artifact-"));
  const next = join(web, ".next");
  mkdirSync(join(next, "static"), { recursive: true });
  mkdirSync(join(web, "public"), { recursive: true });
  // The brand marker lives where a real MMX_PWA=1 build bakes it: inside
  // the staged tree (public/.next/static are what the script stages).
  writeFileSync(join(next, "static", "brand.txt"), brand === "mmx" ? "mmx-install" : "multica-web");
  writeFileSync(join(web, "public", "sw.js"), `const VERSION = "${stamp ? "__MMX_PWA_VERSION__" : "dev"}";\n`);
  writeFileSync(join(next, "static", "asset.js"), "asset\n");
  return { web, cleanup: () => rmSync(web, { recursive: true, force: true }) };
}

function stage(web) {
  return spawnSync("bash", [script], {
    env: { ...process.env, MMX_PWA_SHA: SHA, MMX_PWA_WEB_DIR: web, MMX_PWA_SKIP_BUILD: "1" },
    encoding: "utf8",
  });
}

test("MMX fixture tree stages with SHA stamp and brand gate passing", () => {
  const { web, cleanup } = fixtureTree({ brand: "mmx", stamp: true });
  try {
    const r = stage(web);
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /mmx-pwa artifact staged/);
    const sw = readFileSync(join(web, ".next", "standalone", "apps", "web", "public", "sw.js"), "utf8");
    assert.ok(sw.includes(SHA), "sw.js carries the full SHA stamp");
    assert.ok(!sw.includes("__MMX_PWA_VERSION__"), "placeholder is replaced");
  } finally {
    cleanup();
  }
});

test("default-brand tree is rejected: cannot ship as the mmx-pwa artifact", () => {
  // Simulates the contamination the CI order fix prevents: the web-dist
  // tree (default brand) fed to the MMX staging step.
  const { web, cleanup } = fixtureTree({ brand: "default", stamp: true });
  try {
    const r = stage(web);
    assert.notEqual(r.status, 0, "contaminated tree must fail the brand gate");
    assert.match(r.stderr, /MMX brand marker missing/);
  } finally {
    cleanup();
  }
});

test("unversioned sw.js is rejected", () => {
  const { web, cleanup } = fixtureTree({ brand: "mmx", stamp: false });
  try {
    const r = stage(web);
    assert.notEqual(r.status, 0, "unversioned sw.js must fail staging");
    assert.match(r.stderr, /no version placeholder/);
  } finally {
    cleanup();
  }
});
