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
  mkdirSync(join(next, "server", "app"), { recursive: true });
  mkdirSync(join(next, "server", "chunks"), { recursive: true });
  mkdirSync(join(next, "standalone", "apps", "web", ".next", "server", "app"), { recursive: true });
  mkdirSync(join(web, "public"), { recursive: true });
  // The manifest route body is the brand ground truth. The source-tree
  // server chunk embeds BOTH brands' literals (as real builds do), proving
  // the gate reads the manifest output and not a raw string.
  writeFileSync(join(next, "server", "chunks", "both.js"), "mmx-install multica-web both literals\n");
  // The standalone tree is what `next build` emits; the script stages
  // public + static into it and gates on its manifest route output.
  writeFileSync(
    join(next, "standalone", "apps", "web", ".next", "server", "app", "manifest.webmanifest.body"),
    brand === "mmx" ? '{"id":"/?mmx-install","name":"MMX"}\n' : '{"id":"/","name":"Multica"}\n',
  );
  writeFileSync(join(next, "standalone", "apps", "web", "server.js"), "server\n");
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
    assert.match(r.stderr, /MMX brand manifest missing/);
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
