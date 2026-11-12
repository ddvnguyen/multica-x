import { readdir, readFile } from "node:fs/promises";
import { dirname, join, relative, resolve, sep } from "node:path";

/**
 * Q9 — one entry module, and only public fork surface through it.
 *
 * The plugin renders a fork of Multica. If it could import the fork's files
 * directly, the "which fork is this?" question would have no answer: a fork
 * update could silently change what is rendered, and a plugin file could reach
 * into internals no one promised to keep. So:
 *
 * 1. exactly one source file (`src/fork.ts`) may name an `@multica/*` module;
 * 2. every specifier it names must be a published export of that package;
 * 3. exactly one stylesheet (`src/client/island.css`) may import the fork's
 *    styles, and only through `@multica/ui/styles/*`;
 * 4. no source file may walk out of the package with a relative path.
 */

export const FORK_ENTRY = "src/fork.ts";
export const STYLE_ENTRY = "src/client/island.css";
export const FORK_SCOPE = "@multica/";

const SOURCE_EXTENSIONS = new Set([".ts", ".tsx", ".mts", ".js", ".jsx", ".mjs"]);
const SPECIFIER_PATTERNS = [
  /(?:^|[\s;])(?:import|export)\s[^'"]*?from\s*['"]([^'"]+)['"]/g,
  /(?:^|[\s;])import\s*['"]([^'"]+)['"]/g,
  /\brequire\s*\(\s*['"]([^'"]+)['"]\s*\)/g,
  /\bimport\s*\(\s*['"]([^'"]+)['"]\s*\)/g,
];

/**
 * @param {string} packageRoot absolute path of packages/dsh-plugin
 * @param {{ readFileImpl?: typeof readFile, readdirImpl?: typeof readdir }} [io]
 * @returns {Promise<{ violations: string[], checked: string[] }>}
 */
export async function scanForkEntry(packageRoot, io = {}) {
  const read = io.readFileImpl ?? readFile;
  const list = io.readdirImpl ?? readdir;
  const workspaceRoot = resolve(packageRoot, "..", "..");
  const srcRoot = join(packageRoot, "src");
  const violations = [];
  const checked = [];

  const files = await walk(srcRoot, list);
  const exportsCache = new Map();

  for (const file of files) {
    const relativePath = toPosix(relative(packageRoot, file));
    const extension = file.slice(file.lastIndexOf("."));
    if (!SOURCE_EXTENSIONS.has(extension) && extension !== ".css") continue;
    checked.push(relativePath);
    const source = await read(file, "utf8");

    if (extension === ".css") {
      const imports = [...source.matchAll(/@import\s+["']([^"']+)["']/g)].map(
        (match) => match[1],
      );
      for (const specifier of imports) {
        if (!specifier.startsWith(FORK_SCOPE)) continue;
        if (relativePath !== STYLE_ENTRY) {
          violations.push(
            `${relativePath}: imports ${specifier}; the fork's styles may only be imported by ${STYLE_ENTRY}`,
          );
          continue;
        }
        if (!specifier.startsWith("@multica/ui/styles/")) {
          violations.push(
            `${relativePath}: imports ${specifier}; only @multica/ui/styles/* is importable from a stylesheet`,
          );
          continue;
        }
        const ok = await isPublicExport(
          workspaceRoot,
          specifier,
          exportsCache,
          read,
        );
        if (!ok) {
          violations.push(
            `${relativePath}: ${specifier} is not a public export of its package`,
          );
        }
      }
      continue;
    }

    for (const specifier of specifiersOf(source)) {
      if (specifier.startsWith(FORK_SCOPE)) {
        if (relativePath !== FORK_ENTRY) {
          violations.push(
            `${relativePath}: imports ${specifier}; the fork may only be imported by ${FORK_ENTRY}`,
          );
          continue;
        }
        const ok = await isPublicExport(
          workspaceRoot,
          specifier,
          exportsCache,
          read,
        );
        if (!ok) {
          violations.push(
            `${relativePath}: ${specifier} is not a public export of its package`,
          );
        }
        continue;
      }
      if (specifier.startsWith(".")) {
        const target = resolve(dirname(file), specifier);
        if (!target.startsWith(packageRoot + sep)) {
          violations.push(
            `${relativePath}: ${specifier} resolves outside packages/dsh-plugin`,
          );
        }
      }
    }
  }

  return { violations, checked };
}

function specifiersOf(source) {
  const found = new Set();
  for (const pattern of SPECIFIER_PATTERNS) {
    for (const match of source.matchAll(pattern)) {
      if (match[1] !== undefined) found.add(match[1]);
    }
  }
  return found;
}

async function isPublicExport(workspaceRoot, specifier, cache, read) {
  const [scope, name, ...rest] = specifier.split("/");
  const packageName = `${scope}/${name}`;
  const subpath = rest.length === 0 ? "." : `./${rest.join("/")}`;
  let exportsMap = cache.get(packageName);
  if (exportsMap === undefined) {
    const manifestPath = join(workspaceRoot, "packages", name, "package.json");
    try {
      const manifest = JSON.parse(await read(manifestPath, "utf8"));
      exportsMap = manifest.exports ?? {};
    } catch {
      exportsMap = null;
    }
    cache.set(packageName, exportsMap);
  }
  if (exportsMap === null) return false;
  if (Object.prototype.hasOwnProperty.call(exportsMap, subpath)) return true;
  return Object.keys(exportsMap).some((key) => {
    const index = key.indexOf("*");
    if (index === -1) return false;
    return (
      subpath.startsWith(key.slice(0, index)) &&
      subpath.endsWith(key.slice(index + 1))
    );
  });
}

async function walk(directory, list) {
  const entries = await list(directory, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      files.push(...(await walk(path, list)));
    } else if (entry.isFile()) {
      files.push(path);
    }
  }
  return files.sort();
}

function toPosix(path) {
  return path.split(sep).join("/");
}
