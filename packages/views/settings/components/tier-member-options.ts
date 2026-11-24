import type { RuntimeModel } from "@multica/core/types";
import { findModelCapabilityEntry } from "../../agents/components/inspector/model-capability";

type ModelCatalog = readonly RuntimeModel[] | null;

export interface TierMemberOptions {
  model: string;
  thinkingLevel: string;
}

export function providerForMember(input: {
  runtimeId: string;
  legacyProvider: string;
  runtimes: readonly { id: string; provider: string }[];
}): string {
  return (
    input.runtimes.find((runtime) => runtime.id === input.runtimeId)?.provider ??
    input.legacyProvider.trim()
  );
}

/**
 * Keeps a member's configured values when discovery cannot authoritatively
 * answer. A successful catalog clears only values the new model/runtime no
 * longer supports.
 */
export function memberAfterRuntimeChange(input: {
  model: string;
  thinkingLevel: string;
  provider: string;
  catalog: ModelCatalog;
}): TierMemberOptions {
  if (input.catalog === null) {
    return { model: input.model, thinkingLevel: input.thinkingLevel };
  }

  if (!input.model) return { model: "", thinkingLevel: "" };
  const entry = findModelCapabilityEntry(input.catalog, input.model, input.provider);
  if (!entry) return { model: "", thinkingLevel: "" };
  const levels = entry.thinking?.supported_levels ?? [];
  return {
    model: input.model,
    thinkingLevel:
      input.thinkingLevel && !levels.some((level) => level.value === input.thinkingLevel)
        ? ""
        : input.thinkingLevel,
  };
}

export function memberAfterModelChange(input: {
  model: string;
  thinkingLevel: string;
  provider: string;
  catalog: ModelCatalog;
}): TierMemberOptions {
  if (input.catalog === null || !input.model) {
    return { model: input.model, thinkingLevel: input.thinkingLevel };
  }

  const entry = findModelCapabilityEntry(input.catalog, input.model, input.provider);
  if (!entry) {
    return { model: input.model, thinkingLevel: input.thinkingLevel };
  }
  const levels = entry.thinking?.supported_levels ?? [];
  return {
    model: input.model,
    thinkingLevel:
      input.thinkingLevel && !levels.some((level) => level.value === input.thinkingLevel)
        ? ""
        : input.thinkingLevel,
  };
}

export function thinkingLevelsForMember(
  catalog: readonly RuntimeModel[] | null,
  model: string,
  provider: string,
) {
  if (!catalog || !model) return [];
  return findModelCapabilityEntry(catalog, model, provider)?.thinking?.supported_levels ?? [];
}
