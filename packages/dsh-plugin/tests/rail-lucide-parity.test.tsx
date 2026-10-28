import { renderToStaticMarkup } from "react-dom/server";
import { createElement } from "react";
import { describe, expect, it } from "vitest";
import * as Lucide from "lucide-react";
import { createRailIcon } from "../src/client/seat";

const GLYPHS = [
  { glyph: "issues", Icon: Lucide.ListTodo },
  { glyph: "agents", Icon: Lucide.Bot },
  { glyph: "skills", Icon: Lucide.BookOpenText },
  { glyph: "inbox", Icon: Lucide.Inbox },
  { glyph: "chat", Icon: Lucide.MessageSquare },
  { glyph: "settings", Icon: Lucide.Settings },
] as const;

function svgInner(markup: string): string {
  const match = markup.match(/<svg[^>]*>(.*)<\/svg>/s);
  return match?.[1] ?? "";
}

describe("rail glyph lucide parity", () => {
  for (const { glyph, Icon } of GLYPHS) {
    it(`${glyph} matches lucide`, () => {
      const RailIcon = createRailIcon(createElement as never, glyph as never);
      const railMarkup = renderToStaticMarkup(
        createElement(RailIcon as never, { size: 16 }),
      );
      const lucideMarkup = renderToStaticMarkup(
        createElement(Icon as never, { size: 16 }),
      );
      expect(svgInner(railMarkup)).toBe(svgInner(lucideMarkup));
    });
  }
});
