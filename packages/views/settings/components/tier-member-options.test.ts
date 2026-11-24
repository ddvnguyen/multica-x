// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { RuntimeModel } from "@multica/core/types";
import {
  memberAfterModelChange,
  memberAfterRuntimeChange,
  providerForMember,
  thinkingLevelsForMember,
} from "./tier-member-options";

const CATALOG_A: RuntimeModel[] = [
  {
    id: "model-a",
    label: "Model A",
    thinking: {
      supported_levels: [
        { value: "low", label: "Low" },
        { value: "high", label: "High" },
      ],
    },
  },
  { id: "custom-a", label: "Custom A" },
];

const CATALOG_B: RuntimeModel[] = [
  {
    id: "model-a",
    label: "Model A",
    thinking: { supported_levels: [{ value: "medium", label: "Medium" }] },
  },
];

describe("tier member runtime options", () => {
  it("uses the selected runtime and model capability for thinking levels", () => {
    expect(thinkingLevelsForMember(CATALOG_A, "model-a", "codex")).toEqual([
      { value: "low", label: "Low" },
      { value: "high", label: "High" },
    ]);
    expect(thinkingLevelsForMember(CATALOG_B, "model-a", "claude")).toEqual([
      { value: "medium", label: "Medium" },
    ]);
    expect(thinkingLevelsForMember(CATALOG_A, "custom-a", "codex")).toEqual(
      [],
    );
  });

  it("revalidates model and thinking when the runtime changes", () => {
    expect(
      memberAfterRuntimeChange({
        model: "missing-model",
        thinkingLevel: "high",
        provider: "claude",
        catalog: CATALOG_B,
      }),
    ).toEqual({ model: "", thinkingLevel: "" });
    expect(
      memberAfterRuntimeChange({
        model: "model-a",
        thinkingLevel: "high",
        provider: "claude",
        catalog: CATALOG_B,
      }),
    ).toEqual({ model: "model-a", thinkingLevel: "" });
  });

  it("preserves custom values when the catalog is unknown or the model is custom", () => {
    expect(
      memberAfterRuntimeChange({
        model: "legacy-model",
        thinkingLevel: "legacy-level",
        provider: "claude",
        catalog: null,
      }),
    ).toEqual({ model: "legacy-model", thinkingLevel: "legacy-level" });
    expect(
      memberAfterModelChange({
        model: "custom-model",
        thinkingLevel: "legacy-level",
        provider: "codex",
        catalog: CATALOG_A,
      }),
    ).toEqual({ model: "custom-model", thinkingLevel: "legacy-level" });
  });

  it("clears an unsupported thinking value after a known model change", () => {
    expect(
      memberAfterModelChange({
        model: "model-a",
        thinkingLevel: "high",
        provider: "claude",
        catalog: CATALOG_B,
      }),
    ).toEqual({ model: "model-a", thinkingLevel: "" });
  });

  it("derives provider from runtime and preserves an unbound legacy provider", () => {
    const runtimes = [{ id: "runtime-1", provider: "claude" }];
    expect(
      providerForMember({
        runtimeId: "runtime-1",
        legacyProvider: "old-provider",
        runtimes,
      }),
    ).toBe("claude");
    expect(
      providerForMember({
        runtimeId: "",
        legacyProvider: " legacy-provider ",
        runtimes,
      }),
    ).toBe("legacy-provider");
  });
});
