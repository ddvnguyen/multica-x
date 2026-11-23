"use client";

import { TiersTab } from "./tiers-tab";

/**
 * Team → Tier config (M-545). The Owner rejected the side-menu entry that
 * navigated to Settings → Tiers: the tier config belongs in the Team main
 * panel, beside Agents and Members. The page is a host only — it renders the
 * existing tab, so the roster, the per-role list and both dialogs stay one
 * implementation, and Settings → Tiers keeps working unchanged.
 */
export function TierConfigPage() {
  return (
    // The dashboard card clips its children (`overflow-hidden`), so the page
    // owns the scroll and the bottom gutter.
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto overscroll-contain">
      <div className="mx-auto w-full max-w-5xl px-6 py-6 pb-12">
        <TiersTab />
      </div>
    </div>
  );
}
