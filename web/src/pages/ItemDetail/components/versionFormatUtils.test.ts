import { describe, expect, it } from "vitest";
import { formatPageCount, formatLanguageName, extractSourceHint } from "./versionFormatUtils";

describe("formatPageCount", () => {
  it("formats singular and plural ebook page counts", () => {
    expect(formatPageCount(1)).toBe("1 page");
    expect(formatPageCount(321)).toBe("321 pages");
  });

  it("returns empty string for missing page counts", () => {
    expect(formatPageCount(0)).toBe("");
    expect(formatPageCount(undefined)).toBe("");
  });
});

describe("formatLanguageName", () => {
  it("resolves 3-letter code eng to English", () => {
    expect(formatLanguageName("eng")).toBe("English");
  });

  it("resolves 3-letter code cze to Czech", () => {
    expect(formatLanguageName("cze")).toBe("Czech");
  });

  it("title-cases long names", () => {
    expect(formatLanguageName("english subtitles")).toBe("English Subtitles");
  });

  it("returns empty string for undefined", () => {
    expect(formatLanguageName(undefined)).toBe("");
  });

  it("returns empty string for empty string", () => {
    expect(formatLanguageName("")).toBe("");
  });
});

describe("extractSourceHint", () => {
  it("extracts Remux from filename", () => {
    expect(extractSourceHint("Movie.2160p.Remux.mkv")).toBe("Remux");
  });

  it("extracts WEB-DL from filename", () => {
    expect(extractSourceHint("Movie.1080p.WEB-DL.x264.mkv")).toBe("WEB-DL");
  });

  it("extracts WEBRip from filename", () => {
    expect(extractSourceHint("Movie.720p.WEBRip.x264.mkv")).toBe("WEBRip");
  });

  it("extracts BluRay from filename", () => {
    expect(extractSourceHint("Movie.2160p.BluRay.HEVC.mkv")).toBe("BluRay");
  });

  it("extracts BDRip from filename", () => {
    expect(extractSourceHint("Movie.1080p.BDRip.x264.mkv")).toBe("BDRip");
  });

  it("extracts HDTV from filename", () => {
    expect(extractSourceHint("Movie.1080p.HDTV.mkv")).toBe("HDTV");
  });

  it("extracts DVDRip from filename", () => {
    expect(extractSourceHint("Movie.DVDRip.XviD.avi")).toBe("DVDRip");
  });

  it("is case-insensitive", () => {
    expect(extractSourceHint("Movie.2160p.remux.mkv")).toBe("Remux");
    expect(extractSourceHint("Movie.1080p.web-dl.mkv")).toBe("WEB-DL");
    expect(extractSourceHint("Movie.720p.bluray.mkv")).toBe("BluRay");
  });

  it("returns null when no source hint matches", () => {
    expect(extractSourceHint("Movie.2160p.mkv")).toBeNull();
    expect(extractSourceHint("random-file-name.mp4")).toBeNull();
  });
});
