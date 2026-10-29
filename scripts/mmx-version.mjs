#!/usr/bin/env node
// One version string for one MMX build: x.x.x-yyMMMdd.HH.mm
//
//   0.7.0-26OCT28.21.58   (Owner example: 21:58 local on 28 Oct = 14:58Z,
//                          so the clock is UTC+7, Asia/Ho_Chi_Minh — fixed
//                          offset, no tz database needed)
//
// `x.x.x` is the multica version of the fork, read from apps/web/package.json
// — the file upstream release-please bumps — so nothing re-states it here.
// The suffix is the build time; `--from-git` takes HEAD's committer time
// instead of the wall clock, which is what CI and the lane deploy use so the
// same SHA always produces the same string.
//
// Outputs:
//   node scripts/mmx-version.mjs                     → 0.6.5-26OCT28.21.58
//   node scripts/mmx-version.mjs --from-git          → stamp of HEAD
//   node scripts/mmx-version.mjs --json              → {"version","stamp","utc","sha"}
//   node scripts/mmx-version.mjs --release-json --ref <ref> [--sha <sha>]
//                                                    → the dsh-z release.json line
//
// Flags: --from-git  stamp from `git show -s --format=%cI HEAD` (fails without git)
//        --json      machine-readable payload (the badge tooltip reads stamp/utc/sha)
//        --release-json  one-line release.json with "version" added
//        --ref <r>   release.json ref (default $MULTICA_X_REF)
//        --sha <s>   release.json sha (default: full HEAD sha)
//        --now <iso> override the wall clock (tests)
//        --help
//
// Plain Node, no dependencies — used by vite.config.ts, the CI assertion step
// and deploy/dsh-z/deploy.sh alike.

import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const VERSION_FILE = "apps/web/package.json";

const MONTHS = [
  "JAN",
  "FEB",
  "MAR",
  "APR",
  "MAY",
  "JUN",
  "JUL",
  "AUG",
  "SEP",
  "OCT",
  "NOV",
  "DEC",
];

/** UTC+7 (Asia/Ho_Chi_Minh) has no DST: a fixed offset is the whole truth. */
const UTC7_MS = 7 * 60 * 60 * 1000;

/** The fork's multica version — single source, apps/web/package.json. */
export function readVersion(root = repoRoot) {
  const raw = JSON.parse(readFileSync(resolve(root, VERSION_FILE), "utf8"));
  const version = typeof raw?.version === "string" ? raw.version : "";
  if (!/^\d+\.\d+\.\d+$/.test(version)) {
    throw new Error(
      `${VERSION_FILE}: expected version "x.x.x", got ${JSON.stringify(raw?.version)}`,
    );
  }
  return version;
}

function pad2(n) {
  return String(n).padStart(2, "0");
}

/** The suffix for an instant, read on the UTC+7 clock: 26OCT28.21.58. */
export function stampFor(date) {
  const local = new Date(date.getTime() + UTC7_MS);
  const yy = pad2(local.getUTCFullYear()).slice(-2);
  const month = MONTHS[local.getUTCMonth()];
  const dd = pad2(local.getUTCDate());
  const hh = pad2(local.getUTCHours());
  const mm = pad2(local.getUTCMinutes());
  return `${yy}${month}${dd}.${hh}.${mm}`;
}

/** Tooltip line for the same instant, in UTC: 2026-10-28T14:58Z. */
export function utcLabel(date) {
  return `${date.toISOString().slice(0, 16)}Z`;
}

function git(args, { allowMissing = false } = {}) {
  try {
    return execFileSync("git", args, {
      cwd: repoRoot,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    }).trim();
  } catch (cause) {
    if (allowMissing) return null;
    throw new Error(`git ${args.join(" ")} failed: ${cause.message}`);
  }
}

/** HEAD's committer instant — what makes one SHA produce one string. */
export function committerTime() {
  const iso = git(["show", "-s", "--format=%cI", "HEAD"]);
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    throw new Error(`HEAD committer time is not a date: ${iso}`);
  }
  return date;
}

/**
 * The full payload. `fromGit` pins the stamp to HEAD's committer time and
 * refuses to continue without git; the wall-clock path degrades to an
 * "unknown" sha so a plain local `vite build` still works outside a repo.
 */
export function collect({ fromGit = false, now = new Date(), sha = null } = {}) {
  const date = fromGit ? committerTime() : now;
  const version = readVersion();
  return {
    version,
    stamp: `${version}-${stampFor(date)}`,
    utc: utcLabel(date),
    sha:
      sha ??
      (fromGit
        ? git(["rev-parse", "--short=7", "HEAD"])
        : (git(["rev-parse", "--short=7", "HEAD"], { allowMissing: true }) ??
          "unknown")),
  };
}

/** The dsh-z multica-be release.json line, now carrying "version". */
export function releaseJson({ ref, sha, builtAt = new Date() }) {
  if (typeof ref !== "string" || ref === "") {
    throw new Error("release.json needs a ref (--ref or $MULTICA_X_REF)");
  }
  if (typeof sha !== "string" || sha === "") {
    throw new Error("release.json needs a sha (--sha or HEAD)");
  }
  const { stamp } = collect({ fromGit: true });
  return `${JSON.stringify({
    sha,
    ref,
    version: stamp,
    builtAt: builtAt.toISOString().replace(/\.\d{3}Z$/, "Z"),
  })}\n`;
}

function parseArgs(argv) {
  const opts = {
    fromGit: false,
    json: false,
    releaseJson: false,
    ref: process.env.MULTICA_X_REF ?? null,
    sha: null,
    now: null,
  };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    switch (arg) {
      case "--from-git":
        opts.fromGit = true;
        break;
      case "--json":
        opts.json = true;
        break;
      case "--release-json":
        opts.releaseJson = true;
        break;
      case "--ref":
        opts.ref = argv[(i += 1)];
        break;
      case "--sha":
        opts.sha = argv[(i += 1)];
        break;
      case "--now": {
        const now = new Date(argv[(i += 1)]);
        if (Number.isNaN(now.getTime())) throw new Error("--now needs an ISO date");
        opts.now = now;
        break;
      }
      case "--help":
      case "-h":
        opts.help = true;
        break;
      default:
        throw new Error(`unknown flag: ${arg}`);
    }
  }
  return opts;
}

function main(argv) {
  let opts;
  try {
    opts = parseArgs(argv);
  } catch (cause) {
    process.stderr.write(`${cause.message}\n`);
    return 2;
  }
  if (opts.help) {
    process.stdout.write(
      "usage: mmx-version.mjs [--from-git] [--json] [--release-json] [--ref R] [--sha S] [--now ISO]\n",
    );
    return 0;
  }
  try {
    if (opts.releaseJson) {
      // release.json without a git provenance is meaningless, so the sha
      // defaults to full HEAD and a missing repo is a hard error.
      process.stdout.write(
        releaseJson({
          ref: opts.ref,
          sha: opts.sha ?? git(["rev-parse", "HEAD"]),
          builtAt: opts.now ?? new Date(),
        }),
      );
      return 0;
    }
    const payload = collect({
      fromGit: opts.fromGit,
      now: opts.now ?? new Date(),
      sha: opts.sha,
    });
    process.stdout.write(
      `${opts.json ? `${JSON.stringify(payload)}\n` : `${payload.stamp}\n`}`,
    );
    return 0;
  } catch (cause) {
    process.stderr.write(`${cause.message}\n`);
    return 1;
  }
}

const invoked = process.argv[1] ? resolve(process.argv[1]) : "";
if (invoked === fileURLToPath(import.meta.url)) {
  process.exitCode = main(process.argv.slice(2));
}
