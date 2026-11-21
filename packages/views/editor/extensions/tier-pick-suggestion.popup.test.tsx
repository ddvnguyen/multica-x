import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import enEditor from "../../locales/en/editor.json";
import { TierPickList, type TierPickItem } from "./mention-suggestion";

// M-492: the `@agent#tier` picker used to render bare, transparent rows. The
// underlying issue activity bled through the list, so rows looked stacked and
// duplicated in the deployed UI. It must use the same floating-menu chrome as
// the other editor suggestion lists (see MentionList / SlashCommandList).
const ITEMS: TierPickItem[] = [
  { id: "tier-a", name: "fast", model: "model-a", disabled: false },
  { id: "tier-b", name: "slow", model: "model-b", disabled: true },
];

function renderList(items: TierPickItem[] = ITEMS, command = vi.fn()) {
  render(
    <I18nProvider locale="en" resources={{ en: { editor: enEditor } }}>
      <TierPickList items={items} command={command} />
    </I18nProvider>,
  );
  return command;
}

describe("TierPickList popup chrome (M-492)", () => {
  it("renders an opaque, bounded floating menu instead of transparent rows", () => {
    renderList();
    const listbox = screen.getByRole("listbox");

    expect(listbox).toHaveAttribute("data-mc", "editor-menu");
    expect(listbox).toHaveAttribute("data-kind", "tier");
    // Opaque surface + border + shadow keep page text from bleeding through.
    expect(listbox).toHaveClass("bg-popover", "border", "shadow-md", "rounded-md");
    // One scroll container clamped to the floating-ui viewport budget.
    expect(listbox).toHaveClass("overflow-y-auto");
    expect(listbox.className).toContain(
      "max-h-[min(300px,var(--suggestion-available-height,300px))]",
    );
  });

  it("keeps enabled tiers selectable and flags disabled tiers", async () => {
    const user = userEvent.setup();
    const command = renderList();

    await user.click(screen.getByRole("option", { name: /#fast/ }));
    expect(command).toHaveBeenCalledWith(ITEMS[0]);
    expect(screen.getByRole("option", { name: /#slow/ })).toBeDisabled();
  });
});
