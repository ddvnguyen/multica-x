import { providerDisplayName } from "@multica/core/runtimes";
import type { ModelTier, ModelTierMember } from "@multica/core/types";

/**
 * M-493: the flat "#tier · model" list no longer describes the data. A tier is
 * now a profile (M-481) with ordered (harness, model, runtime) members, and the
 * same model can be served by several harnesses. This module turns the tier
 * list plus its member lists into the model → harness → runtime tree the
 * pickers navigate. It is deliberately pure so the ordering and fallback rules
 * are testable without a DOM or a Query client.
 *
 * "Harness" is the UI name for `provider` (there is no harness table); the
 * runtime is the optional `runtime_id` on a member. A tier with no members —
 * an older backend, or a row created before M-481 — still appears through its
 * legacy provider/model/runtime columns so nobody loses a selectable tier.
 */

/** One leaf of the tree: a concrete (tier, optional member) pair. */
export interface TierPickEntry {
  /** Stable React key: tier id plus member id (or "legacy"). */
  key: string;
  tier: ModelTier;
  member?: ModelTierMember;
  model: string;
  provider: string;
  runtimeId?: string;
  /** Human labels for each step. */
  modelLabel: string;
  providerLabel: string;
  runtimeLabel: string;
  tierLabel: string;
  /** Sort weight: tier idx first, then the member's pick position. */
  order: number;
}

export interface TierPickHarness {
  /** Lowercased provider slug. */
  key: string;
  label: string;
  entries: TierPickEntry[];
}

export interface TierPickModel {
  /** Lowercased model id, used for grouping and step transitions. */
  key: string;
  label: string;
  harnesses: TierPickHarness[];
}

export interface BuildTierPickTreeOptions {
  /** Include disabled tiers/members (admin editors need them; pickers do not). */
  includeDisabled?: boolean;
  /** Resolves a runtime id to its display name from the runtime list. */
  runtimeLabel?: (runtimeId: string) => string | undefined;
}

const byOrder = (a: TierPickEntry, b: TierPickEntry): number =>
  a.order - b.order || a.tier.name.localeCompare(b.tier.name) || a.key.localeCompare(b.key);

/**
 * Group the eligible tiers into model → harness → runtime entries.
 *
 * Callers pass an already agent-filtered tier list; this function only applies
 * the enabled/disabled rule and the member fallbacks, so the same tree can be
 * reused by single-select and multi-select pickers.
 */
export function buildTierPickTree(
  tiers: readonly ModelTier[],
  membersByTier: ReadonlyMap<string, readonly ModelTierMember[]> = new Map(),
  options: BuildTierPickTreeOptions = {},
): TierPickModel[] {
  const entries: TierPickEntry[] = [];

  for (const tier of tiers) {
    if (!options.includeDisabled && tier.enabled === false) continue;

    const members = (membersByTier.get(tier.id) ?? []).filter(
      (member) => options.includeDisabled || member.enabled !== false,
    );
    const orderedMembers = [...members].sort(
      (a, b) => a.position - b.position || a.created_at.localeCompare(b.created_at) || a.id.localeCompare(b.id),
    );
    const effectiveMembers: Array<ModelTierMember | undefined> = orderedMembers.length
      ? orderedMembers
      : [undefined];

    for (const member of effectiveMembers) {
      const model = (member?.model || tier.model || "").trim();
      const provider = (member?.provider || tier.provider || "").trim();
      if (!model && !provider) continue;

      const runtimeId = member?.runtime_id || tier.runtime_id || undefined;
      const runtimeLabel = runtimeId
        ? (options.runtimeLabel?.(runtimeId) || runtimeId.slice(0, 8))
        : provider
          ? providerDisplayName(provider)
          : "Runtime default";

      entries.push({
        key: `${tier.id}:${member?.id ?? "legacy"}`,
        tier,
        ...(member ? { member } : {}),
        model,
        provider,
        ...(runtimeId ? { runtimeId } : {}),
        modelLabel: model || "—",
        providerLabel: provider ? providerDisplayName(provider) : "—",
        runtimeLabel,
        tierLabel: `#${tier.name}`,
        order: tier.idx * 10_000 + (member?.position ?? 0),
      });
    }
  }

  entries.sort(byOrder);

  const models = new Map<string, TierPickModel>();
  for (const entry of entries) {
    const modelKey = entry.model.toLowerCase() || "—";
    let model = models.get(modelKey);
    if (!model) {
      model = { key: modelKey, label: entry.modelLabel, harnesses: [] };
      models.set(modelKey, model);
    }

    const providerKey = entry.provider.toLowerCase() || "—";
    let harness = model.harnesses.find((candidate) => candidate.key === providerKey);
    if (!harness) {
      harness = { key: providerKey, label: entry.providerLabel, entries: [] };
      model.harnesses.push(harness);
    }
    harness.entries.push(entry);
  }

  return [...models.values()].sort(
    (a, b) =>
      Math.min(...a.harnesses.flatMap((h) => h.entries.map((e) => e.order))) -
      Math.min(...b.harnesses.flatMap((h) => h.entries.map((e) => e.order))),
  );
}

/** The distinct models in a tree, useful for the first step's count badge. */
export function tierPickModelCount(model: TierPickModel): number {
  return model.harnesses.reduce((total, harness) => total + harness.entries.length, 0);
}
