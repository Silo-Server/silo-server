// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  audioTrackHasAtmos,
  audioTrackLabel,
  compactHdrSuffix,
  formatChannels,
  formatCodecLabel,
  formatFileSize,
  mapAudioLabel,
  prettyResolution,
} from "./mediaFormat";

describe("formatCodecLabel", () => {
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
    expect(mapAudioLabel("TrueHD Atmos")).toBe("TrueHD Atmos");
    expect(mapAudioLabel("eac3 atmos")).toBe("DD+ Atmos");
    expect(mapAudioLabel("Atmos")).toBe("Atmos");
  });

  it("reads Atmos from the FFprobe profile of a plain codec", () => {
    expect(mapAudioLabel("eac3", "Dolby Digital Plus + Dolby Atmos")).toBe("DD+ Atmos");
    expect(mapAudioLabel("truehd", "Dolby TrueHD + Dolby Atmos")).toBe("TrueHD Atmos");
    expect(mapAudioLabel("eac3", "")).toBe("EAC3");
    expect(mapAudioLabel("dts", "DTS-HD MA")).toBe("DTS");
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

  it("returns the fallback for zero, negative, and missing values", () => {
    expect(formatFileSize(0)).toBe("");
    expect(formatFileSize(-100)).toBe("");
    expect(formatFileSize(undefined)).toBe("");
    expect(formatFileSize(0, { fallback: "—" })).toBe("—");
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
  it("keeps HDR10+, but Dolby Vision wins on a DV + HDR10+ file", () => {
    expect(compactHdrSuffix("HDR10+")).toBe("HDR10+");
    expect(compactHdrSuffix("DV HDR10+")).toBe("DV");
    expect(compactHdrSuffix("HDR10")).toBe("HDR");
  });
});

describe("audioTrackLabel", () => {
  it("reads Atmos from a track's profile or titles", () => {
    const longName = "ATSC A/52B (AC-3, E-AC-3)";
    expect(
      audioTrackLabel({
        codec: "eac3",
        title: longName,
        profile: "Dolby Digital Plus + Dolby Atmos",
      }),
    ).toBe("DD+ Atmos");
    expect(audioTrackLabel({ codec: "truehd", embedded_title: "TrueHD Atmos 7.1" })).toBe(
      "TrueHD Atmos",
    );
    expect(audioTrackLabel({ codec: "eac3", title: longName })).toBe("EAC3");
    expect(audioTrackLabel({ title: "Atmos" })).toBe("");
    expect(audioTrackHasAtmos({ codec: "eac3", profile: "Dolby Digital Plus + Dolby Atmos" })).toBe(
      true,
    );
    expect(audioTrackHasAtmos({ codec: "eac3", title: longName })).toBe(false);
  });

  it("ignores Atmos titles on codecs that cannot carry it", () => {
    expect(audioTrackLabel({ codec: "ac3", title: "AC3 5.1 (from TrueHD Atmos)" })).toBe("AC3");
    expect(audioTrackLabel({ codec: "aac", title: "AAC 2.0 (DDP Atmos downmix)" })).toBe("AAC");
    expect(audioTrackHasAtmos({ codec: "opus", title: "Opus 5.1 from Atmos" })).toBe(false);
    expect(audioTrackLabel({ codec: "mlp", profile: "Dolby Atmos" })).toBe("Atmos");
  });

  it("matches Atmos only as a word", () => {
    expect(audioTrackLabel({ codec: "eac3", title: "Atmospheric score" })).toBe("EAC3");
  });
});
