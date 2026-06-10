import { describe, expect, it } from "vitest";
import {
  formatFileSize,
  formatPageCount,
  formatChannels,
  formatBitrate,
  formatLanguageName,
  extractSourceHint,
} from "./versionFormatUtils";

describe("formatFileSize", () => {
  it("formats bytes in GB", () => {
    expect(formatFileSize(2 * 1024 ** 3)).toBe("2.0 GB");
    expect(formatFileSize(1.5 * 1024 ** 3)).toBe("1.5 GB");
  });

  it("formats bytes in MB", () => {
    expect(formatFileSize(512 * 1024 ** 2)).toBe("512.0 MB");
    expect(formatFileSize(1.5 * 1024 ** 2)).toBe("1.5 MB");
  });

  it("returns empty string for zero", () => {
    expect(formatFileSize(0)).toBe("");
  });

  it("returns empty string for negative values", () => {
    expect(formatFileSize(-100)).toBe("");
  });
});

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

describe("formatChannels", () => {
  it("maps 8 channels to 7.1", () => {
    expect(formatChannels(8)).toBe("7.1");
  });

  it("maps 6 channels to 5.1", () => {
    expect(formatChannels(6)).toBe("5.1");
  });

  it("maps 2 channels to stereo", () => {
    expect(formatChannels(2)).toBe("stereo");
  });

  it("returns empty string for undefined", () => {
    expect(formatChannels(undefined)).toBe("");
  });

  it("returns empty string for zero", () => {
    expect(formatChannels(0)).toBe("");
  });

  it("formats other channel counts with ch suffix", () => {
    expect(formatChannels(4)).toBe("4 ch");
  });
});

describe("formatBitrate", () => {
  it("formats bitrate with kbps suffix", () => {
    expect(formatBitrate(1000)).toBe("1,000 kbps");
    expect(formatBitrate(55000)).toBe("55,000 kbps");
  });

  it("returns empty string for zero", () => {
    expect(formatBitrate(0)).toBe("");
  });

  it("returns empty string for undefined", () => {
    expect(formatBitrate(undefined)).toBe("");
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
