import { describe, expect, it } from "vitest";
import type { FileVersion } from "@/api/types";
import { buildQualitySummary, buildDetailLine, sortByResolution } from "./VersionFlyout";

function makeVersion(overrides: Partial<FileVersion> = {}): FileVersion {
  return {
    file_id: overrides.file_id ?? 1,
    resolution: overrides.resolution ?? "1080p",
    codec_video: overrides.codec_video ?? "h264",
    codec_audio: overrides.codec_audio ?? "aac",
    hdr: overrides.hdr ?? false,
    container: overrides.container ?? "mkv",
    file_size: overrides.file_size ?? 0,
    duration: overrides.duration ?? 0,
    bitrate: overrides.bitrate ?? 0,
    file_name: overrides.file_name,
    file_path: overrides.file_path,
    audio_tracks: overrides.audio_tracks,
    video_tracks: overrides.video_tracks,
    subtitle_tracks: overrides.subtitle_tracks,
  };
}

describe("buildQualitySummary", () => {
  it("joins resolution, codec, HDR, and audio", () => {
    const version = makeVersion({
      resolution: "2160p",
      codec_video: "hevc",
      hdr: true,
      codec_audio: "truehd",
    });
    expect(buildQualitySummary(version)).toBe("2160p · HEVC · HDR · TrueHD");
  });

  it("omits HDR segment when hdr is false", () => {
    const version = makeVersion({
      resolution: "1080p",
      codec_video: "h264",
      hdr: false,
      codec_audio: "aac",
    });
    const result = buildQualitySummary(version);
    expect(result).not.toContain("HDR");
    expect(result).toBe("1080p · H264 · AAC");
  });

  it("omits empty segments", () => {
    const version = makeVersion({
      resolution: "1080p",
      codec_video: "",
      hdr: false,
      codec_audio: "",
    });
    expect(buildQualitySummary(version)).toBe("1080p");
  });

  it("includes resolution even when other fields are empty", () => {
    const version = makeVersion({
      resolution: "720p",
      codec_video: "",
      hdr: false,
      codec_audio: "",
    });
    expect(buildQualitySummary(version)).toBe("720p");
  });

  it("maps audio codec label using mapAudioLabel", () => {
    const version = makeVersion({
      resolution: "2160p",
      codec_video: "hevc",
      hdr: true,
      codec_audio: "TrueHD Atmos",
    });
    expect(buildQualitySummary(version)).toBe("2160p · HEVC · HDR · Atmos");
  });

  it("falls back to container for ebook-style files without video quality", () => {
    const version = makeVersion({
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
    const version = makeVersion({
      file_size: 45 * 1024 ** 3,
      file_name: "Movie.2160p.Remux.mkv",
    });
    expect(buildDetailLine(version)).toBe("45.0 GB · Remux");
  });

  it("shows only file size when no source hint matches", () => {
    const version = makeVersion({
      file_size: 10 * 1024 ** 3,
      file_name: "movie.mkv",
    });
    expect(buildDetailLine(version)).toBe("10.0 GB");
  });

  it("returns empty string when file_size is zero and no name", () => {
    const version = makeVersion({ file_size: 0, file_name: undefined });
    expect(buildDetailLine(version)).toBe("");
  });

  it("shows source hint only when file_size is zero but name matches", () => {
    const version = makeVersion({ file_size: 0, file_name: "Movie.WEB-DL.mkv" });
    expect(buildDetailLine(version)).toBe("WEB-DL");
  });
});

describe("sortByResolution", () => {
  it("sorts versions descending by resolution score", () => {
    const versions = [
      makeVersion({ file_id: 1, resolution: "720p" }),
      makeVersion({ file_id: 2, resolution: "2160p" }),
      makeVersion({ file_id: 3, resolution: "1080p" }),
    ];
    const sorted = sortByResolution(versions);
    expect(sorted.map((v) => v.resolution)).toEqual(["2160p", "1080p", "720p"]);
  });

  it("does not mutate the original array", () => {
    const versions = [
      makeVersion({ file_id: 1, resolution: "720p" }),
      makeVersion({ file_id: 2, resolution: "2160p" }),
    ];
    const original = [...versions];
    sortByResolution(versions);
    expect(versions[0]!.resolution).toBe(original[0]!.resolution);
    expect(versions[1]!.resolution).toBe(original[1]!.resolution);
  });

  it("keeps equal resolutions in original order (stable)", () => {
    const versions = [
      makeVersion({ file_id: 1, resolution: "1080p" }),
      makeVersion({ file_id: 2, resolution: "1080p" }),
    ];
    const sorted = sortByResolution(versions);
    expect(sorted.map((v) => v.file_id)).toEqual([1, 2]);
  });

  it("handles empty array", () => {
    expect(sortByResolution([])).toEqual([]);
  });

  it("handles single element", () => {
    const versions = [makeVersion({ file_id: 1, resolution: "1080p" })];
    expect(sortByResolution(versions)).toHaveLength(1);
  });

  it("places unknown resolutions at the end", () => {
    const versions = [
      makeVersion({ file_id: 1, resolution: "unknown" }),
      makeVersion({ file_id: 2, resolution: "1080p" }),
    ];
    const sorted = sortByResolution(versions);
    expect(sorted[0]!.resolution).toBe("1080p");
    expect(sorted[1]!.resolution).toBe("unknown");
  });
});
