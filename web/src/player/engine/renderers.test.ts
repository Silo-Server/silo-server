import { describe, expect, it, vi } from "vitest";
import { browserMapsToSDR, Canvas2DRenderer, type EnginePicture } from "./renderers";

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

describe("Canvas2DRenderer", () => {
  it("turns and mirrors a rotated picture to fill its upright letterbox", () => {
    const calls: unknown[][] = [];
    const record =
      (name: string) =>
      (...args: unknown[]) =>
        calls.push([name, ...args]);
    const context = {
      fillRect: vi.fn(),
      translate: record("translate"),
      scale: record("scale"),
      rotate: record("rotate"),
      drawImage: (_frame: unknown, ...args: unknown[]) => calls.push(["drawImage", ...args]),
      resetTransform: record("resetTransform"),
    };
    const canvas = document.createElement("canvas");
    vi.spyOn(canvas, "getContext").mockReturnValue(context as never);
    Object.defineProperty(canvas, "clientWidth", { value: 1600 });
    Object.defineProperty(canvas, "clientHeight", { value: 900 });
    vi.stubGlobal("devicePixelRatio", 1);

    // A 1920x1080 phone recording stored landscape, shown portrait: 1080x1920.
    new Canvas2DRenderer(canvas).draw(
      { kind: "frame", frame: {} as VideoFrame, timestamp: 0, duration: 0 },
      { width: 1080, height: 1920, rotation: 90, flip: true },
      "contain",
    );
    vi.unstubAllGlobals();

    // The portrait letterbox is 506.25x900 centered at (800, 450); the
    // landscape picture is drawn 900x506.25 into it, turned a quarter.
    expect(calls).toEqual([
      ["translate", 800, 450],
      ["scale", -1, 1],
      ["rotate", Math.PI / 2],
      ["drawImage", -450, -253.125, 900, 506.25],
      ["resetTransform"],
    ]);
  });
});
