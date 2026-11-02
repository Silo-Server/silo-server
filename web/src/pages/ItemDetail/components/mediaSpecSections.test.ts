// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { FileVersion, VersionVideoTrack } from "@/api/types";
import { makeFileVersion } from "../itemDetail.test-support";
import type { MediaSpecSection } from "./mediaSpecSections";
import {
  buildMediaSpecSections,
  buildSubtitleSections,
  buildVideoSections,
  formatChromaSubsampling,
  formatDolbyVisionLabel,
  formatVideoLevel,
  formatVideoRangeLabel,
} from "./mediaSpecSections";

const makeVersion = (overrides: Partial<FileVersion> = {}) =>
  makeFileVersion({
    resolution: "2160p",
    codec_video: "hevc",
    codec_audio: "truehd",
    hdr: true,
    ...overrides,
  });

function rowValue(rows: Array<{ label: string; value: string }>, label: string): string | null {
  return rows.find((row) => row.label === label)?.value ?? null;
}

function sectionAt(sections: MediaSpecSection[], index: number): MediaSpecSection {
  const section = sections[index];
  if (!section) throw new Error(`expected a section at index ${index}`);
  return section;
}

describe("formatChromaSubsampling", () => {
  it("maps common yuv pixel formats", () => {
    expect(formatChromaSubsampling("yuv420p")).toBe("4:2:0");
    expect(formatChromaSubsampling("yuv420p10le")).toBe("4:2:0");
    expect(formatChromaSubsampling("yuv422p")).toBe("4:2:2");
    expect(formatChromaSubsampling("yuv444p10le")).toBe("4:4:4");
    expect(formatChromaSubsampling("yuv411p")).toBe("4:1:1");
  });

  it("maps hardware-style formats to 4:2:0", () => {
    expect(formatChromaSubsampling("nv12")).toBe("4:2:0");
    expect(formatChromaSubsampling("p010le")).toBe("4:2:0");
  });

  it("returns empty string for unknown or missing formats", () => {
    expect(formatChromaSubsampling("gray")).toBe("");
    expect(formatChromaSubsampling("")).toBe("");
    expect(formatChromaSubsampling(undefined)).toBe("");
  });
});

describe("formatDolbyVisionLabel", () => {
  it("falls back to the numeric profile", () => {
    expect(formatDolbyVisionLabel({ dv_profile: 8 })).toBe("Dolby Vision Profile 8");
  });

  it("appends base-layer compatibility and enhancement layer details", () => {
    expect(formatDolbyVisionLabel({ dolby_vision: "Profile 8.1", dv_bl_compat_id: 1 })).toBe(
      "Dolby Vision Profile 8.1 (HDR10 compatible)",
    );
    expect(
      formatDolbyVisionLabel({
        dolby_vision: "Profile 7.6",
        dv_bl_compat_id: 6,
        dv_el_present: true,
      }),
    ).toBe("Dolby Vision Profile 7.6 (HDR10 compatible, EL)");
    expect(formatDolbyVisionLabel({ dolby_vision: "Profile 8.2", dv_bl_compat_id: 2 })).toBe(
      "Dolby Vision Profile 8.2 (SDR compatible)",
    );
  });

  it("falls back to video_range_type for compatibility when DV side-data fields are missing", () => {
    expect(
      formatDolbyVisionLabel({ dolby_vision: "Profile 8", video_range_type: "DOVIWithHDR10" }),
    ).toBe("Dolby Vision Profile 8 (HDR10 compatible)");
    expect(
      formatDolbyVisionLabel({ dolby_vision: "Profile 8", video_range_type: "DOVIWithHLG" }),
    ).toBe("Dolby Vision Profile 8 (HLG compatible)");
    expect(formatDolbyVisionLabel({ dolby_vision: "Profile 5", video_range_type: "DOVI" })).toBe(
      "Dolby Vision Profile 5",
    );
    expect(
      formatVideoRangeLabel({
        dolby_vision: "Profile 8",
        video_range_type: "DOVIWithHDR10Plus",
        hdr10_plus: true,
      }),
    ).toBe("Dolby Vision Profile 8 (HDR10 compatible) · HDR10+");
  });

  it("prefers explicit DV side-data fields over the video_range_type fallback", () => {
    expect(
      formatDolbyVisionLabel({
        dolby_vision: "Profile 8.2",
        dv_bl_compat_id: 2,
        video_range_type: "DOVIWithHDR10",
      }),
    ).toBe("Dolby Vision Profile 8.2 (SDR compatible)");
  });
});

