import { describe, expect, it } from "vitest";
import { isASSCodec, isBitmapCodec, isPGSCodec, isSubtitleFormatLabel } from "./subtitleCodecs";

describe("isPGSCodec", () => {
  it("matches PGS codec names case-insensitively", () => {
    expect(isPGSCodec("pgs")).toBe(true);
    expect(isPGSCodec("hdmv_pgs_subtitle")).toBe(true);
    expect(isPGSCodec("HDMV_PGS_SUBTITLE")).toBe(true);
  });

  it("rejects non-PGS codecs", () => {
    expect(isPGSCodec("dvd_subtitle")).toBe(false);
    expect(isPGSCodec("ass")).toBe(false);
    expect(isPGSCodec("subrip")).toBe(false);
    expect(isPGSCodec(undefined)).toBe(false);
  });
});

describe("isBitmapCodec", () => {
  it("covers all image-based codecs", () => {
    expect(isBitmapCodec("pgs")).toBe(true);
    expect(isBitmapCodec("hdmv_pgs_subtitle")).toBe(true);
    expect(isBitmapCodec("dvd_subtitle")).toBe(true);
    expect(isBitmapCodec("dvb_subtitle")).toBe(true);
  });

  it("rejects text codecs", () => {
    expect(isBitmapCodec("srt")).toBe(false);
    expect(isBitmapCodec("ass")).toBe(false);
    expect(isBitmapCodec(undefined)).toBe(false);
  });
});

describe("isASSCodec", () => {
  it("matches ASS/SSA only", () => {
    expect(isASSCodec("ass")).toBe(true);
    expect(isASSCodec("ssa")).toBe(true);
    expect(isASSCodec("pgs")).toBe(false);
    expect(isASSCodec(undefined)).toBe(false);
  });
});

describe("isSubtitleFormatLabel", () => {
  it("recognizes raw codec names and friendly aliases", () => {
    expect(isSubtitleFormatLabel("SUBRIP", "subrip")).toBe(true);
    expect(isSubtitleFormatLabel("SRT", "subrip")).toBe(true);
    expect(isSubtitleFormatLabel("PGS", "hdmv_pgs_subtitle")).toBe(true);
  });

  it("preserves meaningful track titles", () => {
    expect(isSubtitleFormatLabel("Forced", "subrip")).toBe(false);
    expect(isSubtitleFormatLabel("Latin American", "subrip")).toBe(false);
  });
});
