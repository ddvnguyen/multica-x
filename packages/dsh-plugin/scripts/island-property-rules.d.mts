/** Types for `island-property-rules.mjs`, which only the Vite configs and tests load. */

import type { Plugin } from "vite";

export declare const PROPERTY_RULES_EXPORT: string;
export declare function extractPropertyRules(css: string): string;
export declare function inlineCssSource(code: string): string | null;
export declare function islandPropertyRules(): Plugin;
