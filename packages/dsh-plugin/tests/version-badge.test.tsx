import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { mmxBuild } from "@multica/views/build-info";
import { ISLAND_VERSION } from "../src/client/island-app";
import { IslandVersionBadge } from "../src/client/version-badge";

// The island reads the same injected __MMX_BUILD__ the real bundle carries
// (vitest.config.ts defines it with fixed inputs, M-155). If the define ever
// stops reaching the source, mmxBuild() is null and every assertion here
// fails loudly instead of the badge silently disappearing on dsh-z.
describe("IslandVersionBadge", () => {
  it("shows the injected stamp, with UTC time and short sha in the tooltip", () => {
    const build = mmxBuild();
    expect(build).not.toBeNull();

    render(<IslandVersionBadge />);
    const badge = screen.getByTestId("mmx-badge");
    expect(badge).toHaveTextContent(build!.stamp);
    expect(badge).toHaveAttribute("title", `${build!.utc} · ${build!.sha}`);
  });

  it("sends the same string as the panel's client identity", () => {
    const build = mmxBuild();
    expect(build).not.toBeNull();
    // identity.version is what multica-be records (island-app.tsx): a server
    // log line must name the same build the Settings row and badge show.
    expect(ISLAND_VERSION).toBe(build!.stamp);
    expect(ISLAND_VERSION).not.toBe("0.1.0");
  });
});
