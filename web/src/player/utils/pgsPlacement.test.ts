import { describe, expect, it } from "vitest";
import {
  computePgsPlacements,
  computeVideoRect,
  detectCueRegions,
  type Rect,
} from "./pgsPlacement";
import { DEFAULT_SUBTITLE_APPEARANCE, type SubtitleAppearance } from "../../lib/subtitleAppearance";

// ─── helpers ─────────────────────────────────────────────────────────────────

/** Builds an RGBA buffer with opaque pixels inside the given rects. */
function frameWithInk(width: number, height: number, rects: Rect[]): Uint8ClampedArray {
  const data = new Uint8ClampedArray(width * height * 4);
  for (const r of rects) {
    for (let y = r.y; y < r.y + r.height; y++) {
      for (let x = r.x; x < r.x + r.width; x++) {
        data[(y * width + x) * 4 + 3] = 255;
      }
    }
  }
  return data;
}

function appearance(overrides: Partial<SubtitleAppearance>): SubtitleAppearance {
  return { ...DEFAULT_SUBTITLE_APPEARANCE, ...overrides };
}

const VIDEO_RECT: Rect = { x: 0, y: 0, width: 1920, height: 1080 };

// ─── region detection ────────────────────────────────────────────────────────

describe("detectCueRegions", () => {
  it("finds a single bottom cue with tight bounds", () => {
    const data = frameWithInk(480, 270, [{ x: 100, y: 230, width: 280, height: 20 }]);
    expect(detectCueRegions(data, 480, 270)).toEqual([
      { x: 100, y: 230, width: 280, height: 20, lineHeight: 20 },
    ]);
  });

  it("merges the lines of a two-line cue but keeps a distant sign separate", () => {
    // Two dialogue lines 6px apart (< merge gap) plus a sign near the top.
    const data = frameWithInk(480, 270, [
      { x: 60, y: 30, width: 80, height: 12 }, // floating sign
      { x: 120, y: 220, width: 240, height: 14 }, // line 1
      { x: 140, y: 240, width: 200, height: 14 }, // line 2 (gap of 6)
    ]);
    const regions = detectCueRegions(data, 480, 270);
    expect(regions).toHaveLength(2);
    expect(regions[0]).toEqual({ x: 60, y: 30, width: 80, height: 12, lineHeight: 12 });
    // Merged dialogue band spans both lines' bounds; line height is one line.
    expect(regions[1]).toEqual({ x: 120, y: 220, width: 240, height: 34, lineHeight: 14 });
  });

  it("returns nothing for a fully transparent frame", () => {
    const data = new Uint8ClampedArray(480 * 270 * 4);
    expect(detectCueRegions(data, 480, 270)).toEqual([]);
  });
});

// ─── placement ───────────────────────────────────────────────────────────────

