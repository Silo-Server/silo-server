import { describe, expect, it } from "vitest";

import {
  computeSubtitleFontScale,
  computeSubtitleFontSize,
  computeSubtitlePositionStyle,
  computeSubtitleStyles,
  DEFAULT_SUBTITLE_APPEARANCE,
  SUBTITLE_REFERENCE_HEIGHT,
} from "./subtitleAppearance";

describe("computeSubtitleFontScale", () => {
  it("returns 1 at the 16:9 reference height", () => {
    expect(computeSubtitleFontScale(1280, SUBTITLE_REFERENCE_HEIGHT, 16 / 9)).toBe(1);
  });

  it("scales proportionally with the rendered video height", () => {
    // 16:9 video filling a 1920x1080 player renders at 1080 tall — 1.5x the
    // 720 reference.
    expect(computeSubtitleFontScale(1920, 1080, 16 / 9)).toBeCloseTo(1.5);
    // Same video in a half-size window scales down proportionally.
    expect(computeSubtitleFontScale(960, 540, 16 / 9)).toBeCloseTo(0.75);
  });

  it("tracks the letterboxed video, not the player, for narrow windows", () => {
    // A 16:9 video in a tall 1000x2000 player renders 1000 wide → 562.5 tall.
    expect(computeSubtitleFontScale(1000, 2000, 16 / 9)).toBeCloseTo(562.5 / 720);
  });

  it("uses the 16:9 reference frame for wider-than-16:9 content", () => {
    // 2.35:1 content filling a 1920-wide player: reference height stays
    // 1920 * 9/16 = 1080, matching how position offsets are anchored.
    expect(computeSubtitleFontScale(1920, 1080, 2.35)).toBeCloseTo(1.5);
  });

  it("falls back to 1 before measurements are available", () => {
    expect(computeSubtitleFontScale(0, 0, 16 / 9)).toBe(1);
    expect(computeSubtitleFontScale(1920, 1080, 0)).toBe(1);
    expect(computeSubtitleFontScale(1920, 1080, Number.NaN)).toBe(1);
  });
});

describe("computeSubtitlePositionStyle", () => {
  it("anchors Bottom to the player window", () => {
    // The video occupies only 562.5px in this tall player, but Bottom remains
    // 7% from the player window edge instead of moving up to the video frame.
    expect(computeSubtitlePositionStyle("bottom", 1000, 2000, 16 / 9)).toEqual({
      bottom: "140px",
    });
  });

  it("anchors Lower Third to the rendered 16:9 video frame", () => {
    // The centered 16:9 frame is 562.5px tall, with 718.75px below it. The
    // lower-third inset adds 12% of that frame height: 718.75 + 67.5 = 786.25.
    expect(computeSubtitlePositionStyle("lower-third", 1000, 2000, 16 / 9)).toEqual({
      bottom: "786.25px",
    });
  });
});

describe("computeSubtitleFontSize", () => {
  it("returns the base size at scale 1", () => {
    expect(computeSubtitleFontSize("large")).toBe("32px");
    expect(computeSubtitleFontSize("small", 1)).toBe("20px");
  });

  it("scales the base size", () => {
    expect(computeSubtitleFontSize("large", 1.5)).toBe("48px");
    expect(computeSubtitleFontSize("xxlarge", 0.5)).toBe("24px");
  });

  it("clamps to a legible minimum in tiny windows", () => {
    expect(computeSubtitleFontSize("small", 0.1)).toBe("12px");
  });
});

describe("computeSubtitleStyles", () => {
  it("applies the font scale to the cue font size", () => {
    const unscaled = computeSubtitleStyles(DEFAULT_SUBTITLE_APPEARANCE);
    const scaled = computeSubtitleStyles(DEFAULT_SUBTITLE_APPEARANCE, 2);
    expect(unscaled.cueStyle.fontSize).toBe("32px");
    expect(scaled.cueStyle.fontSize).toBe("64px");
  });
});
