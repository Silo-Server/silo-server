import { describe, expect, it } from "vitest";
import { videoContentRect } from "../utils/videoFit";
import { pgsPlaneRect, placePGSObject } from "./usePGSSubtitles";

describe("pgsPlaneRect", () => {
  it("covers the picture when the plane matches it", () => {
    const picture = videoContentRect(1600, 900, 1920, 1080, "contain");
    expect(pgsPlaneRect(picture, 1920, 1080)).toEqual(picture);
  });

  it("centers a 16:9 disc plane on a scope encode, reaching into the letterbox", () => {
    const picture = videoContentRect(1600, 900, 1920, 800, "contain");
    const plane = pgsPlaneRect(picture, 1920, 1080);
    expect(plane.width).toBeCloseTo(1600);
    expect(plane.height).toBeCloseTo(900);
    expect(plane.y).toBeCloseTo(0);
    expect(picture.y).toBeGreaterThan(plane.y);
  });

  it("matches heights for a pillarbox-cropped encode, reaching into the side bars", () => {
    // A 4:3 picture cropped from a 16:9 disc frame.
    const picture = videoContentRect(1600, 900, 1440, 1080, "contain");
    const plane = pgsPlaneRect(picture, 1920, 1080);
    expect(plane.width).toBeCloseTo(1600);
    expect(plane.height).toBeCloseTo(900);
    expect(plane.x).toBeCloseTo(0);
    expect(plane.y).toBeCloseTo(0);
  });

  it("stretches anamorphic SD planes to the picture", () => {
    const picture = videoContentRect(1600, 900, 853, 480, "contain");
    expect(pgsPlaneRect(picture, 720, 480)).toEqual(picture);
  });
});

describe("placePGSObject", () => {
  // A centered dialogue line near the bottom of a 1920x1080 plane.
  const line = { x: 400, y: 950, width: 1120, height: 80 };
  const source = { x: 0, y: 0, width: line.width, height: line.height };

  function place(boxWidth: number, boxHeight: number, fit: "contain" | "cover", inset = 0) {
    const picture = videoContentRect(boxWidth, boxHeight, 1920, 800, fit);
    return placePGSObject({
      object: line,
      source,
      plane: pgsPlaneRect(picture, 1920, 1080),
      planeWidth: 1920,
      planeHeight: 1080,
      boxWidth,
      boxHeight,
      bottomInsetPx: inset,
    });
  }

  it("keeps the disc position when nothing covers it", () => {
    const rect = place(1920, 1080, "contain");
    expect(rect).toEqual({ x: 400, y: 950, width: 1120, height: 80 });
  });

  it("lifts a lower line clear of the control bar", () => {
    const rect = place(1920, 1080, "contain", 140);
    expect(rect.y + rect.height).toBeCloseTo(1080 - 140);
    expect(rect.x).toBe(400);
  });

  it("leaves an upper line alone when the control bar is up", () => {
    const picture = videoContentRect(1920, 1080, 1920, 800, "contain");
    const rect = placePGSObject({
      object: { x: 400, y: 60 },
      source,
      plane: pgsPlaneRect(picture, 1920, 1080),
      planeWidth: 1920,
      planeHeight: 1080,
      boxWidth: 1920,
      boxHeight: 1080,
      bottomInsetPx: 140,
    });
    expect(rect.y).toBe(60);
  });

  it("keeps a line inside a narrow Fill viewport", () => {
    const rect = place(390, 844, "cover");
    expect(rect.x).toBeGreaterThanOrEqual(0);
    expect(rect.x + rect.width).toBeLessThanOrEqual(390);
    expect(rect.y + rect.height).toBeLessThanOrEqual(844);
  });
});

describe("videoContentRect", () => {
  it("letterboxes for contain and overflows for cover", () => {
    const contain = videoContentRect(1000, 1000, 1920, 1080, "contain");
    expect(contain.x).toBeCloseTo(0);
    expect(contain.y).toBeCloseTo(218.75);
    expect(contain.width).toBeCloseTo(1000);
    expect(contain.height).toBeCloseTo(562.5);
    const cover = videoContentRect(1000, 1000, 1920, 1080, "cover");
    expect(cover.height).toBe(1000);
    expect(cover.x).toBeLessThan(0);
  });
});
