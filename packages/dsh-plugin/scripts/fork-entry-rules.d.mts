export declare const FORK_ENTRY: string;
export declare const STYLE_ENTRY: string;
export declare const FORK_SCOPE: string;
export declare function scanForkEntry(
  packageRoot: string,
  io?: { readFileImpl?: typeof import("node:fs/promises").readFile; readdirImpl?: typeof import("node:fs/promises").readdir },
): Promise<{ violations: string[]; checked: string[] }>;
