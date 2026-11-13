// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { LeafItemUserData, PlaybackVariant } from "@/api/types";
import { makeFileVersion as makeVersion } from "../itemDetail.test-support";
import {
  playbackEditions,
  selectedDownloadVersions,
  selectedStartVersion,
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

describe("selectDefaultVersion tie-break", () => {
  // Same order as the server's sortFileVersions; audio never breaks a tie.
  it.each([
    [
      "HDR before SDR",
      { hdr: false, file_size: 9, codec_audio: "truehd" },
      { hdr: true, file_size: 1, codec_audio: "aac" },
    ],
    ["larger file", { file_size: 1, codec_audio: "truehd" }, { file_size: 2, codec_audio: "aac" }],
  ] as const)("prefers %s at the same resolution", (_name, first, second) => {
    const files = [
      makeVersion({ file_id: 1, resolution: "2160p", ...first }),
      makeVersion({ file_id: 2, resolution: "2160p", ...second }),
    ];
    expect(selectDefaultVersion(files)?.file_id).toBe(2);
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
  })) as PlaybackVariant[];

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
        history as LeafItemUserData,
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
    expect(selectDefaultVersion(files, "1080p")?.file_id).toBe(2);
  });

  it("breaks a no-history tie between cuts by edition preference, not file size", () => {
    const cut = (file_id: number, edition_key: string, file_size: number) => {
      const file = makeVersion({ file_id, edition_key, resolution: "2160p", file_size });
      return { variant_id: edition_key, edition_key, parts: [{ part_index: 1, versions: [file] }] };
    };
    const cuts = [cut(1, "extended", 9), cut(2, "theatrical", 1)] as PlaybackVariant[];
    expect(selectDefaultPlaybackVariantVersion([], cuts)?.file_id).toBe(2);
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
  ] as PlaybackVariant[];
  const files = [standard, partOne, partTwo, extended];
  expect(
    playbackEditions(files, variants).map((e) => [e.key, e.versions.map((v) => v.file_id)]),
  ).toEqual([
    ["", [1, 2]],
    ["extended", [4]],
  ]);
  // A later part last played resumes as is; Start Over and Download begin at part 1.
  const resumed = selectDefaultPlaybackVariantVersion(files, variants, {
    played: false,
    position_seconds: 600,
    last_file_id: 3,
  });
  expect(resumed?.file_id).toBe(3);
  // Finished on the last part: no resume point, so a rewatch starts at part 1.
  expect(
    selectDefaultPlaybackVariantVersion(files, variants, {
      played: true,
      position_seconds: 0,
      last_file_id: 3,
    })?.file_id,
  ).toBe(1);
  expect(selectedStartVersion(resumed, variants)?.file_id).toBe(2);
  expect(selectedDownloadVersions(resumed, variants).map((v) => v.file_id)).toEqual([2, 3]);
  // Picking another cut leaves the later part behind.
  expect(
    selectDefaultPlaybackVariantVersion(
      files,
      variants,
      { last_file_id: 3 } as LeafItemUserData,
      undefined,
      "extended",
    )?.file_id,
  ).toBe(4);
  expect(
    selectDefaultPlaybackVariantVersion(
      files,
      variants,
      { last_edition_key: "extended" } as LeafItemUserData,
      undefined,
      "",
    )?.file_id,
  ).toBe(1);
});