describe("computePgsPlacements", () => {
  // A typical authored dialogue cue near the bottom of a 1920×1080 frame.
  const dialogue: Rect = { x: 660, y: 950, width: 600, height: 80 };

  it("scales a single-line cue to the target text line height", () => {
    const [p] = computePgsPlacements([dialogue], 1920, 1080, VIDEO_RECT, appearance({}), 44);
    // lineHeight defaults to the region height: the cue renders one text
    // line tall regardless of its authored size.
    expect(p!.dest.height).toBeCloseTo(44);
    expect(p!.dest.width).toBeCloseTo(600 * (44 / 80));
  });

  it("scales a multi-line cue by its per-line height", () => {
    // Two 30px lines with a 20px gap: 80px region, 30px line height.
    const twoLine = { x: 660, y: 950, width: 600, height: 80, lineHeight: 30 };
    const [p] = computePgsPlacements([twoLine], 1920, 1080, VIDEO_RECT, appearance({}), 44);
    expect(p!.dest.height).toBeCloseTo(80 * (44 / 30));
  });

  it("caps upscaling of tiny authored cues", () => {
    const tiny = { x: 900, y: 1000, width: 120, height: 10 };
    const [p] = computePgsPlacements([tiny], 1920, 1080, VIDEO_RECT, appearance({}), 44);
    // 44/10 = 4.4 would be mush; capped at 2.5.
    expect(p!.dest.height).toBeCloseTo(25);
  });

  it("scales bottom cues upward from their bottom edge", () => {
    // Use lower-third to skip the bottom re-margin and isolate anchoring.
    const [p] = computePgsPlacements(
      [{ x: 660, y: 500, width: 600, height: 80 }],
      1920,
      1080,
      VIDEO_RECT,
      appearance({ fontSize: "xxlarge", position: "lower-third" }),
      44,
    );
    // midY (540/1080 = 0.5) is bottom-region; maxY (580/1080 ≈ 0.54) is
    // outside the dialogue band, so no preset repositioning applies.
    expect(p!.dest.y + p!.dest.height).toBeCloseTo(580); // bottom edge fixed
    expect(p!.dest.height).toBeCloseTo(44);
    // X stays centered.
    expect(p!.dest.x + p!.dest.width / 2).toBeCloseTo(960);
  });

  it("re-margins dialogue-band cues to the text overlay's bottom anchor", () => {
    const [p] = computePgsPlacements([dialogue], 1920, 1080, VIDEO_RECT, appearance({}), 44);
    // 16:9 content: reference frame equals the video rect, bottom offset 7%.
    expect(p!.dest.y + p!.dest.height).toBeCloseTo(1080 - 1080 * 0.07);
  });

  it("moves dialogue to the lower third and top presets", () => {
    const [lower] = computePgsPlacements(
      [dialogue],
      1920,
      1080,
      VIDEO_RECT,
      appearance({ position: "lower-third" }),
      44,
    );
    expect(lower!.dest.y + lower!.dest.height).toBeCloseTo(1080 - 1080 * 0.18);

    const [top] = computePgsPlacements(
      [dialogue],
      1920,
      1080,
      VIDEO_RECT,
      appearance({ position: "top" }),
      44,
    );
    expect(top!.dest.y).toBeCloseTo(1080 * 0.07);
  });

  it("leaves floating signs at their authored placement", () => {
    const sign: Rect = { x: 200, y: 100, width: 300, height: 60 };
    const [p] = computePgsPlacements(
      [sign],
      1920,
      1080,
      VIDEO_RECT,
      appearance({ position: "top" }),
      44,
    );
    // Top-region cue: grows downward from its authored top edge and is not
    // repositioned by the preset.
    expect(p!.dest.y).toBeCloseTo(100);
  });

  it("shifts a multi-cue dialogue group by one shared offset", () => {
    const line1: Rect = { x: 700, y: 900, width: 500, height: 40 };
    const line2: Rect = { x: 700, y: 980, width: 500, height: 40 };
    const [p1, p2] = computePgsPlacements(
      [line1, line2],
      1920,
      1080,
      VIDEO_RECT,
      appearance({}),
      44,
    );
    // Group bottom (1020) moves to the 7% anchor line; both cues shift
    // equally, preserving authored spacing between bottom-anchored edges.
    const shift = 1080 - 1080 * 0.07 - 1020;
    expect(p1!.dest.y + p1!.dest.height).toBeCloseTo(940 + shift);
    expect(p2!.dest.y + p2!.dest.height).toBeCloseTo(1020 + shift);
  });

  it("adds a padded background box only for the box style", () => {
    const [boxed] = computePgsPlacements(
      [dialogue],
      1920,
      1080,
      VIDEO_RECT,
      appearance({ backgroundStyle: "box" }),
      44,
    );
    expect(boxed!.background).not.toBeNull();
    expect(boxed!.background!.rect.width).toBeGreaterThan(boxed!.dest.width);
    expect(boxed!.background!.rect.height).toBeGreaterThan(boxed!.dest.height);

    const [plain] = computePgsPlacements([dialogue], 1920, 1080, VIDEO_RECT, appearance({}), 44);
    expect(plain!.background).toBeNull();
  });

  it("anchors wide-content dialogue into the letterbox like the text overlay", () => {
    // 2.35:1 video letterboxed in a 16:9 canvas: the reference frame extends
    // below the video box, so the bottom anchor lands in the letterbox.
    const videoRect = computeVideoRect(1920, 1080, 2.35);
    const srcH = Math.round(1920 / 2.35);
    const cue: Rect = { x: 660, y: srcH - 130, width: 600, height: 80 };
    const [p] = computePgsPlacements([cue], 1920, srcH, videoRect, appearance({}), 44);
    const refBottom = 1080 / 2 + (1920 * (9 / 16)) / 2;
    expect(p!.dest.y + p!.dest.height).toBeCloseTo(refBottom - 1920 * (9 / 16) * 0.07);
    // Below the video box — inside the letterbox band.
    expect(p!.dest.y + p!.dest.height).toBeGreaterThan(videoRect.y + videoRect.height);
  });

  it("clamps scaled cues into the video rect", () => {
    // A full-width cue with a short line height would upscale past the
    // video box; the scale caps at the box width instead.
    const wide: Rect = { x: 0, y: 1000, width: 1920, height: 30 };
    const [p] = computePgsPlacements([wide], 1920, 1080, VIDEO_RECT, appearance({}), 60);
    expect(p!.dest.x).toBeGreaterThanOrEqual(0);
    expect(p!.dest.width).toBeLessThanOrEqual(1920);
  });
});

// ─── video rect ──────────────────────────────────────────────────────────────

describe("computeVideoRect", () => {
  it("letterboxes wider-than-canvas video", () => {
    // 2.35:1 video in a 16:9 canvas → bars above and below.
    const rect = computeVideoRect(1920, 1080, 2.35);
    expect(rect.x).toBe(0);
    expect(rect.width).toBe(1920);
    expect(rect.height).toBeCloseTo(1920 / 2.35);
    expect(rect.y).toBeCloseTo((1080 - 1920 / 2.35) / 2);
  });

  it("pillarboxes narrower video", () => {
    // 4:3 video in a 16:9 canvas → bars left and right.
    const rect = computeVideoRect(1920, 1080, 4 / 3);
    expect(rect.y).toBe(0);
    expect(rect.height).toBe(1080);
    expect(rect.width).toBeCloseTo(1080 * (4 / 3));
  });

  it("falls back to the full canvas for invalid aspect", () => {
    expect(computeVideoRect(1920, 1080, NaN)).toEqual({
      x: 0,
      y: 0,
      width: 1920,
      height: 1080,
    });
  });
});
