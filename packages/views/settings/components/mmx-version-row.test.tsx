import { fireEvent, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import * as NavigationContext from "../../navigation/context";
import { renderWithI18n } from "../../test/i18n";
import { MmxVersionRow } from "./mmx-version-row";

// The build the island's bundle injects (M-155). The module is mocked rather
// than defined away: the row's job is to render whatever the host stamped,
// and the "host stamped nothing" half of that contract is the first test.
const build = vi.hoisted(() => ({
  value: null as null | {
    version: string;
    stamp: string;
    utc: string;
    sha: string;
  },
}));

vi.mock("../../build-info", () => ({
  mmxBuild: () => build.value,
}));

vi.mock("../../navigation/context", async (importOriginal) => ({
  ...(await importOriginal<typeof NavigationContext>()),
  useNavigation: () => ({
    searchParams: new URLSearchParams(""),
    hash: "",
    pathname: "/acme/settings",
    replace: vi.fn(),
    push: vi.fn(),
    back: vi.fn(),
    getShareableUrl: (path: string) => path,
  }),
}));

const stamped = {
  version: "9.9.9",
  stamp: "9.9.9-26OCT28.21.58",
  utc: "2026-10-28T14:58Z",
  sha: "abc1234",
};

describe("MmxVersionRow", () => {
  beforeEach(() => {
    build.value = null;
    Object.assign(navigator, {
      clipboard: { writeText: vi.fn().mockResolvedValue(undefined) },
    });
  });

  it("renders nothing when the host injected no build", () => {
    const { container } = renderWithI18n(<MmxVersionRow />);
    expect(screen.queryByTestId("mmx-version")).toBeNull();
    expect(container).toBeEmptyDOMElement();
  });

  it("shows the stamp and copies version · sha · route", () => {
    build.value = stamped;
    renderWithI18n(<MmxVersionRow />);

    expect(screen.getByTestId("mmx-version")).toHaveTextContent(stamped.stamp);

    fireEvent.click(screen.getByRole("button", { name: "Copy version" }));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(
      `${stamped.stamp} · ${stamped.sha} · /acme/settings`,
    );
  });
});
