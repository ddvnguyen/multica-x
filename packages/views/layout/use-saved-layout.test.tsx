import { render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetDeviceStorage, setDeviceStorage } from "../platform/device-storage";

// The library call is what a split page makes; capture its arguments instead of
// mounting a real group, whose sizes depend on layout jsdom does not compute.
const calls: Array<Record<string, unknown>> = [];
vi.mock("react-resizable-panels", () => ({
  useDefaultLayout: (options: Record<string, unknown>) => {
    calls.push(options);
    return { defaultLayout: undefined, onLayoutChanged: () => undefined };
  },
}));

const { useSavedLayout } = await import("./use-saved-layout");

/** A page using the hook, the way the issue, project, object and builder pages do. */
function Page({ id, panelIds }: { id: string; panelIds?: string[] }) {
  useSavedLayout(id, panelIds);
  return null;
}

beforeEach(() => {
  calls.length = 0;
});

afterEach(() => {
  resetDeviceStorage();
});

describe("useSavedLayout", () => {
  it("leaves the library on its own storage for web and desktop", () => {
    render(<Page id="multica_project_detail_layout" />);
    expect(calls).toHaveLength(1);
    // `undefined` is the library's "use localStorage": the default path stays
    // byte-for-byte what it was before the island needed an adapter.
    expect(calls[0]).toMatchObject({ id: "multica_project_detail_layout", storage: undefined });
  });

  it("hands the host's adapter to the library, so the key is namespaced", () => {
    const adapter = {
      getItem: () => null,
      setItem: () => undefined,
      removeItem: () => undefined,
    };
    setDeviceStorage(adapter);
    render(<Page id="multica_agent_builder_layout" />);
    expect(calls[0]).toMatchObject({ id: "multica_agent_builder_layout", storage: adapter });
  });

  it("passes panel ids through for a group with conditionally-rendered panels", () => {
    render(<Page id="x" panelIds={["a", "b"]} />);
    expect(calls[0]).toMatchObject({ id: "x", panelIds: ["a", "b"] });
  });
});
