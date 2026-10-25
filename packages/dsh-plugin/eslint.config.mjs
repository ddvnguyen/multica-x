import reactConfig from "@multica/eslint-config/react";

/**
 * The fast mirror of the provenance gate (`scripts/check-fork-entry.mjs`).
 *
 * Only `src/fork.ts` may name the fork; every other file imports `../fork`. The
 * editor gets the error immediately, and the script — which also checks that
 * each specifier is a *public* export — is the gate that runs in CI.
 */
export default [
  ...reactConfig,
  {
    ignores: ["lib/", "node_modules/"],
  },
  {
    files: ["src/**/*.{ts,tsx}"],
    ignores: ["src/fork.ts"],
    rules: {
      "no-restricted-imports": [
        "error",
        {
          patterns: [
            {
              group: ["@multica/*", "@multica/**"],
              message:
                "Import the fork through ./fork (or ../fork). The provenance gate rejects any other @multica/* specifier.",
            },
          ],
        },
      ],
    },
  },
  {
    // Everything the island imports is bundled into the client artifact, the
    // fork and React 19 alike, so those are build-time dependencies here rather
    // than runtime ones the package would publish. `ws` stays a real dependency:
    // the host half imports it from the dsh process.
    files: ["src/fork.ts", "src/client/**/*.{ts,tsx}"],
    rules: {
      "import-x/no-extraneous-dependencies": [
        "error",
        { devDependencies: true, peerDependencies: true },
      ],
    },
  },
  {
    // Build-time Node scripts: the two globals they actually use.
    files: ["scripts/**/*.mjs"],
    languageOptions: {
      globals: { console: "readonly", process: "readonly" },
    },
  },
];
