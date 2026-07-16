import { describe, expect, it } from "vitest";

import { bestVideoRangeLabel, videoRangeLabel, type VideoRangeSource } from "./videoRange";

describe("videoRangeLabel", () => {
  it("returns empty for SDR / unprobed", () => {
    expect(videoRangeLabel({ hdr: false })).toBe("");
    expect(videoRangeLabel({ hdr: false, video_tracks: [{}] })).toBe("");
  });

  it("falls back to generic HDR from the bare boolean", () => {
    expect(videoRangeLabel({ hdr: true })).toBe("HDR");
    expect(videoRangeLabel({ hdr: true, video_tracks: [{}] })).toBe("HDR");
  });

  it("detects Dolby Vision from the dolby_vision string", () => {
    expect(videoRangeLabel({ hdr: true, video_tracks: [{ dolby_vision: "Profile 5" }] })).toBe(
      "DV",
    );
  });

  it("detects Dolby Vision from dv_profile alone", () => {
    expect(videoRangeLabel({ hdr: true, video_tracks: [{ dv_profile: 8 }] })).toBe("DV");
  });

  it("detects Dolby Vision from a DOVI video_range_type", () => {
    expect(
      videoRangeLabel({ hdr: true, video_tracks: [{ video_range_type: "DOVIWithSDR" }] }),
    ).toBe("DV");
  });

  it("combines DV with HDR10 base-layer compatibility", () => {
    expect(
      videoRangeLabel({
        hdr: true,
        video_tracks: [{ dolby_vision: "Profile 8", video_range_type: "DOVIWithHDR10" }],
      }),
    ).toBe("DV HDR10");
    expect(
      videoRangeLabel({
        hdr: true,
        video_tracks: [{ dolby_vision: "Profile 7", color_transfer: "smpte2084" }],
      }),
    ).toBe("DV HDR10");
  });

  it("combines DV with HLG compatibility", () => {
    expect(
      videoRangeLabel({ hdr: true, video_tracks: [{ video_range_type: "DOVIWithHLG" }] }),
    ).toBe("DV HLG");
  });

  it("labels HDR10+ from the flag or range type", () => {
    expect(videoRangeLabel({ hdr: true, video_tracks: [{ hdr10_plus: true }] })).toBe("HDR10+");
    expect(videoRangeLabel({ hdr: true, video_tracks: [{ video_range_type: "HDR10Plus" }] })).toBe(
      "HDR10+",
    );
    expect(
      videoRangeLabel({
        hdr: true,
        video_tracks: [{ video_range_type: "DOVIWithELHDR10Plus" }],
      }),
    ).toBe("DV HDR10+");
  });

  it("labels HDR10 and HLG from video_range_type or color_transfer", () => {
    expect(videoRangeLabel({ hdr: true, video_tracks: [{ video_range_type: "HDR10" }] })).toBe(
      "HDR10",
    );
    expect(videoRangeLabel({ hdr: true, video_tracks: [{ color_transfer: "smpte2084" }] })).toBe(
      "HDR10",
    );
    expect(videoRangeLabel({ hdr: true, video_tracks: [{ video_range_type: "HLG" }] })).toBe("HLG");
    expect(videoRangeLabel({ hdr: true, video_tracks: [{ color_transfer: "arib-std-b67" }] })).toBe(
      "HLG",
    );
  });
});

describe("bestVideoRangeLabel", () => {
  it("prefers DV over a generic-HDR sibling version", () => {
    const versions: VideoRangeSource[] = [
      { hdr: true },
      {
        hdr: true,
        video_tracks: [{ dolby_vision: "Profile 8", video_range_type: "DOVIWithHDR10" }],
      },
    ];
    expect(bestVideoRangeLabel(versions)).toBe("DV HDR10");
  });

  it("prefers explicit HDR10 over the bare boolean", () => {
    const versions: VideoRangeSource[] = [
      { hdr: true },
      { hdr: true, video_tracks: [{ color_transfer: "smpte2084" }] },
    ];
    expect(bestVideoRangeLabel(versions)).toBe("HDR10");
  });

  it("returns empty when every version is SDR", () => {
    expect(bestVideoRangeLabel([{ hdr: false }, { hdr: false }])).toBe("");
  });
});
