import { describe, expect, it } from "vitest";
import { makeFileVersion } from "../itemDetail.test-support";
import { buildQualitySummary, buildDetailLine, sortByResolution } from "./VersionFlyout";
import { versionFileLabel } from "./versionFormatUtils";

describe("buildQualitySummary", () => {
  it("joins resolution, codec, HDR, and audio", () => {
    const version = makeFileVersion({
      resolution: "2160p",
      codec_video: "hevc",
      hdr: true,
      codec_audio: "truehd",
    });
    expect(buildQualitySummary(version)).toBe("2160p · HEVC · HDR · TrueHD");
  });

  it("falls back to container for ebook-style files without video quality", () => {
    const version = makeFileVersion({
      resolution: "",
      codec_video: "",
      codec_audio: "",
      hdr: false,
      container: "epub",
      file_name: "A Psalm for the Wild-Built.epub",
    });

    expect(buildQualitySummary(version)).toBe("EPUB");
  });
});

describe("buildDetailLine", () => {
  it("shows file size and source hint when present", () => {
    const version = makeFileVersion({
      file_size: 45 * 1024 ** 3,
      file_name: "Movie.2160p.Remux.mkv",
    });
    expect(buildDetailLine(version)).toBe("45.0 GB · Remux");
  });
});

describe("versionFileLabel", () => {
  it.each([
    [
      "drops the show and episode prefix",
      "My TVShow (2026) - S01E01 - Episode name 1st version [1080p].mkv",
      "Episode name 1st version [1080p]",
    ],
    ["handles a multi-episode token", "Show.S02E03-E04.Two.Parter.WEB-DL.mkv", "Two.Parter.WEB-DL"],
    ["handles an underscore-separated token", "Show_S01E01_Pilot_1080p.mkv", "Pilot_1080p"],
    ["handles a lowercase dotted token", "show.s01e01e02.720p.hdtv.mkv", "720p.hdtv"],
    [
      "keeps a movie name whole",
      "Movie (2024) [Bluray-2160p Remux].mkv",
      "Movie (2024) [Bluray-2160p Remux]",
    ],
    ["keeps the name when nothing follows the token", "Show - S01E01.mkv", "Show - S01E01"],
    ["returns nothing for an empty name", "  ", ""],
    ["returns nothing for a missing name", undefined, ""],
  ])("%s", (_case, fileName, expected) => {
    expect(versionFileLabel(fileName)).toBe(expected);
  });
});

describe("sortByResolution", () => {
  it("sorts versions descending by resolution score", () => {
    const versions = [
      makeFileVersion({ file_id: 1, resolution: "720p" }),
      makeFileVersion({ file_id: 2, resolution: "2160p" }),
      makeFileVersion({ file_id: 3, resolution: "1080p" }),
    ];
    const sorted = sortByResolution(versions);
    expect(sorted.map((v) => v.resolution)).toEqual(["2160p", "1080p", "720p"]);
  });

  it("does not mutate the original array", () => {
    const versions = [
      makeFileVersion({ file_id: 1, resolution: "720p" }),
      makeFileVersion({ file_id: 2, resolution: "2160p" }),
    ];
    const original = [...versions];
    sortByResolution(versions);
    expect(versions[0]!.resolution).toBe(original[0]!.resolution);
    expect(versions[1]!.resolution).toBe(original[1]!.resolution);
  });

  it("places unknown resolutions at the end", () => {
    const versions = [
      makeFileVersion({ file_id: 1, resolution: "unknown" }),
      makeFileVersion({ file_id: 2, resolution: "1080p" }),
    ];
    const sorted = sortByResolution(versions);
    expect(sorted[0]!.resolution).toBe("1080p");
    expect(sorted[1]!.resolution).toBe("unknown");
  });
});
