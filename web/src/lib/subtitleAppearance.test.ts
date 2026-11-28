import { describe, expect, it } from "vitest";
import { computeSubtitleFontScale } from "./subtitleAppearance";

describe("computeSubtitleFontScale", () => {
  it("gives every film the same subtitle size in a given window", () => {
    // 1600×600 is wider than a 2.40:1 film: the largest 16:9 frame that fits
    // is 600px tall, so 600 / 720.
    expect(computeSubtitleFontScale(1600, 600)).toBeCloseTo(600 / 720);
    // 900×900 is narrower than 16:9: the 16:9 frame is 900 × 9/16 tall.
    expect(computeSubtitleFontScale(900, 900)).toBeCloseTo(506.25 / 720);
    // Fill keeps following the visible window height.
    expect(computeSubtitleFontScale(900, 900, "cover")).toBeCloseTo(900 / 720);
  });
});
