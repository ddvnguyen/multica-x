// @vitest-environment jsdom

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { SettingsRow } from "./settings-layout";

describe("SettingsRow stacked", () => {
  it("keeps the two-column layout by default", () => {
    const { container } = render(
      <SettingsRow label="Label" description="Hint">
        <p>body text</p>
      </SettingsRow>,
    );
    const row = container.firstElementChild as HTMLElement;
    expect(row.className).toContain("flex-row");
    expect(row.className).toContain("justify-between");
    const body = screen.getByText("body text").parentElement as HTMLElement;
    expect(body.className).toContain("max-w-[56%]");
  });

  it("stacks label above full-width children when stacked", () => {
    const { container } = render(
      <SettingsRow stacked label="Label" description="Hint">
        <p>body text</p>
      </SettingsRow>,
    );
    const row = container.firstElementChild as HTMLElement;
    expect(row.className).toContain("flex-col");
    expect(row.className).not.toContain("flex-row");
    const body = screen.getByText("body text").parentElement as HTMLElement;
    expect(body.className).toContain("w-full");
    expect(body.className).toContain("max-w-none");
    expect(body.className).not.toContain("max-w-[56%]");
  });
});
