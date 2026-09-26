import { describe, expect, it } from "vitest";

import type { PlayerFileVersion, PlayerMarkerSegment } from "../types";
import {
  markerOccurrenceAtTime,
  patchVersionMarkers,
  resolveActiveVersionMarkers,
  resolveAutoplayMarker,
  resolveMarkerRegions,
} from "./watchPageMarkers";

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
    recap: overrides.recap,
    preview: overrides.preview,
    marker_segments: overrides.marker_segments,
  };
}

function segments(...entries: [string, number, number][]): PlayerMarkerSegment[] {
  return entries.map(([kind, start, end]) => ({
    kind: kind as PlayerMarkerSegment["kind"],
    start_seconds: start,
    end_seconds: end,
  }));
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

describe("resolveMarkerRegions", () => {
  it("prefers the complete occurrence list over the singular fields", () => {
    const regions = resolveMarkerRegions(
      makeVersion({
        intro: { start: 10, end: 60 },
        marker_segments: segments(["intro", 10, 60], ["intro", 900, 940]),
      }),
    );
    expect(regions).toEqual([
      { kind: "intro", start: 10, end: 60 },
      { kind: "intro", start: 900, end: 940 },
    ]);
  });

  it("falls back to the singular fields and drops unusable ranges", () => {
    const regions = resolveMarkerRegions(
      makeVersion({
        intro: { start: 10, end: 60 },
        credits: { start: 60, end: 60 },
        recap: null,
      }),
    );
    expect(regions).toEqual([{ kind: "intro", start: 10, end: 60 }]);
  });

  it("orders occurrences by start time", () => {
    const regions = resolveMarkerRegions(
      makeVersion({ marker_segments: segments(["credits", 1500, 1700], ["intro", 12, 50]) }),
    );
    expect(regions.map((region) => region.kind)).toEqual(["intro", "credits"]);
  });
});

describe("markerOccurrenceAtTime", () => {
  const regions = resolveMarkerRegions(
    makeVersion({
      marker_segments: segments(["intro", 10, 40], ["intro", 300, 340], ["credits", 1500, 1700]),
    }),
  );

  it("returns null before the first occurrence starts", () => {
    expect(markerOccurrenceAtTime(regions, "intro", 5)).toBeNull();
  });

  it("keeps the previous occurrence after it ends so a skip can be undone", () => {
    expect(markerOccurrenceAtTime(regions, "intro", 70)).toEqual({
      kind: "intro",
      start: 10,
      end: 40,
    });
  });

  it("selects the occurrence covering the playhead", () => {
    expect(markerOccurrenceAtTime(regions, "intro", 310)).toEqual({
      kind: "intro",
      start: 300,
      end: 340,
    });
  });

  it("ignores other kinds", () => {
    expect(markerOccurrenceAtTime(regions, "preview", 310)).toBeNull();
  });

  it("uses an active occurrence when a nested occurrence has ended", () => {
    const nested = resolveMarkerRegions(
      makeVersion({ marker_segments: segments(["intro", 10, 50], ["intro", 20, 30]) }),
    );
    expect(markerOccurrenceAtTime(nested, "intro", 35)).toEqual({
      kind: "intro",
      start: 10,
      end: 50,
    });
  });

  it.each([30, 50])(
    "keeps the last ending occurrence after skipping an inner marker ending at %s",
    (innerEnd) => {
      const nested = resolveMarkerRegions(
        makeVersion({ marker_segments: segments(["intro", 10, 50], ["intro", 20, innerEnd]) }),
      );
      expect(markerOccurrenceAtTime(nested, "intro", 50)).toEqual({
        kind: "intro",
        start: 10,
        end: 50,
      });
    },
  );

  it("prefers the earliest start when active occurrences have the same end", () => {
    const nested = resolveMarkerRegions(
      makeVersion({ marker_segments: segments(["intro", 10, 50], ["intro", 20, 50]) }),
    );
    expect(markerOccurrenceAtTime(nested, "intro", 25)).toEqual({
      kind: "intro",
      start: 10,
      end: 50,
    });
  });

  it("keeps selecting the latest start when active occurrences have different ends", () => {
    const nested = resolveMarkerRegions(
      makeVersion({ marker_segments: segments(["intro", 10, 50], ["intro", 20, 30]) }),
    );
    expect(markerOccurrenceAtTime(nested, "intro", 25)).toEqual({
      kind: "intro",
      start: 20,
      end: 30,
    });
  });
});

describe("resolveAutoplayMarker", () => {
  it("treats only the closing credits as the auto-next trigger", () => {
    const regions = resolveMarkerRegions(
      makeVersion({ marker_segments: segments(["credits", 1000, 1100], ["credits", 1700, 1800]) }),
    );
    expect(resolveAutoplayMarker(regions, 1800, false)).toEqual({ start: 1700, end: 1800 });
  });

  it("does not span the narrative gap before the closing credits", () => {
    const regions = resolveMarkerRegions(
      makeVersion({ marker_segments: segments(["credits", 1000, 1100]) }),
    );
    expect(resolveAutoplayMarker(regions, 1800, false)).toBeNull();
  });

  it("counts previews only when previews may autoplay next", () => {
    const regions = resolveMarkerRegions(
      makeVersion({ marker_segments: segments(["preview", 1700, 1800]) }),
    );
    expect(resolveAutoplayMarker(regions, 1800, false)).toBeNull();
    expect(resolveAutoplayMarker(regions, 1800, true)).toEqual({ start: 1700, end: 1800 });
  });
});
