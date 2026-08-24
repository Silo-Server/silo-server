import { describe, expect, it } from "vitest";
import {
  compactHdrSuffix,
  dolbyVisionLabel,
  formatAudioTrackLabel,
  formatBitrate,
  formatChannels,
  formatCodecLabel,
  formatDynamicRangeLabel,
  formatFileSize,
  formatMbpsFromKbps,
  formatSampleRate,
  formatVideoQualitySummary,
  formatVersionAudioLabel,
  formatVersionQualitySummary,
  mapAudioLabel,
  prettyResolution,
} from "./mediaFormat";

describe("formatCodecLabel", () => {
  it("maps known codecs to display labels", () => {
    expect(formatCodecLabel("h264")).toBe("H.264");
    expect(formatCodecLabel("hevc")).toBe("HEVC");
    expect(formatCodecLabel("eac3")).toBe("Dolby Digital+");
    expect(formatCodecLabel("truehd")).toBe("TrueHD");
    expect(formatCodecLabel("opus")).toBe("Opus");
  });

  it("uppercases unknown codecs", () => {
    expect(formatCodecLabel("vp9")).toBe("VP9");
  });

  it("returns the fallback for missing codecs", () => {
    expect(formatCodecLabel(undefined)).toBe("—");
    expect(formatCodecLabel("  ")).toBe("—");
    expect(formatCodecLabel("", "")).toBe("");
  });
});

describe("mapAudioLabel", () => {
  it("prefers Atmos over other markers", () => {
    expect(mapAudioLabel("TrueHD Atmos")).toBe("Atmos");
    expect(mapAudioLabel("eac3 atmos")).toBe("Atmos");
  });

  it("maps codec families", () => {
    expect(mapAudioLabel("truehd")).toBe("TrueHD");
    expect(mapAudioLabel("dts-hd ma")).toBe("DTS-HD");
    expect(mapAudioLabel("dts:x")).toBe("DTS-HD");
    expect(mapAudioLabel("dts")).toBe("DTS");
    expect(mapAudioLabel("eac3")).toBe("EAC3");
    expect(mapAudioLabel("e-ac-3")).toBe("EAC3");
    expect(mapAudioLabel("aac")).toBe("AAC");
    expect(mapAudioLabel("flac")).toBe("FLAC");
  });

  it("uppercases unknown codecs, unlike formatCodecLabel's curated casing", () => {
    expect(mapAudioLabel("pcm_s24le")).toBe("PCM_S24LE");
    expect(mapAudioLabel("mp3")).toBe("MP3");
    expect(mapAudioLabel("opus")).toBe("OPUS");
  });
});

describe("precise media labels", () => {
  it("prefers Dolby Vision evidence over the generic HDR flag", () => {
    expect(
      formatDynamicRangeLabel({
        hdr: true,
        video_tracks: [{ dv_profile: 8, video_range_type: "DOVIWithHDR10" }],
      }),
    ).toBe("Dolby Vision");
    expect(formatDynamicRangeLabel({ hdr: true, video_tracks: [] })).toBe("HDR");
    expect(formatDynamicRangeLabel({ hdr: false, video_tracks: [] })).toBeNull();
  });

  it("distinguishes Dolby Digital Plus and TrueHD Atmos", () => {
    expect(
      formatAudioTrackLabel({
        codec: "eac3",
        profile: "Dolby Digital Plus + Dolby Atmos",
      }),
    ).toBe("DD+ Atmos");
    expect(formatAudioTrackLabel({ codec: "truehd", profile: "Dolby TrueHD + Dolby Atmos" })).toBe(
      "TrueHD Atmos",
    );
    expect(formatAudioTrackLabel({ codec: "opus", title: "English Atmos" })).toBe("Atmos");
    expect(formatAudioTrackLabel({ codec: "eac3", profile: "Dolby Digital Plus" })).toBe("EAC3");
  });

  it("uses the effective or default audio track before the file fallback", () => {
    expect(
      formatVersionAudioLabel({
        codec_audio: "aac",
        effective_audio_track_index: 1,
        audio_tracks: [
          { codec: "truehd", profile: "Dolby TrueHD + Dolby Atmos", default: true },
          { codec: "eac3", profile: "Dolby Digital Plus + Dolby Atmos" },
        ],
      }),
    ).toBe("DD+ Atmos");
  });

  it("builds one consistent version summary", () => {
    const version = {
      resolution: "2160p",
      codec_video: "hevc",
      codec_audio: "eac3",
      hdr: true,
      video_tracks: [{ dv_profile: 8 }],
      audio_tracks: [{ codec: "eac3", profile: "Dolby Digital Plus + Dolby Atmos", default: true }],
    };

    expect(formatVideoQualitySummary(version)).toBe("4K · HEVC · Dolby Vision");
    expect(formatVersionQualitySummary(version)).toBe("4K · HEVC · Dolby Vision · DD+ Atmos");
  });
});

