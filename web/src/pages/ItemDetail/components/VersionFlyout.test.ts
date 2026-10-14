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
