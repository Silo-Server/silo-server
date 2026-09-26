import { describe, expect, it } from "vitest";
import {
  applyASSMarginInset,
  computeCoverCrop,
  coverZoom,
  NO_ASS_MARGIN_INSET,
  resolveASSMarginInset,
} from "./assFillMargins";

const STYLE_FORMAT =
  "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding";
const EVENT_FORMAT =
  "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text";

function script(playRes: string[], styles: string[], events: string[], eol = "\n"): string {
  return [
    "[Script Info]",
    ...playRes,
    "",
    "[V4+ Styles]",
    STYLE_FORMAT,
    ...styles,
    "",
    "[Events]",
    EVENT_FORMAT,
    ...events,
  ].join(eol);
}

const DEFAULT_STYLE =
  "Style: Default,Arial,64,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,3,0,2,40,30,20,1";

describe("computeCoverCrop", () => {
  it("crops top and bottom when the player is wider than the video", () => {
    const crop = computeCoverCrop(3440, 1440, 16 / 9);
    expect(crop.x).toBe(0);
    // 3440 / (16/9) = 1935 rendered rows, 1440 visible.
    expect(crop.y).toBeCloseTo((1935 - 1440) / 2 / 1935);
  });

  it("crops left and right when the player is narrower than the video", () => {
    const crop = computeCoverCrop(1920, 1080, 2.4);
    expect(crop.y).toBe(0);
    expect(crop.x).toBeCloseTo((1 - 16 / 9 / 2.4) / 2);
  });

  it("does not crop matching or unmeasured geometry", () => {
    expect(computeCoverCrop(1920, 1080, 16 / 9)).toEqual({ x: 0, y: 0 });
    expect(computeCoverCrop(0, 0, 16 / 9)).toEqual({ x: 0, y: 0 });
    expect(computeCoverCrop(1920, 1080, 0)).toEqual({ x: 0, y: 0 });
  });
});

describe("coverZoom", () => {
  it("returns how much larger the covered video renders than in Fit", () => {
    expect(coverZoom(computeCoverCrop(3440, 1440, 16 / 9))).toBeCloseTo(1935 / 1440);
    expect(coverZoom(computeCoverCrop(1920, 1080, 2.4))).toBeCloseTo(2.4 / (16 / 9));
    expect(coverZoom({ x: 0, y: 0 })).toBe(1);
  });
});

describe("resolveASSMarginInset", () => {
  it("converts the crop into PlayRes units", () => {
    const content = script(["PlayResX: 1920", "PlayResY: 1080"], [], []);
    expect(resolveASSMarginInset(content, { x: 0.05, y: 0.128 })).toEqual({
      horizontal: 96,
      vertical: 138,
    });
  });

  it("uses libass PlayRes fallbacks", () => {
    expect(resolveASSMarginInset(script([], [], []), { x: 0.5, y: 0.5 })).toEqual({
      horizontal: 192,
      vertical: 144,
    });
    expect(resolveASSMarginInset(script(["PlayResX: 1280"], [], []), { x: 0, y: 0.5 })).toEqual({
      horizontal: 0,
      vertical: 512,
    });
    expect(resolveASSMarginInset(script(["PlayResY: 720"], [], []), { x: 0.5, y: 0 })).toEqual({
      horizontal: 480,
      vertical: 0,
    });
  });

  it("returns no inset without a crop", () => {
    expect(resolveASSMarginInset(script([], [], []), { x: 0, y: 0 })).toBe(NO_ASS_MARGIN_INSET);
  });
});

describe("applyASSMarginInset", () => {
  it("grows style margins and explicit event margins only", () => {
    const content = script(
      ["PlayResX: 1920", "PlayResY: 1080"],
      [DEFAULT_STYLE],
      [
        "Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Inherits, the style",
        "Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0010,0,0055,,Own margins",
        "Comment: 0,0:00:01.00,0:00:02.00,Default,,5,5,5,,Not rendered",
      ],
    );

    const lines = applyASSMarginInset(content, { horizontal: 7, vertical: 100 }).split("\n");

    expect(lines).toContain(
      "Style: Default,Arial,64,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,3,0,2,47,37,120,1",
    );
    expect(lines).toContain(
      "Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Inherits, the style",
    );
    expect(lines).toContain("Dialogue: 0,0:00:01.00,0:00:02.00,Default,,17,0,155,,Own margins");
    expect(lines).toContain("Comment: 0,0:00:01.00,0:00:02.00,Default,,5,5,5,,Not rendered");
  });

  it("leaves positioned events and override tags untouched", () => {
    const sign =
      "Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,{\\an5\\pos(960,480)}Sign, here";
    const content = script(["PlayResY: 1080"], [DEFAULT_STYLE], [sign]);
    expect(applyASSMarginInset(content, { horizontal: 0, vertical: 50 })).toContain(sign);
  });

  it("follows a custom Format order and preserves CRLF line endings", () => {
    const content = [
      "[V4+ Styles]",
      "Format: Name, MarginV, Alignment",
      "Style: Top,30,8",
      "",
    ].join("\r\n");
    expect(applyASSMarginInset(content, { horizontal: 0, vertical: 12 })).toBe(
      ["[V4+ Styles]", "Format: Name, MarginV, Alignment", "Style: Top,42,8", ""].join("\r\n"),
    );
  });

  it("grows a margin declared as the last style column", () => {
    const content = ["[V4+ Styles]", "Format: Name, Alignment, MarginV", "Style: Low,2,40"].join(
      "\n",
    );
    expect(applyASSMarginInset(content, { horizontal: 0, vertical: 10 })).toBe(
      ["[V4+ Styles]", "Format: Name, Alignment, MarginV", "Style: Low,2,50"].join("\n"),
    );
  });

  it("uses libass default formats when a section omits Format", () => {
    const content = ["[V4+ Styles]", DEFAULT_STYLE].join("\n");
    expect(applyASSMarginInset(content, { horizontal: 0, vertical: 5 })).toContain(",40,30,25,1");
  });

  it("returns the content unchanged for a zero inset", () => {
    const content = script([], [DEFAULT_STYLE], []);
    expect(applyASSMarginInset(content, NO_ASS_MARGIN_INSET)).toBe(content);
  });
});
