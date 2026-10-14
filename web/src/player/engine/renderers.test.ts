import { describe, expect, it } from "vitest";
import { browserMapsToSDR, type EnginePicture } from "./renderers";

const frame = (transfer: string | null): EnginePicture => ({
  kind: "frame",
  frame: { colorSpace: { transfer } } as unknown as VideoFrame,
  timestamp: 0,
  duration: 0,
});

describe("browserMapsToSDR", () => {
  it("leaves frames that kept a PQ or HLG tag to the browser's own mapping", () => {
    // Hardware decoders ignore the sRGB override and Chromium tone-maps on import.
    expect(browserMapsToSDR(frame("pq"))).toBe(true);
    expect(browserMapsToSDR(frame("hlg"))).toBe(true);
  });

  it("tone-maps frames that took the override and uploaded planes itself", () => {
    expect(browserMapsToSDR(frame("iec61966-2-1"))).toBe(false);
    expect(browserMapsToSDR(frame(null))).toBe(false);
    expect(
      browserMapsToSDR({
        kind: "planes",
        planes: {} as never,
        timestamp: 0,
        duration: 0,
      }),
    ).toBe(false);
  });
});
