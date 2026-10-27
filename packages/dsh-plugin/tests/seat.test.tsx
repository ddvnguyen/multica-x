import { createElement } from "react";
import { describe, expect, it } from "vitest";
import type { DshClientContext } from "../src/dsh";
import {
  apply,
  configureHostRuntime,
  inject,
  MULTICA_ENTRIES,
  MULTICA_PANEL_ID,
} from "../src/client/index";
import { FAMILY_ROOTS } from "../src/client/navigation";
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

function byName(registrations: Registration[], name: string): Registration[] {
  return registrations.filter((entry) => entry.options.name === name);
}

describe("client apply", () => {
  it("adds one rail row and one keyed main seat per view", () => {
    const { ctx, registrations, injected } = fakeSlots();
    apply(ctx);

    expect(inject).toEqual(["slots"]);
    // Two contributions per entry, and each waits for its own slot.
    expect(injected).toEqual(
      MULTICA_ENTRIES.flatMap(() => ["sidebar.panellist", "main"]),
    );

    const rows = byName(registrations, "sidebar.panellist");
    const seats = byName(registrations, "main");
    expect(rows).toHaveLength(6);
    expect(seats).toHaveLength(6);
    expect(MULTICA_ENTRIES).toHaveLength(6);
  });

  it("labels the six entries and keeps the rail order", () => {
    const { ctx, registrations } = fakeSlots();
    apply(ctx);

    const rows = byName(registrations, "sidebar.panellist");
    expect(rows.map((entry) => entry.options.label)).toEqual([
      "Multica Issues",
      "Multica Agents",
      "Multica Skills",
      "Multica Inbox",
      "Multica Chat",
      "Multica Settings",
    ]);
    // A list slot has no grouping API: adjacent order is the grouping, and each
    // row's id is its own, so no shipped row is replaced.
    expect(rows.map((entry) => entry.options.order)).toEqual([30, 31, 32, 33, 34, 35]);
    for (const row of rows) expect(row.options.key).toBeUndefined();
    expect(new Set(rows.map((entry) => entry.options.id)).size).toBe(6);
    expect(rows[0]?.options.id).toBe(MULTICA_PANEL_ID);
  });

  it("addresses each rail row with a matching main cell", () => {
    const { ctx, registrations } = fakeSlots();
    apply(ctx);

    const rowIds = byName(registrations, "sidebar.panellist").map((entry) =>
      String(entry.options.id),
    );
    const seatKeys = byName(registrations, "main").map((entry) =>
      String(entry.options.key),
    );
    // dsh selects a panel only when a `main` cell claims the same id.
    expect(seatKeys).toEqual(rowIds);
    for (const seat of byName(registrations, "main")) {
      expect(seat.options.priority).toBe(-100);
    }
  });

  it("opens each entry at its own view", () => {
    configureHostRuntime(createElement as never);
    for (const entry of MULTICA_ENTRIES) {
      const Seat = createSeatComponent(createElement as never, {
        panelId: entry.id,
        route: entry.route,
      });
      const element = Seat() as { type: string; props: Record<string, unknown> };
      expect(element.props["data-multica-panel"]).toBe(entry.id);
    }
    // One entry per family, and the first is the board.
    expect(MULTICA_ENTRIES.map((entry) => entry.route)).toEqual([
      FAMILY_ROOTS.issues,
      FAMILY_ROOTS.agents,
      FAMILY_ROOTS.skills,
      FAMILY_ROOTS.inbox,
      FAMILY_ROOTS.chat,
      FAMILY_ROOTS.settings,
    ]);
    configureHostRuntime(null);
  });
});

describe("seat component", () => {
  it("renders one host element and no hooks", () => {
    configureHostRuntime(createElement as never);
    const Seat = createSeatComponent(createElement as never, {
      panelId: MULTICA_PANEL_ID,
      route: FAMILY_ROOTS.issues,
    });
    // React 18 calls a component as a plain function; a hook here would throw.
    const element = Seat() as { type: string; props: Record<string, unknown> };
    expect(element.type).toBe("div");
    expect(element.props["data-multica-panel"]).toBe("multica");
    expect(typeof element.props.ref).toBe("function");
    configureHostRuntime(null);
  });

  it("sizes the rail glyph from the owner props", () => {
    for (const glyph of [
      "issues",
      "agents",
      "skills",
      "inbox",
      "chat",
      "settings",
    ] as const) {
      const Icon = createRailIcon(createElement as never, glyph);
      const element = Icon({ size: 18 }) as {
        type: string;
        props: Record<string, unknown>;
      };
      expect(element.type).toBe("svg");
      expect(element.props.width).toBe(18);
      expect(element.props.height).toBe(18);
    }
  });

  it("draws a different shape for every entry", () => {
    const drawn = MULTICA_ENTRIES.map((entry) => {
      const Icon = createRailIcon(createElement as never, entry.glyph);
      const element = Icon({ size: 16 }) as { props: { children: unknown[] } };
      return JSON.stringify(element.props.children);
    });
    expect(new Set(drawn).size).toBe(MULTICA_ENTRIES.length);
  });
});
