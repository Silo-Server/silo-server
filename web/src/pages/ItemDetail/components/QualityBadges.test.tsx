import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { FileVersion } from "@/api/types";
import QualityBadges from "./QualityBadges";
import { resolveSelectedMediaSummary } from "./selectedMediaSummary";

function makeVersion(overrides: Partial<FileVersion> = {}): FileVersion {
  return {
    file_id: 1,
    resolution: "1080p",
    codec_video: "h264",
    codec_audio: "aac",
    hdr: false,
    container: "mkv",
    file_size: 0,
    duration: 0,
    bitrate: 0,
    ...overrides,
  };
}

describe("QualityBadges", () => {
  it("uses track details for 4K Dolby Vision with Dolby Digital Plus Atmos", () => {
    const version = makeVersion({
      resolution: "2160p",
      codec_video: "hevc",
      codec_audio: "eac3",
      hdr: true,
      video_tracks: [
        {
          codec: "hevc",
          width: 3840,
          height: 2160,
          dolby_vision: "Profile 8",
          dv_profile: 8,
          dv_bl_compat_id: 1,
          video_range: "DolbyVision",
          video_range_type: "DOVIWithHDR10",
        },
      ],
      audio_tracks: [
        {
          codec: "eac3",
          profile: "Dolby Digital Plus + Dolby Atmos",
          channels: 6,
          default: true,
        },
      ],
    });

    const markup = renderToStaticMarkup(
      <QualityBadges summary={resolveSelectedMediaSummary(version, undefined, 0)} />,
    );

    expect(markup).toContain(">4K<");
    expect(markup).toContain(">Dolby Vision<");
    expect(markup).toContain(">DD+ Atmos<");
    expect(markup).not.toContain(">2160p<");
    expect(markup).not.toContain(">HDR<");
    expect(markup).not.toContain(">EAC3<");
  });

  it("prefers a codec-specific Atmos label over a generic file-level label", () => {
    const version = makeVersion({
      codec_audio: "eac3 atmos",
      audio_tracks: [
        {
          codec: "eac3",
          profile: "Dolby Digital Plus + Dolby Atmos",
          default: true,
        },
      ],
    });

    const markup = renderToStaticMarkup(
      <QualityBadges summary={resolveSelectedMediaSummary(version, undefined, 0)} />,
    );

    expect(markup).toContain(">DD+ Atmos<");
    expect(markup).not.toContain(">Atmos<");
  });

  it("preserves ordinary HDR and EAC3 labels", () => {
    const version = makeVersion({
      resolution: "1080p",
      codec_audio: "eac3",
      hdr: true,
      video_tracks: [{ codec: "hevc", video_range_type: "HDR10" }],
      audio_tracks: [{ codec: "eac3", profile: "Dolby Digital Plus", default: true }],
    });

    const markup = renderToStaticMarkup(
      <QualityBadges summary={resolveSelectedMediaSummary(version, undefined, 0)} />,
    );

    expect(markup).toContain(">1080p<");
    expect(markup).toContain(">HDR10<");
    expect(markup).toContain(">EAC3<");
    expect(markup).not.toContain("Dolby Vision");
    expect(markup).not.toContain("Atmos");
  });

  it("detects Dolby Vision from the derived video range type", () => {
    const version = makeVersion({
      resolution: "2160p",
      hdr: false,
      video_tracks: [{ codec: "hevc", video_range_type: "DOVIWithHDR10" }],
    });

    const markup = renderToStaticMarkup(
      <QualityBadges summary={resolveSelectedMediaSummary(version, undefined, 0)} />,
    );

    expect(markup).toContain(">4K<");
    expect(markup).toContain(">Dolby Vision<");
    expect(markup).not.toContain(">HDR<");
  });
});
