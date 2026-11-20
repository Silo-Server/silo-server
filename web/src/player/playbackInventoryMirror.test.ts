import { describe, expect, it } from "vitest";

import type { FileVersion } from "@/api/types";
import {
  inventoryPublicationTargets,
  mergePlaybackInventory,
  planInventoryDescribesRequestedFile,
  type PlaybackInventorySnapshot,
} from "./playbackInventoryMirror";

function version(overrides: Partial<FileVersion> = {}): FileVersion {
  return {
    file_id: 7,
    file_path: "/media/heat.mkv",
    resolution: "1080p",
    codec_video: "h264",
    codec_audio: "eac3",
    hdr: false,
    container: "mkv",
    file_size: 0,
    duration: 7_200,
    bitrate: 0,
    audio_tracks: [{ language: "eng", codec: "eac3", channels: 6 }],
    subtitle_tracks: [{ index: 0, language: "en", codec: "subrip", title: "English" }],
    ...overrides,
  };
}

function snapshot(overrides: Partial<PlaybackInventorySnapshot> = {}): PlaybackInventorySnapshot {
  return {
    fileId: 7,
    audioTracks: [{ language: "fra", codec: "aac", channels: 2 }],
    subtitleTracks: [{ index: 3, language: "fr", label: "French", url: "/subs/fr.vtt" }],
    audioVerified: false,
    subtitleVerified: false,
    updatedAt: 1,
    ...overrides,
  };
}

describe("planInventoryDescribesRequestedFile", () => {
  it("is true while the plan serves the requested row", () => {
    // A virtual rotation keeps effective_media_file_id at the requested row and
    // moves virtual_source_revision instead, so the inventory still describes
    // the viewer's selection.
    expect(
      planInventoryDescribesRequestedFile({
        requested_media_file_id: 7,
        effective_media_file_id: 7,
      }),
    ).toBe(true);
  });

  it("is false when the plan served a different row", () => {
    // A substitution (or a lower version chosen for this device) carries its own
    // stream table; the requested version's pre-play menu must not adopt it.
    expect(
      planInventoryDescribesRequestedFile({
        requested_media_file_id: 881207,
        effective_media_file_id: 1073913,
      }),
    ).toBe(false);
  });

  it("is false without a plan or ids", () => {
    expect(planInventoryDescribesRequestedFile(null)).toBe(false);
    expect(planInventoryDescribesRequestedFile(undefined)).toBe(false);
    expect(planInventoryDescribesRequestedFile({})).toBe(false);
    expect(
      planInventoryDescribesRequestedFile({
        requested_media_file_id: 7,
        effective_media_file_id: 0,
      }),
    ).toBe(false);
  });
});

describe("inventoryPublicationTargets", () => {
  it("publishes a substituted release only under the file it actually serves", () => {
    // The viewer asked for A (7) but the server served B (9). B's track table
    // says nothing about A, so nothing is published under A.
    expect(
      inventoryPublicationTargets({ requested_media_file_id: 7, effective_media_file_id: 9 }, 9),
    ).toEqual([9]);
  });

  it("publishes under both ids while the plan still serves the requested row", () => {
    expect(
      inventoryPublicationTargets({ requested_media_file_id: 7, effective_media_file_id: 7 }, 7),
    ).toEqual([7]);
    // The effective id is the transport's answer; the requested id is added
    // only when it names the same row through the plan's own fields.
    expect(
      inventoryPublicationTargets({ requested_media_file_id: 7, effective_media_file_id: 7 }, 9),
    ).toEqual([9, 7]);
  });

  it("publishes nothing without an effective file id", () => {
    expect(inventoryPublicationTargets(null, null)).toEqual([]);
    expect(inventoryPublicationTargets(null, 0)).toEqual([]);
  });
});

describe("mergePlaybackInventory", () => {
  it("overlays declared rows so the item page can show them immediately", () => {
    const merged = mergePlaybackInventory(version(), snapshot());

    expect(merged.audio_tracks).toEqual([{ language: "fra", codec: "aac", channels: 2 }]);
    expect(merged.subtitle_tracks?.[0]?.language).toBe("fr");
  });

  it("does not regress a probed list back to a declared player snapshot", () => {
    const probed = version({
      probed_at: "2026-11-13T19:36:20Z",
      audio_tracks: [{ language: "eng", codec: "eac3", channels: 6, default: true }],
      subtitle_tracks: [
        { index: 0, language: "en", codec: "subrip", title: "English" },
        { index: 1, language: "fr", codec: "subrip", title: "French" },
      ],
    });

    const merged = mergePlaybackInventory(probed, snapshot(), {
      audioProbed: true,
      subtitlesProbed: true,
    });

    expect(merged.audio_tracks).toEqual(probed.audio_tracks);
    expect(merged.subtitle_tracks).toEqual(probed.subtitle_tracks);
  });

  it("lets verified player evidence replace a declared list", () => {
    const merged = mergePlaybackInventory(
      version(),
      snapshot({ audioVerified: true, subtitleVerified: true }),
    );

    expect(merged.audio_tracks?.[0]?.language).toBe("fra");
    expect(merged.subtitle_tracks?.[0]?.language).toBe("fr");
  });

  it("keeps a verified empty inventory empty instead of restoring a declaration", () => {
    const merged = mergePlaybackInventory(
      version(),
      snapshot({
        audioTracks: [],
        subtitleTracks: [],
        audioVerified: true,
        subtitleVerified: true,
      }),
    );

    expect(merged.audio_tracks).toEqual([]);
    expect(merged.subtitle_tracks).toEqual([]);
  });
});
