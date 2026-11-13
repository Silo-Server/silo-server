// @vitest-environment node

import { describe, expect, it } from "vitest";
import { makeFileVersion as makeVersion } from "../itemDetail.test-support";
import {
  playbackEditions,
  selectedDownloadVersions,
  pickBestAttributes,
  selectDefaultVersion,
  selectDefaultPlaybackVariantVersion,
} from "./versionRankingUtils";

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

describe("detail edition Auto", () => {
  it.each([
    ["4320p", undefined, 1],
    ["8k", undefined, 1],
    ["4320p", "1080p", 2],
    ["", "1080p", 2],
  ] as const)("ranks %s against 2160p with preference %s", (resolution, quality, expected) => {
    const files = [
      makeVersion({ file_id: 1, resolution }),
      makeVersion({ file_id: 2, resolution: "2160p" }),
    ];
    expect(selectDefaultPlaybackVariantVersion(files, undefined, undefined, quality)?.file_id).toBe(
      expected,
    );
  });

  const versions = [
    makeVersion({
      file_id: 1,
      edition_key: "theatrical",
      resolution: "1080p",
      hdr: false,
      codec_audio: "aac",
    }),
    makeVersion({
      file_id: 2,
      edition_key: "extended",
      resolution: "2160p",
      hdr: true,
      codec_audio: "dts",
    }),
    makeVersion({
      file_id: 3,
      edition_key: "extended",
      resolution: "1080p",
      hdr: false,
      codec_audio: "aac",
    }),
  ];
  const variants = ["theatrical", "extended"].map((edition_key) => ({
    variant_id: edition_key,
    edition_key,
    parts: [
      {
        part_index: 1,
        default_file_id: edition_key === "theatrical" ? 1 : 2,
        versions: versions.filter((v) => v.edition_key === edition_key),
      },
    ],
  })) as import("@/api/types").PlaybackVariant[];

  it.each([
    ["quality chooses the initial edition", undefined, undefined, undefined, 2],
    ["last file chooses edition, not quality", { last_file_id: 3 }, undefined, undefined, 2],
    [
      "remembered edition wins over last file",
      { last_edition_key: "theatrical", last_file_id: 2 },
      undefined,
      undefined,
      1,
    ],
    ["Auto stays in the chosen edition", undefined, undefined, "theatrical", 1],
    ["quality cap beats server default file", undefined, "1080p", "extended", 3],
    ["closest above cap stays in edition", undefined, "720p", "extended", 3],
  ] as const)("%s", (_name, history, quality, edition, expected) => {
    expect(
      selectDefaultPlaybackVariantVersion(
        versions,
        variants,
        history as import("@/api/types").LeafItemUserData,
        quality,
        edition,
      )?.file_id,
    ).toBe(expected);
  });

  it("does not escape the cap when best attributes belong to different files", () => {
    const files = [
      makeVersion({ file_id: 1, resolution: "2160p", hdr: true, codec_audio: "truehd" }),
      makeVersion({ file_id: 2, resolution: "1080p", hdr: false, codec_audio: "aac" }),
      makeVersion({ file_id: 3, resolution: "720p", hdr: true, codec_audio: "truehd" }),
    ];
    expect(selectDefaultVersion(files, undefined, "1080p")?.file_id).toBe(2);
  });
});

it("groups presentations by edition and recovers history from a later part", () => {
  const standard = makeVersion({ file_id: 1, edition_key: "", resolution: "1080p" });
  const partOne = makeVersion({ file_id: 2, edition_key: "", resolution: "720p" });
  const partTwo = makeVersion({ file_id: 3, edition_key: "", resolution: "2160p" });
  const extended = makeVersion({ file_id: 4, edition_key: "extended", resolution: "2160p" });
  const variants = [
    { variant_id: "single", parts: [{ part_index: 1, versions: [standard] }] },
    {
      variant_id: "multipart",
      parts: [
        { part_index: 1, versions: [partOne] },
        { part_index: 2, versions: [partTwo] },
      ],
    },
    {
      variant_id: "extended",
      edition_key: "extended",
      parts: [{ part_index: 1, versions: [extended] }],
    },
  ] as import("@/api/types").PlaybackVariant[];
  const files = [standard, partOne, partTwo, extended];
  expect(
    playbackEditions(files, variants).map((e) => [e.key, e.versions.map((v) => v.file_id)]),
  ).toEqual([
    ["", [1, 2]],
    ["extended", [4]],
  ]);
  expect(
    selectDefaultPlaybackVariantVersion(files, variants, {
      last_file_id: 3,
    } as import("@/api/types").LeafItemUserData)?.file_id,
  ).toBe(1);
  expect(
    selectDefaultPlaybackVariantVersion(
      files,
      variants,
      { last_edition_key: "extended" } as import("@/api/types").LeafItemUserData,
      undefined,
      "",
    )?.file_id,
  ).toBe(1);
  expect(selectedDownloadVersions(partOne, variants).map((v) => v.file_id)).toEqual([2, 3]);
});
