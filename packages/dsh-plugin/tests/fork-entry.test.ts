// @vitest-environment node
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { scanForkEntry } from "../scripts/fork-entry-rules.mjs";

const packageRoot = resolve(import.meta.dirname, "..");
const scratch: string[] = [];

afterEach(async () => {
  await Promise.all(scratch.splice(0).map((path) => rm(path, { recursive: true, force: true })));
});

/** A miniature workspace: one package root plus the fork packages it names. */
async function fixture(files: Record<string, string>): Promise<string> {
  const root = await mkdtemp(join(tmpdir(), "dsh-plugin-gate-"));
  scratch.push(root);
  await mkdir(join(root, "packages", "core"), { recursive: true });
  await writeFile(
    join(root, "packages", "core", "package.json"),
    JSON.stringify({ name: "@multica/core", exports: { ".": "./index.ts", "./platform": "./platform/index.ts" } }),
  );
  for (const [path, contents] of Object.entries(files)) {
    const absolute = join(root, "packages", "dsh-plugin", path);
    await mkdir(join(absolute, ".."), { recursive: true });
    await writeFile(absolute, contents);
  }
  return join(root, "packages", "dsh-plugin");
}

describe("fork provenance gate", () => {
  it("passes on this package", async () => {
    const { violations, checked } = await scanForkEntry(packageRoot);
    expect(violations).toEqual([]);
    expect(checked).toContain("src/fork.ts");
    expect(checked).toContain("src/client/index.ts");
  });

  it("rejects a second file that imports the fork", async () => {
    const root = await fixture({
      "src/fork.ts": 'export { CoreProvider } from "@multica/core/platform";\n',
      "src/client/leak.ts": 'import { CoreProvider } from "@multica/core/platform";\n',
    });
    const { violations } = await scanForkEntry(root);
    expect(violations.some((line: string) => line.startsWith("src/client/leak.ts"))).toBe(true);
  });

  it("rejects a private path even from the entry module", async () => {
    const root = await fixture({
      "src/fork.ts": 'export { api } from "@multica/core/api/client";\n',
    });
    const { violations } = await scanForkEntry(root);
    expect(violations).toHaveLength(1);
    expect(violations[0]).toContain("is not a public export");
  });

  it("rejects a relative path that walks out of the package", async () => {
    const root = await fixture({
      "src/fork.ts": 'import x from "../../core/platform/index.ts";\n',
    });
    const { violations } = await scanForkEntry(root);
    expect(violations[0]).toContain("resolves outside packages/dsh-plugin");
  });

  it("rejects a stylesheet that imports the fork", async () => {
    const root = await fixture({
      "src/fork.ts": "export {};\n",
      "src/client/extra.css": '@import "@multica/core/theme.css";\n',
    });
    const { violations } = await scanForkEntry(root);
    expect(violations[0]).toContain("the fork's styles may only be imported by src/client/island.css");
  });
});
