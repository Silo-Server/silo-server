import { describe, expect, it } from "vitest";

import type { PlayerFileVersion } from "../types";
import { patchVersionMarkers, resolveActiveVersionMarkers } from "./watchPageMarkers";

function makeVersion(overrides: Partial<PlayerFileVersion> = {}): PlayerFileVersion {
  return {
    file_id: overrides.file_id ?? 1,
    resolution: overrides.resolution ?? "1080p",
    codec_video: overrides.codec_video ?? "h264",
    codec_audio: overrides.codec_audio ?? "aac",
    hdr: overrides.hdr ?? false,
    container: overrides.container ?? "mp4",
    file_size: overrides.file_size ?? 1000,
    duration: overrides.duration ?? 1800,
    bitrate: overrides.bitrate ?? 2000,
    intro: overrides.intro,
    credits: overrides.credits,
  };
}

describe("resolveActiveVersionMarkers", () => {
  it("uses only the selected version markers", () => {
    expect(
      resolveActiveVersionMarkers(
        makeVersion({
          intro: null,
          credits: { start: 1500, end: 1790 },
        }),
      ),
    ).toEqual({
      intro: null,
      credits: { start: 1500, end: 1790 },
      recap: null,
      preview: null,
    });
  });

  it("returns null markers when the selected version has none", () => {
    expect(resolveActiveVersionMarkers(makeVersion({ intro: null, credits: null }))).toEqual({
      intro: null,
      credits: null,
      recap: null,
      preview: null,
    });
  });
});

describe("patchVersionMarkers", () => {
  it("patches intro and credits for only the targeted file version", () => {
    const versions = [
      makeVersion({ file_id: 1, intro: null, credits: null }),
      makeVersion({
        file_id: 2,
        intro: { start: 10, end: 60 },
        credits: { start: 1500, end: 1790 },
      }),
    ];

    const patched = patchVersionMarkers(
      versions,
      1,
      { start: 12, end: 70 },
      { start: 1490, end: 1780 },
    );

    expect(patched[0]).toMatchObject({
      intro: { start: 12, end: 70 },
      credits: { start: 1490, end: 1780 },
    });
    expect(patched[1]).toMatchObject({
      intro: { start: 10, end: 60 },
      credits: { start: 1500, end: 1790 },
    });
  });
});
