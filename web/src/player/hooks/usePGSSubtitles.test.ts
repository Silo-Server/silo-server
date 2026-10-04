import { describe, expect, it } from "vitest";
import { videoContentRect } from "../utils/videoFit";
import { pgsPlaneRect } from "./usePGSSubtitles";

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

  it("stretches anamorphic SD planes to the picture", () => {
    const picture = videoContentRect(1600, 900, 853, 480, "contain");
    expect(pgsPlaneRect(picture, 720, 480)).toEqual(picture);
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
