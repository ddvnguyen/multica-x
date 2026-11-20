#!/usr/bin/env node
// Regression test for scripts/build-mmx-pwa-artifact.sh (M-451 item 3, layout
// contract M-457): the push-only artifact path is exercised with fixture
// trees, since CI skips those steps on pull requests.
//
//   * MMX fixture tree  -> stages, stamps sw.js with the SHA, brand gate
//     passes, and the upload root is EXACTLY `standalone/` + `release.json`
//     with the tree at `standalone/apps/web/...` — the relative layout
//     scripts/lane-z/autodeploy.sh verifies. The test and the consumer share
//     one contract, so a nesting change (the M-457 bug: upload-artifact v4
//     rooted a multi-path upload at `apps/web/.next/standalone/`) fails here.
//   * default-brand tree (simulated web-dist contamination: the tree the
//     web-dist upload captured, rebuilt without MMX_PWA) -> brand gate
//     rejects, so it can never ship under the mmx-pwa artifact name
//   * unversioned sw.js fixture -> placeholder gate rejects
//   * release.json with another sha -> gate rejects (the lane compares it to
//     the run sha)
//   * missing release.json -> gate rejects
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { dirname } from "node:path";
import test from "node:test";

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), "..");
const script = join(repoRoot, "scripts", "build-mmx-pwa-artifact.sh");
const SHA = "a".repeat(40);
const OTHER_SHA = "b".repeat(40);

function fixtureTree({ brand, stamp, releaseSha = SHA }) {
  const dir = mkdtempSync(join(tmpdir(), "mmx-pwa-artifact-"));
  const web = join(dir, "web");
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
  const release = join(dir, "release.json");
  if (releaseSha) {
    writeFileSync(
      release,
      `${JSON.stringify({ sha: releaseSha, ref: "ci", version: "0.0.0-test", builtAt: "2026-01-01T00:00:00Z" })}\n`,
    );
  }
  return {
    dir,
    web,
    stageDir: join(dir, "mmx-pwa-stage"),
    release: releaseSha ? release : join(dir, "no-release.json"),
    cleanup: () => rmSync(dir, { recursive: true, force: true }),
  };
}

function stage({ web, stageDir, release }) {
  return spawnSync("bash", [script], {
    env: {
      ...process.env,
      MMX_PWA_SHA: SHA,
      MMX_PWA_WEB_DIR: web,
      MMX_PWA_STAGE_DIR: stageDir,
      MMX_PWA_RELEASE_JSON: release,
      MMX_PWA_SKIP_BUILD: "1",
    },
    encoding: "utf8",
  });
}

test("MMX fixture tree stages the autodeploy layout: standalone/ + release.json at the root", () => {
  const tree = fixtureTree({ brand: "mmx", stamp: true });
  try {
    const r = stage(tree);
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /mmx-pwa artifact staged/);
    // The upload root is the artifact root: nothing else, no nesting.
    assert.deepEqual(readdirSync(tree.stageDir).sort(), ["release.json", "standalone"]);
    // autodeploy.sh verify-artifact + install-mmx-pwa read exactly these.
    const staged = join(tree.stageDir, "standalone", "apps", "web");
    assert.equal(readFileSync(join(staged, "server.js"), "utf8"), "server\n");
    assert.match(
      readFileSync(join(staged, ".next", "server", "app", "manifest.webmanifest.body"), "utf8"),
      /"id":"\/\?mmx-install"/,
      "staged tree keeps the MMX manifest",
    );
    const sw = readFileSync(join(staged, "public", "sw.js"), "utf8");
    assert.ok(sw.includes(SHA), "sw.js carries the full SHA stamp");
    assert.ok(!sw.includes("__MMX_PWA_VERSION__"), "placeholder is replaced");
    assert.equal(JSON.parse(readFileSync(join(tree.stageDir, "release.json"), "utf8")).sha, SHA);
  } finally {
    tree.cleanup();
  }
});

test("default-brand tree is rejected: cannot ship as the mmx-pwa artifact", () => {
  // Simulates the contamination the CI order fix prevents: the web-dist
  // tree (default brand) fed to the MMX staging step.
  const tree = fixtureTree({ brand: "default", stamp: true });
  try {
    const r = stage(tree);
    assert.notEqual(r.status, 0, "contaminated tree must fail the brand gate");
    assert.match(r.stderr, /MMX brand manifest missing/);
  } finally {
    tree.cleanup();
  }
});

test("unversioned sw.js is rejected", () => {
  const tree = fixtureTree({ brand: "mmx", stamp: false });
  try {
    const r = stage(tree);
    assert.notEqual(r.status, 0, "unversioned sw.js must fail staging");
    assert.match(r.stderr, /no version placeholder/);
  } finally {
    tree.cleanup();
  }
});

test("release.json with another sha is rejected", () => {
  // autodeploy.sh compares the artifact's release.json sha to the run sha;
  // a mismatch must fail here, not on the lane.
  const tree = fixtureTree({ brand: "mmx", stamp: true, releaseSha: OTHER_SHA });
  try {
    const r = stage(tree);
    assert.notEqual(r.status, 0, "a mismatched release.json must fail staging");
    assert.match(r.stderr, /release\.json sha/);
  } finally {
    tree.cleanup();
  }
});

test("missing release.json is rejected", () => {
  const tree = fixtureTree({ brand: "mmx", stamp: true, releaseSha: "" });
  try {
    const r = stage(tree);
    assert.notEqual(r.status, 0, "a missing release.json must fail staging");
    assert.match(r.stderr, /no release\.json/);
  } finally {
    tree.cleanup();
  }
});
