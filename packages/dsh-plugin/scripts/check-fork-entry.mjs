import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { FORK_ENTRY, scanForkEntry, STYLE_ENTRY } from "./fork-entry-rules.mjs";

/** Fail the build unless the plugin reaches the fork through one entry each. */
const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const { violations, checked } = await scanForkEntry(packageRoot);

if (violations.length > 0) {
  console.error("dsh-plugin provenance gate failed:");
  for (const violation of violations) console.error(`  - ${violation}`);
  console.error(
    `\nThe fork may only be reached through ${FORK_ENTRY} (modules) and ${STYLE_ENTRY} (styles).`,
  );
  process.exit(1);
}

console.log(
  `dsh-plugin provenance gate: ${checked.length} files checked, fork reached only through ${FORK_ENTRY} and ${STYLE_ENTRY}`,
);
