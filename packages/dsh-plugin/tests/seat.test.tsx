import { createElement } from "react";
import { describe, expect, it } from "vitest";
import type { DshClientContext } from "../src/dsh";
import { apply, configureHostRuntime, inject, MULTICA_PANEL_ID } from "../src/client/index";
import { createRailIcon, createSeatComponent } from "../src/client/seat";

interface Registration {
  options: Record<string, unknown>;
  component: unknown;
}

function fakeSlots() {
  const registrations: Registration[] = [];
  const injected: string[] = [];
  const ctx: DshClientContext = {
    slots: {
      inject(key: string, register: () => unknown) {
        injected.push(key);
        return register();
      },
      register(options, component) {
        registrations.push({
          options: options as unknown as Record<string, unknown>,
          component,
        });
        return () => {};
      },
    },
  };
  return { registrations, injected, ctx };
}

describe("client apply", () => {
  it("adds one rail row and one keyed main seat", () => {
    const { ctx, registrations, injected } = fakeSlots();
    apply(ctx);

    expect(inject).toEqual(["slots"]);
    expect(injected).toEqual(["sidebar.panellist", "main"]);

    const row = registrations.find((entry) => entry.options.name === "sidebar.panellist");
    expect(row?.options).toMatchObject({ id: MULTICA_PANEL_ID, label: "Multica" });
    // Additive: a list id of its own, so no shipped row is replaced.
    expect(row?.options.key).toBeUndefined();

    const seat = registrations.find((entry) => entry.options.name === "main");
    expect(seat?.options).toMatchObject({ key: MULTICA_PANEL_ID, priority: -100 });
  });
});

describe("seat component", () => {
  it("renders one host element and no hooks", () => {
    configureHostRuntime(createElement as never);
    const Seat = createSeatComponent(createElement as never);
    // React 18 calls a component as a plain function; a hook here would throw.
    const element = Seat() as { type: string; props: Record<string, unknown> };
    expect(element.type).toBe("div");
    expect(element.props["data-multica-panel"]).toBe("multica");
    expect(typeof element.props.ref).toBe("function");
    configureHostRuntime(null);
  });

  it("sizes the rail glyph from the owner props", () => {
    const Icon = createRailIcon(createElement as never);
    const element = Icon({ size: 18 }) as {
      type: string;
      props: Record<string, unknown>;
    };
    expect(element.type).toBe("svg");
    expect(element.props.width).toBe(18);
    expect(element.props.height).toBe(18);
  });
});
