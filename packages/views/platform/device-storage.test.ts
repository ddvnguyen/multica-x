import { afterEach, describe, expect, it } from "vitest";
import {
  deviceStorage,
  resetDeviceStorage,
  setDeviceStorage,
  type DeviceStorage,
} from "./device-storage";

function fakeStorage(): DeviceStorage & { values: Map<string, string> } {
  const values = new Map<string, string>();
  return {
    values,
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => void values.set(key, value),
    removeItem: (key) => void values.delete(key),
  };
}

afterEach(() => {
  resetDeviceStorage();
  window.localStorage.clear();
});

describe("device storage", () => {
  it("is the browser's own store when no host installs one", () => {
    expect(deviceStorage()).toBe(window.localStorage);
  });

  it("takes the host's adapter, so its keys never reach the bare store", () => {
    const adapter = fakeStorage();
    setDeviceStorage(adapter);

    deviceStorage()?.setItem("react-resizable-panels:x", "{}");

    expect(adapter.values.get("react-resizable-panels:x")).toBe("{}");
    expect(window.localStorage.getItem("react-resizable-panels:x")).toBeNull();
  });

  it("can keep device state for this page only", () => {
    setDeviceStorage(null);
    expect(deviceStorage()).toBeNull();
  });

  it("goes back to the browser's store on reset", () => {
    setDeviceStorage(fakeStorage());
    resetDeviceStorage();
    expect(deviceStorage()).toBe(window.localStorage);
  });
});
