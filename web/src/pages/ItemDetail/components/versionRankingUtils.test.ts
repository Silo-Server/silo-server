// @vitest-environment node

import { describe, expect, it } from "vitest";
import { makeFileVersion as makeVersion } from "../itemDetail.test-support";
import { pickBestAttributes, selectDefaultVersion } from "./versionRankingUtils";

describe("pickBestAttributes", () => {
  it("returns null for empty versions array", () => {
    expect(pickBestAttributes([])).toBeNull();
  });

  it("picks the best resolution across versions", () => {
    const versions = [
      makeVersion({ resolution: "720p", codec_audio: "aac" }),
      makeVersion({ resolution: "1080p", codec_audio: "aac" }),
      makeVersion({ resolution: "4k", codec_audio: "aac" }),
    ];
    const result = pickBestAttributes(versions);
    expect(result).not.toBeNull();
    expect(result!.resolution).toBe("4k");
  });

  it("picks the best audio across versions", () => {
    const versions = [
      makeVersion({ resolution: "1080p", codec_audio: "aac" }),
      makeVersion({ resolution: "1080p", codec_audio: "dts" }),
      makeVersion({ resolution: "1080p", codec_audio: "TrueHD Atmos" }),
    ];
    const result = pickBestAttributes(versions);
    expect(result).not.toBeNull();
    expect(result!.audioLabel).toBe("TrueHD Atmos");
  });

  it("labels Atmos from a later track of the same codec", () => {
    const versions = [
      makeVersion({
        codec_audio: "eac3",
        audio_tracks: [
          { codec: "eac3", language: "en" },
          { codec: "eac3", profile: "Dolby Digital Plus + Dolby Atmos", language: "en" },
        ],
      }),
    ];
    expect(pickBestAttributes(versions)?.audioLabel).toBe("DD+ Atmos");
  });

  // Real files never pair an aac track with an Atmos profile; the mismatch
  // proves the profile is read only from codec_audio's own stream.
  it("does not read a profile from a first track that is not codec_audio's stream", () => {
    const versions = [
      makeVersion({
        codec_audio: "eac3",
        audio_tracks: [
          { codec: "aac", profile: "Dolby Digital Plus + Dolby Atmos", language: "en" },
        ],
      }),
    ];
    expect(pickBestAttributes(versions)?.audioLabel).toBe("EAC3");
  });

  it("sets hdr true when any version has hdr", () => {
    const versions = [
      makeVersion({ resolution: "1080p", hdr: false }),
      makeVersion({ resolution: "1080p", hdr: true }),
    ];
    const result = pickBestAttributes(versions);
    expect(result).not.toBeNull();
    expect(result!.hdr).toBe(true);
  });

  it("checks audio_tracks for higher-quality codecs", () => {
    const versions = [
      makeVersion({
        resolution: "1080p",
        codec_audio: "aac",
        audio_tracks: [{ codec: "TrueHD Atmos", language: "en" }],
      }),
    ];
    const result = pickBestAttributes(versions);
    expect(result).not.toBeNull();
    expect(result!.audioLabel).toBe("TrueHD Atmos");
  });

  it("ignores audio_tracks entries without codec", () => {
    const versions = [
      makeVersion({
        resolution: "1080p",
        codec_audio: "aac",
        audio_tracks: [{ language: "en" }],
      }),
    ];
    const result = pickBestAttributes(versions);
    expect(result).not.toBeNull();
    expect(result!.audioLabel).toBe("AAC");
  });

  it("returns empty audioLabel when codec_audio is empty and no audio_tracks", () => {
    const versions = [makeVersion({ resolution: "1080p", codec_audio: "" })];
    const result = pickBestAttributes(versions);
    expect(result).not.toBeNull();
    expect(result!.audioLabel).toBe("");
  });
});

describe("selectDefaultVersion audio matching", () => {
  it("still matches versions by codec when the preferred track's profile names Atmos", () => {
    const plain = makeVersion({ file_id: 1, resolution: "2160p", codec_audio: "aac" });
    const atmos = makeVersion({
      file_id: 2,
      resolution: "2160p",
      codec_audio: "eac3",
      audio_tracks: [
        { codec: "eac3", profile: "Dolby Digital Plus + Dolby Atmos", language: "en" },
      ],
    });
    expect(selectDefaultVersion([plain, atmos])?.file_id).toBe(2);
  });
});
