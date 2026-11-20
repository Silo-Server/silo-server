// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { FileVersion } from "@/api/types";
import {
  pickBestAttributes,
  selectDefaultPlaybackVariantVersion,
  selectDefaultVersion,
  selectItemVersion,
} from "./versionRankingUtils";

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
    audio_tracks: overrides.audio_tracks,
    video_tracks: overrides.video_tracks,
    subtitle_tracks: overrides.subtitle_tracks,
    available: overrides.available,
    failed: overrides.failed,
  };
}

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

  it("labels Atmos from the primary audio track's profile", () => {
    const versions = [
      makeVersion({
        resolution: "2160p",
        codec_audio: "eac3",
        audio_tracks: [
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

  it("sets hdr false when no version has hdr", () => {
    const versions = [
      makeVersion({ resolution: "1080p", hdr: false }),
      makeVersion({ resolution: "720p", hdr: false }),
    ];
    const result = pickBestAttributes(versions);
    expect(result).not.toBeNull();
    expect(result!.hdr).toBe(false);
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

describe("persisted version selection", () => {
  const theatrical = makeVersion({
    file_id: 11,
    resolution: "1080p",
    codec_audio: "aac",
    edition_key: "theatrical",
  });
  const extended = makeVersion({
    file_id: 22,
    resolution: "2160p",
    codec_audio: "eac3",
    edition_key: "extended",
  });

  it("keeps the last watched row even when its edition no longer matches the preference", () => {
    expect(
      selectDefaultVersion(
        [theatrical, extended],
        { last_file_id: 11, last_edition_key: "extended" } as never,
        "2160p",
        "extended",
      )?.file_id,
    ).toBe(11);
  });

  it("keeps the last watched row even when a variant filter moved the candidate set", () => {
    expect(
      selectDefaultPlaybackVariantVersion(
        [theatrical, extended],
        [
          {
            id: "extended",
            edition_key: "extended",
            parts: [{ part_index: 0, versions: [extended] }],
          },
          {
            id: "theatrical",
            edition_key: "theatrical",
            parts: [{ part_index: 0, versions: [theatrical] }],
          },
        ] as never,
        { last_file_id: 11, last_edition_key: "extended" } as never,
        "2160p",
        "extended",
      )?.file_id,
    ).toBe(11);
  });

  it("prefers the device's requested row over the last watched substitute", () => {
    const variants = [
      {
        id: "extended",
        edition_key: "extended",
        parts: [{ part_index: 0, versions: [extended] }],
      },
      {
        id: "theatrical",
        edition_key: "theatrical",
        parts: [{ part_index: 0, versions: [theatrical] }],
      },
    ] as never;

    // Progress persistence stored the row that served the bytes (11); the
    // viewer asked for 22.
    expect(
      selectDefaultPlaybackVariantVersion(
        [theatrical, extended],
        variants,
        { last_file_id: 11, last_edition_key: "extended" } as never,
        "2160p",
        "extended",
        22,
      )?.file_id,
    ).toBe(22);
  });

  it("falls back to the last watched row when the requested row left the catalog", () => {
    expect(
      selectDefaultPlaybackVariantVersion(
        [theatrical, extended],
        undefined,
        { last_file_id: 22 } as never,
        "2160p",
        null,
        999,
      )?.file_id,
    ).toBe(22);
  });
});

describe("selectItemVersion", () => {
  const listed = makeVersion({ file_id: 1 });
  const other = makeVersion({ file_id: 2, resolution: "720p" });

  it("keeps an explicit pick the viewer made here even when it is failed", () => {
    const failed = makeVersion({ file_id: 3, failed: true, available: false });
    const selection = selectItemVersion([listed, failed], listed, failed.file_id, failed.file_id);
    expect(selection.version?.file_id).toBe(failed.file_id);
    expect(selection.fallback).toBeNull();
  });

  it("skips a failed server hint and reports why", () => {
    const failed = makeVersion({ file_id: 3, failed: true, available: false });
    const selection = selectItemVersion([other, failed], failed, failed.file_id);
    expect(selection.version?.file_id).toBe(other.file_id);
    expect(selection.fallback).toMatchObject({
      reason: "failed",
      skipped: expect.objectContaining({ file_id: failed.file_id }),
      selected: expect.objectContaining({ file_id: other.file_id }),
    });
  });

  it("plays the best listed row when the remembered row left the list", () => {
    const selection = selectItemVersion([listed, other], listed, 999);
    expect(selection.version?.file_id).toBe(listed.file_id);
    expect(selection.fallback).toMatchObject({ reason: "unlisted", skipped: null });
  });

  it("keeps the preferred row when every row is unplayable", () => {
    const failedA = makeVersion({ file_id: 4, failed: true });
    const failedB = makeVersion({ file_id: 5, available: false });
    const selection = selectItemVersion([failedA, failedB], failedA, failedA.file_id);
    expect(selection.version?.file_id).toBe(failedA.file_id);
    expect(selection.fallback).toMatchObject({
      reason: "failed",
      skipped: expect.objectContaining({ file_id: failedA.file_id }),
      selected: expect.objectContaining({ file_id: failedA.file_id }),
    });
  });
});
