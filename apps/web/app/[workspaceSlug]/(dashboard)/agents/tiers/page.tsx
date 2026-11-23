import { TierConfigPage } from "@multica/views/settings/components/tier-config-page";

// Team → Tier config (M-545). A static segment under /agents keeps the page
// in the Team space and resolves ahead of the /agents/:id detail route.
export default function AgentTierConfigRoute() {
  return <TierConfigPage />;
}
