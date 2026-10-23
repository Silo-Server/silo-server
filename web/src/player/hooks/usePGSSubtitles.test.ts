import { describe, expect, it } from "vitest";
import { videoContentRect, type FitRect } from "../utils/videoFit";
import { pgsPlaneRect, placePGSObjects } from "./usePGSSubtitles";

type PlaceArgs = Omit<Parameters<typeof placePGSObjects>[0], "objects"> & {
  object: { x: number; y: number };
  source: { x: number; y: number; width: number; height: number };
};

function placePGSObject({ object, source, ...rest }: PlaceArgs): FitRect {
  const [rect] = placePGSObjects({ ...rest, objects: [{ object, source }] });
  if (!rect) throw new Error("expected a placed object");
  return rect;
}

function two(rects: FitRect[]): [FitRect, FitRect] {
  const [first, second] = rects;
  if (!first || !second) throw new Error("expected two placed objects");
  return [first, second];
}

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

describe("placePGSObjects", () => {
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

  it("shrinks an object taller than the room above the control bar", () => {
    const picture = videoContentRect(640, 360, 1920, 1080, "contain");
    const tall = { x: 160, y: 300, width: 1600, height: 700 };
    const rect = placePGSObject({
      object: tall,
      source: { x: 0, y: 0, width: tall.width, height: tall.height },
      plane: pgsPlaneRect(picture, 1920, 1080),
      planeWidth: 1920,
      planeHeight: 1080,
      boxWidth: 640,
      boxHeight: 360,
      bottomInsetPx: 120,
    });
    expect(rect.y).toBeGreaterThanOrEqual(0);
    expect(rect.y + rect.height).toBeLessThanOrEqual(360 - 120);
    expect(rect.width / rect.height).toBeCloseTo(1600 / 700);
  });

  // Two dialogue lines as two objects, as Blu-ray subtitles often are.
  const upperLine = { object: { x: 560, y: 900 }, source: { x: 0, y: 0, width: 800, height: 60 } };
  const lowerLine = { object: { x: 660, y: 980 }, source: { x: 0, y: 0, width: 600, height: 60 } };

  function placeLines(
    boxWidth: number,
    boxHeight: number,
    fit: "contain" | "cover",
    inset: number,
  ) {
    const picture = videoContentRect(boxWidth, boxHeight, 1920, 1080, fit);
    return placePGSObjects({
      objects: [upperLine, lowerLine],
      plane: pgsPlaneRect(picture, 1920, 1080),
      planeWidth: 1920,
      planeHeight: 1080,
      boxWidth,
      boxHeight,
      bottomInsetPx: inset,
    });
  }

  it("lifts both lines of a subtitle together above the control bar", () => {
    const [first, second] = two(placeLines(1920, 1080, "contain", 140));
    expect(second.y + second.height).toBeCloseTo(1080 - 140);
    expect(second.y - first.y).toBeCloseTo(80);
    expect(first.x).toBe(560);
    expect(second.x).toBe(660);
  });

  it("shrinks both lines of a subtitle by one factor in a narrow Fill viewport", () => {
    const wide = { x: 210, y: 900 };
    const picture = videoContentRect(390, 700, 1920, 1080, "cover");
    const [first, second] = two(
      placePGSObjects({
        objects: [{ object: wide, source: { x: 0, y: 0, width: 1500, height: 60 } }, lowerLine],
        plane: pgsPlaneRect(picture, 1920, 1080),
        planeWidth: 1920,
        planeHeight: 1080,
        boxWidth: 390,
        boxHeight: 700,
        bottomInsetPx: 0,
      }),
    );
    expect(first.height).toBeCloseTo(second.height);
    expect(first.width / second.width).toBeCloseTo(1500 / 600);
    expect(first.x).toBeGreaterThanOrEqual(0);
    expect(first.x + first.width).toBeLessThanOrEqual(390);
    expect(second.y).toBeGreaterThan(first.y + first.height);
  });

  it("places an upper sign apart from the dialogue below it", () => {
    const picture = videoContentRect(1920, 1080, 1920, 1080, "contain");
    const [sign, dialogue] = two(
      placePGSObjects({
        objects: [
          { object: { x: 700, y: 60 }, source: { x: 0, y: 0, width: 500, height: 60 } },
          lowerLine,
        ],
        plane: pgsPlaneRect(picture, 1920, 1080),
        planeWidth: 1920,
        planeHeight: 1080,
        boxWidth: 1920,
        boxHeight: 1080,
        bottomInsetPx: 140,
      }),
    );
    expect(sign.y).toBe(60);
    expect(dialogue.y + dialogue.height).toBeCloseTo(1080 - 140);
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
