import { describe, expect, it } from "vitest";
import { parseHWDeviceList, toggleHWDevice } from "./playbackSettings.utils";

const DETECTED = ["/dev/dri/renderD128", "/dev/dri/renderD129"];

describe("parseHWDeviceList", () => {
  it("returns empty for unset or blank values", () => {
    expect(parseHWDeviceList(undefined)).toEqual([]);
    expect(parseHWDeviceList("")).toEqual([]);
    expect(parseHWDeviceList(" , ")).toEqual([]);
  });

  it("splits and trims a comma list", () => {
    expect(parseHWDeviceList(" /dev/dri/renderD128 ,/dev/dri/renderD129")).toEqual(DETECTED);
  });
});

describe("toggleHWDevice", () => {
  it("adds a device to an empty selection", () => {
    expect(toggleHWDevice("", "/dev/dri/renderD129", DETECTED)).toBe("/dev/dri/renderD129");
  });

  it("keeps detection order regardless of click order", () => {
    const afterSecond = toggleHWDevice("", "/dev/dri/renderD129", DETECTED);
    const afterBoth = toggleHWDevice(afterSecond, "/dev/dri/renderD128", DETECTED);
    expect(afterBoth).toBe("/dev/dri/renderD128,/dev/dri/renderD129");
  });

  it("removes an already-selected device", () => {
    expect(toggleHWDevice("/dev/dri/renderD128,/dev/dri/renderD129", "/dev/dri/renderD128", DETECTED)).toBe(
      "/dev/dri/renderD129",
    );
  });

  it("preserves selected devices missing from the current detection pass", () => {
    expect(toggleHWDevice("/dev/dri/renderD200", "/dev/dri/renderD128", DETECTED)).toBe(
      "/dev/dri/renderD128,/dev/dri/renderD200",
    );
  });
});
