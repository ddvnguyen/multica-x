import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { collect, readVersion, stampFor, utcLabel } from "./mmx-version.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const scriptPath = join(here, "mmx-version.mjs");
const repoRoot = resolve(here, "..");

function run(...args) {
  return execFileSync(process.execPath, [scriptPath, ...args], {
    cwd: repoRoot,
    encoding: "utf8",
  });
}

test("the Owner's example: 14:58Z on 28 Oct reads 26OCT28.21.58 on the UTC+7 clock", () => {
  assert.equal(stampFor(new Date("2026-10-28T14:58:00Z")), "26OCT28.21.58");
  assert.equal(utcLabel(new Date("2026-10-28T14:58:00Z")), "2026-10-28T14:58Z");
});

test("a full stamp is x.x.x-yyMMMdd.HH.mm with a capital English month", () => {
  const { stamp, version } = collect({ now: new Date("2026-10-28T14:58:00Z") });
  assert.equal(stamp, `${version}-26OCT28.21.58`);
  assert.match(stamp, /^\d+\.\d+\.\d+-\d{2}[A-Z]{3}\d{2}\.\d{2}\.\d{2}$/);
});

test("all twelve month abbreviations", () => {
  const months = [
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
  for (let month = 0; month < 12; month += 1) {
    // 04:00Z is 11:00 on the same day under UTC+7, so only the month shows.
    const date = new Date(Date.UTC(2026, month, 10, 4, 0, 0));
    assert.equal(stampFor(date), `26${months[month]}10.11.00`);
  }
});

test("UTC+7 rolls the day over at midnight, then the month, then the year", () => {
  assert.equal(stampFor(new Date("2026-10-28T16:59:59Z")), "26OCT28.23.59");
  assert.equal(stampFor(new Date("2026-10-28T17:00:00Z")), "26OCT29.00.00");
  assert.equal(stampFor(new Date("2026-12-31T16:59:59Z")), "26DEC31.23.59");
  assert.equal(stampFor(new Date("2026-12-31T17:00:00Z")), "27JAN01.00.00");
});

test("x.x.x comes from apps/web/package.json, nowhere else", () => {
  const file = JSON.parse(
    readFileSync(join(repoRoot, "apps/web/package.json"), "utf8"),
  );
  assert.equal(readVersion(), file.version);
  // And the CLI agrees with the file, not with a copy of it.
  const fromFile = run("--now", "2026-10-28T14:58:00Z").trim();
  assert.ok(fromFile.startsWith(`${file.version}-`));
});

test("--json carries the tooltip payload: stamp, UTC time and short sha", () => {
  const payload = JSON.parse(
    run("--json", "--now", "2026-10-28T14:58:00Z", "--sha", "abc1234").trim(),
  );
  assert.match(payload.stamp, /\d{2}OCT28\.21\.58$/);
  assert.equal(payload.utc, "2026-10-28T14:58Z");
  assert.equal(payload.sha, "abc1234");
});

test("one SHA, one string: --from-git is stable across runs", () => {
  const first = run("--from-git").trim();
  const second = run("--from-git").trim();
  assert.equal(first, second);
  assert.match(first, /^\d+\.\d+\.\d+-\d{2}[A-Z]{3}\d{2}\.\d{2}\.\d{2}$/);
});

test("--release-json carries the same stamp next to sha and ref", () => {
  const line = run(
    "--release-json",
    "--ref",
    "ci",
    "--sha",
    "deadbeef",
    "--now",
    "2026-10-28T21:58:00Z",
  );
  const body = JSON.parse(line);
  assert.equal(body.sha, "deadbeef");
  assert.equal(body.ref, "ci");
  assert.equal(body.version, run("--from-git").trim());
  assert.match(body.builtAt, /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/);
});

test("--release-json without a ref fails loudly", () => {
  assert.throws(
    () =>
      execFileSync(
        process.execPath,
        [scriptPath, "--release-json", "--sha", "deadbeef"],
        {
          cwd: repoRoot,
          encoding: "utf8",
          env: { ...process.env, MULTICA_X_REF: "" },
          stdio: ["ignore", "pipe", "pipe"],
        },
      ),
    /needs a ref/,
  );
});
