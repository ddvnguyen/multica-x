// @vitest-environment node
import { describe, expect, it } from "vitest";
import { wrapClientBundle } from "../scripts/wrap-client.mjs";

describe("wrap-client bundle isolation (M-168)", () => {
  it("keeps a bundle-local `api` function callable after wrapping", () => {
    // Mimics the minified dnd-kit shape that collided with the wrapper's
    // `var api = module.exports`: a bundle-local function named `api` that is
    // invoked internally and exported.
    // Lazy reference: `announce` calls `api` at invocation time, after the
    // wrapper has run. In the buggy (non-isolated) wrapper, `var api =
    // module.exports` rebinds the same function-scope `api` to the exports
    // object, so `announce()` throws "api is not a function".
    const bundle = [
      "function announce() { return api(); }",
      "var api = function () { return 42; };",
      "module.exports = { announce: announce, api: api, configureHostRuntime: function () {} };",
    ].join("\n");

    const artifact = wrapClientBundle(bundle, "@multica/dsh-plugin-test");

    // Capture the spec the wrapper hands to the dsh module loader.
    const captured: { factory?: (require: unknown) => unknown } = {};
    (globalThis as { window?: unknown }).window = {
      __ModuleLoader__: {
        load(spec: { factory: (require: unknown) => unknown }) {
          captured.factory = spec.factory;
        },
      },
    };

    // Evaluate the wrapper artifact so it calls window.__ModuleLoader__.load.
    // eslint-disable-next-line no-eval
    eval(artifact);

    const factory = captured.factory;
    expect(typeof factory).toBe("function");

    const result = factory?.(() => null) as { announce: () => number; api: () => number };
    expect(typeof result.announce).toBe("function");
    expect(result.announce()).toBe(42);
    expect(result.api()).toBe(42);
  });

  it("wraps the bundle in an IIFE so wrapper locals cannot shadow bundle locals", () => {
    const artifact = wrapClientBundle("module.exports = {};", "@multica/dsh-plugin-test");
    expect(artifact).toContain("(function (module, exports) {");
  });
});
