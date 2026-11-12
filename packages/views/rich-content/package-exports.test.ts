// @vitest-environment node
/**
 * Package export-target guard.
 *
 * A dangling export can still resolve from the source tree during typecheck,
 * then fail only when a consuming application is bundled. This regressed in
 * MUL-4922, so validate the filesystem boundary rather than mirroring an
 * individual package.json entry.
 *
 * Packages that ship compiled artifacts export paths under a gitignored output
 * directory, which cannot exist in a fresh checkout — `lib/` is absent until
 * `build` runs, and the views suite runs before it. Those targets are excused
 * only when the same package declares a `build` script, so a gitignored export
 * nothing produces is still reported.
 */
import { describe, expect, it } from "vitest";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, relative } from "node:path";

const REPO_ROOT = join(__dirname, "..", "..", "..");

function workspacePackageJsons(): string[] {
  const out: string[] = [];
  for (const group of ["packages", "apps"]) {
    const groupDir = join(REPO_ROOT, group);
    if (!existsSync(groupDir)) continue;
    for (const entry of readdirSync(groupDir)) {
      const pkg = join(groupDir, entry, "package.json");
      if (existsSync(pkg) && statSync(pkg).isFile()) out.push(pkg);
    }
  }
  return out;
}

function exportTargets(value: unknown): string[] {
  if (typeof value === "string") return [value];
  if (value && typeof value === "object") {
    return Object.values(value as Record<string, unknown>).flatMap(exportTargets);
  }
  return [];
}

function exportTargetExists(pkgDir: string, target: string): boolean {
  const path = target.includes("*") ? target.slice(0, target.indexOf("*")) : target;
  return existsSync(join(pkgDir, path));
}

/**
 * Top-level paths a package's own `.gitignore` excludes.
 *
 * A target under one of them is build output: it cannot exist in a fresh
 * checkout, so its absence says nothing about whether the export is correct.
 * Only packages that ship compiled artifacts have this shape; everything else
 * exports the sources their consumers compile.
 */
function ignoredEntries(pkgDir: string): string[] {
  const file = join(pkgDir, ".gitignore");
  if (!existsSync(file)) return [];
  return readFileSync(file, "utf8")
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(
      (line) => line !== "" && !line.startsWith("#") && !line.startsWith("!"),
    )
    .map(
      (line) =>
        line.replace(/^\.\//, "").replace(/^\//, "").split("/")[0] ?? "",
    );
}

function isBuildOutput(pkgDir: string, target: string): boolean {
  const first = target.replace(/^\.\//, "").split("/")[0] ?? "";
  return first !== "" && ignoredEntries(pkgDir).includes(first);
}

interface Dangling {
  pkg: string;
  subpath: string;
  target: string;
}

function findDanglingExports(
  pkgPaths: string[] = workspacePackageJsons(),
): Dangling[] {
  const dangling: Dangling[] = [];

  for (const pkgPath of pkgPaths) {
    const pkgDir = dirname(pkgPath);
    let parsed: { exports?: unknown; scripts?: Record<string, string> };
    try {
      parsed = JSON.parse(readFileSync(pkgPath, "utf8"));
    } catch {
      continue;
    }
    if (!parsed.exports || typeof parsed.exports !== "object") continue;
    // Build output is excused only where a build step produces it. A package
    // that gitignores `lib/` but never builds it still has a dangling export.
    const builds = typeof parsed.scripts?.build === "string";

    for (const [subpath, value] of Object.entries(
      parsed.exports as Record<string, unknown>,
    )) {
      for (const target of exportTargets(value)) {
        if (
          !exportTargetExists(pkgDir, target) &&
          !(builds && isBuildOutput(pkgDir, target))
        ) {
          dangling.push({
            pkg: relative(REPO_ROOT, pkgPath),
            subpath,
            target,
          });
        }
      }
    }
  }

  return dangling;
}

describe("workspace package exports", () => {
  it("every export target points at a file that exists", () => {
    const dangling = findDanglingExports();
    expect(dangling.map((d) => `${d.pkg} :: "${d.subpath}" -> ${d.target}`)).toEqual([]);
  });

  it("excuses a build-output target only where the package builds it", () => {
    const scratch = mkdtempSync(join(tmpdir(), "package-exports-"));
    try {
      const built = join(scratch, "built");
      const unbuilt = join(scratch, "unbuilt");
      for (const dir of [built, unbuilt]) {
        mkdirSync(dir);
        writeFileSync(join(dir, ".gitignore"), "lib/\nnode_modules/\n");
      }
      writeFileSync(
        join(built, "package.json"),
        JSON.stringify({
          name: "fixture-built",
          scripts: { build: "tsc -p tsconfig.build.json" },
          exports: {
            ".": "./lib/index.js",
            "./missing": "./src/missing.ts",
          },
        }),
      );
      writeFileSync(
        join(unbuilt, "package.json"),
        JSON.stringify({
          name: "fixture-unbuilt",
          exports: { ".": "./lib/index.js" },
        }),
      );

      const reported = findDanglingExports([
        join(built, "package.json"),
        join(unbuilt, "package.json"),
      ]).map((d) => `${d.subpath} -> ${d.target}`);

      expect(reported).toEqual([
        "./missing -> ./src/missing.ts",
        ". -> ./lib/index.js",
      ]);
    } finally {
      rmSync(scratch, { recursive: true, force: true });
    }
  });

  it("does not pass vacuously when traversal or existence checks break", () => {
    const viewsDir = join(REPO_ROOT, "packages/views");
    expect(workspacePackageJsons().length).toBeGreaterThan(3);
    expect(exportTargetExists(viewsDir, "./editor/index.ts")).toBe(true);
    expect(exportTargetExists(viewsDir, "./definitely-missing.ts")).toBe(false);
  });
});