describe("formatFileSize", () => {
  it("formats bytes in GB", () => {
    expect(formatFileSize(2 * 1024 ** 3)).toBe("2.0 GB");
    expect(formatFileSize(1.5 * 1024 ** 3)).toBe("1.5 GB");
  });

  it("formats bytes in MB and KB", () => {
    expect(formatFileSize(512 * 1024 ** 2)).toBe("512.0 MB");
    expect(formatFileSize(2 * 1024)).toBe("2.0 KB");
    expect(formatFileSize(100)).toBe("100 B");
  });

  it("uses IEC unit labels when requested", () => {
    expect(formatFileSize(2 * 1024 ** 3, { iecUnits: true })).toBe("2.0 GiB");
    expect(formatFileSize(512 * 1024 ** 2, { iecUnits: true })).toBe("512.0 MiB");
    expect(formatFileSize(2 * 1024, { iecUnits: true })).toBe("2.0 KiB");
  });

  it("returns the fallback for zero, negative, and missing values", () => {
    expect(formatFileSize(0)).toBe("");
    expect(formatFileSize(-100)).toBe("");
    expect(formatFileSize(undefined)).toBe("");
    expect(formatFileSize(0, { fallback: "—" })).toBe("—");
  });
});

describe("formatBitrate", () => {
  it("formats kbps with thousands separators", () => {
    expect(formatBitrate(24_500)).toBe("24,500 kbps");
    expect(formatBitrate(640)).toBe("640 kbps");
  });

  it("returns the fallback for missing values", () => {
    expect(formatBitrate(0)).toBe("");
    expect(formatBitrate(undefined)).toBe("");
    expect(formatBitrate(undefined, "—")).toBe("—");
  });
});

describe("formatMbpsFromKbps", () => {
  it("converts kbps to Mbps with one decimal", () => {
    expect(formatMbpsFromKbps(24_500)).toBe("24.5 Mbps");
    expect(formatMbpsFromKbps(800)).toBe("0.8 Mbps");
  });

  it("returns the fallback for missing values", () => {
    expect(formatMbpsFromKbps(0)).toBe("—");
    expect(formatMbpsFromKbps(undefined, "")).toBe("");
  });
});

describe("formatSampleRate", () => {
  it("formats sample rates in Hz", () => {
    expect(formatSampleRate(48_000)).toBe("48,000 Hz");
  });

  it("returns the fallback for missing values", () => {
    expect(formatSampleRate(0)).toBe("");
    expect(formatSampleRate(undefined, "—")).toBe("—");
  });
});

describe("formatChannels", () => {
  it("maps channel counts to layout labels", () => {
    expect(formatChannels(8)).toBe("7.1");
    expect(formatChannels(6)).toBe("5.1");
    expect(formatChannels(2)).toBe("stereo");
    expect(formatChannels(3)).toBe("3 ch");
  });

  it("returns empty string for missing values", () => {
    expect(formatChannels(undefined)).toBe("");
    expect(formatChannels(0)).toBe("");
  });
});

describe("dolbyVisionLabel", () => {
  it("prefixes raw profile strings", () => {
    expect(dolbyVisionLabel("Profile 8.1")).toBe("Dolby Vision Profile 8.1");
  });

  it("keeps labels that already carry the prefix", () => {
    expect(dolbyVisionLabel("Dolby Vision Profile 5")).toBe("Dolby Vision Profile 5");
  });
});

describe("prettyResolution", () => {
  it("maps 2160p variants to 4K and 4320p variants to 8K", () => {
    expect(prettyResolution("2160p")).toBe("4K");
    expect(prettyResolution("4k")).toBe("4K");
    expect(prettyResolution("uhd")).toBe("4K");
    expect(prettyResolution("4320p")).toBe("8K");
  });

  it("keeps lowercase-p resolutions and uppercases unknowns", () => {
    expect(prettyResolution("1080p")).toBe("1080p");
    expect(prettyResolution("720p")).toBe("720p");
    expect(prettyResolution("sd")).toBe("SD");
  });

  it("returns null for missing values", () => {
    expect(prettyResolution(undefined)).toBeNull();
    expect(prettyResolution("  ")).toBeNull();
  });
});

describe("compactHdrSuffix", () => {
  it("collapses Dolby Vision variants to DV and others to HDR", () => {
    expect(compactHdrSuffix("DV HDR10")).toBe("DV");
    expect(compactHdrSuffix("HDR10")).toBe("HDR");
    expect(compactHdrSuffix("HLG")).toBe("HDR");
  });

  it("returns null for missing values", () => {
    expect(compactHdrSuffix(undefined)).toBeNull();
  });
});