describe("formatVideoRangeLabel", () => {
  it("combines Dolby Vision with HDR10+", () => {
    const track: VersionVideoTrack = {
      dolby_vision: "Profile 8.1",
      dv_bl_compat_id: 1,
      hdr10_plus: true,
    };
    expect(formatVideoRangeLabel(track)).toBe(
      "Dolby Vision Profile 8.1 (HDR10 compatible) · HDR10+",
    );
  });

  it("reports plain HDR10+ without Dolby Vision", () => {
    expect(formatVideoRangeLabel({ hdr10_plus: true })).toBe("HDR10+");
    expect(formatVideoRangeLabel({ hdr10_plus: true, video_range_type: "HDR10Plus" })).toBe(
      "HDR10+",
    );
  });

  it("keeps the DV hint from video_range_type when hdr10_plus is set without raw DV fields", () => {
    expect(formatVideoRangeLabel({ hdr10_plus: true, video_range_type: "DOVIWithHDR10Plus" })).toBe(
      "Dolby Vision · HDR10+",
    );
    expect(formatVideoRangeLabel({ hdr10_plus: true, video_range_type: "HDR10" })).toBe(
      "HDR10 · HDR10+",
    );
  });

  it("maps video_range_type enum values to friendly labels", () => {
    expect(formatVideoRangeLabel({ video_range_type: "HDR10" })).toBe("HDR10");
    expect(formatVideoRangeLabel({ video_range_type: "DOVIWithHDR10" })).toBe(
      "Dolby Vision (HDR10 compatible)",
    );
    expect(formatVideoRangeLabel({ video_range_type: "HLG" })).toBe("HLG");
  });

  it("passes through unknown range types and falls back to video_range", () => {
    expect(formatVideoRangeLabel({ video_range_type: "FancyNewRange" })).toBe("FancyNewRange");
    expect(formatVideoRangeLabel({ video_range: "SDR" })).toBe("SDR");
    expect(formatVideoRangeLabel({})).toBe("");
  });
});

describe("formatVideoLevel", () => {
  it("scales H.264 levels by 10", () => {
    expect(formatVideoLevel("h264", 41)).toBe("4.1");
    expect(formatVideoLevel("avc", 40)).toBe("4");
  });

  it("scales HEVC levels by 30", () => {
    expect(formatVideoLevel("hevc", 153)).toBe("5.1");
    expect(formatVideoLevel("hevc", 120)).toBe("4");
  });

  it("decodes AV1 level indexes", () => {
    expect(formatVideoLevel("av1", 13)).toBe("5.1");
    expect(formatVideoLevel("av1", 8)).toBe("4.0");
  });

  it("returns raw values for unknown codecs and empty for missing levels", () => {
    expect(formatVideoLevel("vp9", 31)).toBe("31");
    expect(formatVideoLevel("hevc", 0)).toBe("");
    expect(formatVideoLevel(undefined, undefined)).toBe("");
  });
});

describe("buildVideoSections", () => {
  it("omits empty fields for sparse SDR tracks and numbers multiple tracks", () => {
    const sections = buildVideoSections(
      makeVersion({
        video_tracks: [
          { codec: "h264", width: 1920, height: 1080, interlaced: true },
          { codec: "mjpeg" },
        ],
      }),
    );
    expect(sections).toHaveLength(2);
    const first = sectionAt(sections, 0);
    expect(first.title).toBe("Video 1");
    expect(sectionAt(sections, 1).title).toBe("Video 2");
    expect(rowValue(first.rows, "Scan")).toBe("Interlaced");
    expect(rowValue(first.rows, "Dynamic Range")).toBeNull();
    expect(rowValue(first.rows, "Chroma Subsampling")).toBeNull();
    expect(rowValue(first.rows, "Bitrate")).toBeNull();
  });
});

describe("buildSubtitleSections", () => {
  it("distinguishes embedded and external tracks", () => {
    const sections = buildSubtitleSections(
      makeVersion({
        subtitle_tracks: [
          { language: "eng", codec: "subrip", forced: true, default: false },
          {
            language: "spa",
            codec: "srt",
            external: true,
            file_name: "Example.spa.srt",
            hearing_impaired: true,
          },
        ],
      }),
    );

    const embedded = sectionAt(sections, 0);
    const external = sectionAt(sections, 1);
    expect(rowValue(embedded.rows, "Source")).toBe("Embedded");
    expect(rowValue(embedded.rows, "Forced")).toBe("Yes");
    expect(rowValue(embedded.rows, "File")).toBeNull();
    expect(rowValue(external.rows, "Source")).toBe("External");
    expect(rowValue(external.rows, "File")).toBe("Example.spa.srt");
    expect(rowValue(external.rows, "Hearing Impaired")).toBe("Yes");
  });
});

describe("buildMediaSpecSections", () => {
  it("orders sections General, Video, Audio, Subtitle and drops empty groups", () => {
    const sections = buildMediaSpecSections(
      makeVersion({
        container: "mkv",
        file_size: 1024 ** 3,
        video_tracks: [{ codec: "hevc" }],
        audio_tracks: [{ codec: "aac", channels: 2 }],
      }),
    );
    expect(sections.map((section) => section.title)).toEqual(["General", "Video", "Audio"]);
  });
});
