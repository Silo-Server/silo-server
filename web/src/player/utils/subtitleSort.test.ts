// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { PlayerSubtitleInfo, SubtitleMode } from "../types";
import {
  findPreferredSubtitleIndex,
  resolveSubtitleAutoSelect,
  sortSubtitlesBySource,
} from "./subtitleSort";

function makeSub(overrides: Partial<PlayerSubtitleInfo>): PlayerSubtitleInfo {
  return {
    index: 0,
    language: "en",
    label: "English",
    url: "/test",
    ...overrides,
  };
}

describe("sortSubtitlesBySource", () => {
  it("sorts embedded before external before downloaded", () => {
    const tracks = [
      makeSub({ index: 0, source: "downloaded" }),
      makeSub({ index: 1, source: "external" }),
      makeSub({ index: 2, source: "embedded" }),
    ];
    const sorted = sortSubtitlesBySource(tracks);
    expect(sorted.map((t) => t.source)).toEqual(["embedded", "external", "downloaded"]);
  });

  it("preserves relative order within same source", () => {
    const tracks = [
      makeSub({ index: 0, source: "embedded", language: "en" }),
      makeSub({ index: 1, source: "embedded", language: "es" }),
      makeSub({ index: 2, source: "external", language: "fr" }),
    ];
    const sorted = sortSubtitlesBySource(tracks);
    expect(sorted.map((t) => t.language)).toEqual(["en", "es", "fr"]);
  });

  it("treats missing source as embedded", () => {
    const tracks = [
      makeSub({ index: 0, source: "external" }),
      makeSub({ index: 1, source: undefined }),
    ];
    const sorted = sortSubtitlesBySource(tracks);
    expect(sorted.map((t) => t.source)).toEqual([undefined, "external"]);
  });
});

describe("findPreferredSubtitleIndex", () => {
  it("prefers an exact regional tag over generic and other regional tracks", () => {
    const tracks = [
      makeSub({ index: 0, language: "en-GB" }),
      makeSub({ index: 1, language: "en" }),
      makeSub({ index: 2, language: "en-US" }),
    ];
    expect(findPreferredSubtitleIndex(tracks, "en-US")).toBe(2);
  });

  it("falls back to a generic language before another region", () => {
    const tracks = [
      makeSub({ index: 0, language: "en-GB" }),
      makeSub({ index: 1, language: "en" }),
    ];
    expect(findPreferredSubtitleIndex(tracks, "en-US")).toBe(1);
  });

  it.each([
    ["embedded over external", "embedded", "external"],
    ["external over downloaded", "external", "downloaded"],
    ["embedded over downloaded", "embedded", "downloaded"],
  ] as const)("prefers %s for the same language", (_name, preferred, other) => {
    const tracks = [
      makeSub({ index: 0, source: other, language: "en" }),
      makeSub({ index: 1, source: preferred, language: "en" }),
    ];
    expect(findPreferredSubtitleIndex(tracks, "en")).toBe(1);
  });

  it("returns backend index, not array position, when they differ", () => {
    // Simulates bitmap subs being skipped: backend indices have gaps.
    const tracks = [
      makeSub({ index: 2, source: "embedded", language: "en" }),
      makeSub({ index: 4, source: "embedded", language: "es" }),
    ];
    expect(findPreferredSubtitleIndex(tracks, "en")).toBe(2);
    expect(findPreferredSubtitleIndex(tracks, "es")).toBe(4);
  });
});

describe("resolveSubtitleAutoSelect", () => {
  type Options = {
    mode: SubtitleMode;
    tracks?: Partial<PlayerSubtitleInfo>[];
    preferredLanguage?: string | null;
    audioLanguage?: string | null;
    showForcedSubtitles?: boolean;
  };
  function opts(overrides: Options) {
    return {
      mode: overrides.mode,
      tracks: (overrides.tracks ?? []).map((t) => makeSub(t)),
      preferredLanguage: overrides.preferredLanguage ?? null,
      audioLanguage: overrides.audioLanguage ?? null,
      showForcedSubtitles: overrides.showForcedSubtitles ?? true,
    };
  }
  const select = (_name: string, input: Options, expected: number | null) =>
    expect(resolveSubtitleAutoSelect(opts(input))).toBe(expected);

  describe("off mode", () => {
    it.each<[string, Options, number | null]>([
      [
        "returns null when forced subtitles are disabled",
        {
          mode: "off",
          tracks: [{ index: 0, language: "en", source: "external" }],
          preferredLanguage: "en",
          showForcedSubtitles: false,
        },
        null,
      ],
      [
        "selects a forced subtitle matching the audio language when enabled",
        {
          mode: "off",
          tracks: [
            { index: 0, language: "en", forced: true, source: "embedded" },
            { index: 1, language: "ja", forced: true, source: "external" },
          ],
          audioLanguage: "ja",
        },
        1,
      ],
      [
        "selects no forced subtitle when the audio language is unknown",
        {
          mode: "off",
          tracks: [{ index: 0, language: "en", forced: true }],
          audioLanguage: "und",
        },
        null,
      ],
      [
        "reuses source priority among forced tracks",
        {
          mode: "off",
          tracks: [
            { index: 0, language: "en", forced: true, source: "external" },
            { index: 1, language: "en", forced: true, source: "embedded" },
          ],
          audioLanguage: "en",
        },
        1,
      ],
    ])("%s", select);
  });

  describe("always mode", () => {
    it.each<[string, Options, number | null]>([
      [
        "selects preferred language track",
        {
          mode: "always",
          tracks: [
            { index: 0, language: "es", source: "embedded" },
            { index: 1, language: "en", source: "external" },
          ],
          preferredLanguage: "en",
        },
        1,
      ],
      [
        "returns null when no preferred language set",
        { mode: "always", tracks: [{ index: 0, language: "en" }], preferredLanguage: null },
        null,
      ],
      [
        "returns null when preferred language has no match",
        { mode: "always", tracks: [{ index: 0, language: "es" }], preferredLanguage: "en" },
        null,
      ],
      [
        "prefers embedded over external for same language",
        {
          mode: "always",
          tracks: [
            { index: 0, language: "en", source: "external" },
            { index: 1, language: "en", source: "embedded" },
          ],
          preferredLanguage: "en",
        },
        1,
      ],
    ])("%s", select);
  });

  // Auto compares the audio that plays with the subtitle language (Android,
  // Apple, and the server's Jellyfin "Smart" mapping); the profile's audio
  // language plays no part.
  describe("auto mode", () => {
    const englishAndFrench: Partial<PlayerSubtitleInfo>[] = [
      { index: 0, language: "en", source: "embedded" },
      { index: 1, language: "en", forced: true, source: "embedded" },
      { index: 2, language: "fr", source: "embedded" },
      { index: 3, language: "fr", forced: true, source: "embedded" },
    ];
    it.each<[string, Options, number | null]>([
      [
        "shows no subtitles when the audio is in the subtitle language",
        {
          mode: "auto",
          tracks: englishAndFrench,
          preferredLanguage: "en",
          audioLanguage: "en",
          showForcedSubtitles: false,
        },
        null,
      ],
      [
        "shows the forced track when the audio is in the subtitle language and forced is on",
        { mode: "auto", tracks: englishAndFrench, preferredLanguage: "en", audioLanguage: "en" },
        1,
      ],
      [
        "selects full subtitles in the subtitle language for other audio",
        { mode: "auto", tracks: englishAndFrench, preferredLanguage: "fr", audioLanguage: "en" },
        2,
      ],
      [
        "prefers full dialogue over forced for other audio, forced on or off",
        {
          mode: "auto",
          tracks: englishAndFrench,
          preferredLanguage: "fr",
          audioLanguage: "ja",
          showForcedSubtitles: false,
        },
        2,
      ],
      [
        "uses a forced track in the subtitle language only when it is the language's only track",
        {
          mode: "auto",
          tracks: [
            { index: 0, language: "en", forced: true },
            { index: 1, language: "fr", forced: true },
          ],
          preferredLanguage: "fr",
          audioLanguage: "en",
        },
        1,
      ],
      [
        "treats regional variants of the subtitle language as the same language",
        {
          mode: "auto",
          tracks: [{ index: 0, language: "en-GB", source: "embedded" }],
          preferredLanguage: "en-US",
          audioLanguage: "en-GB",
          showForcedSubtitles: false,
        },
        null,
      ],
      [
        "matches the subtitle language's regional track for other audio",
        {
          mode: "auto",
          tracks: [
            { index: 0, language: "pt-PT", source: "embedded" },
            { index: 1, language: "pt-BR", source: "embedded" },
          ],
          preferredLanguage: "pt-BR",
          audioLanguage: "en",
        },
        1,
      ],
      [
        "treats ISO 639-1 and ISO 639-2 codes as the same language",
        {
          mode: "auto",
          tracks: [{ index: 0, language: "en", source: "external" }],
          preferredLanguage: "en",
          audioLanguage: "eng",
          showForcedSubtitles: false,
        },
        null,
      ],
      [
        "selects subtitles when the audio language is unknown",
        { mode: "auto", tracks: englishAndFrench, preferredLanguage: "en", audioLanguage: null },
        0,
      ],
      [
        "treats und audio as unknown",
        { mode: "auto", tracks: englishAndFrench, preferredLanguage: "en", audioLanguage: "und" },
        0,
      ],
      [
        "shows no full subtitles without a subtitle language",
        { mode: "auto", tracks: englishAndFrench, preferredLanguage: null, audioLanguage: "ja" },
        null,
      ],
      [
        "keeps the audio language's forced track without a subtitle language",
        { mode: "auto", tracks: englishAndFrench, preferredLanguage: null, audioLanguage: "fr" },
        3,
      ],
      [
        "shows no forced track without a subtitle language when forced is off",
        {
          mode: "auto",
          tracks: englishAndFrench,
          preferredLanguage: null,
          audioLanguage: "fr",
          showForcedSubtitles: false,
        },
        null,
      ],
      [
        "shows nothing when subtitles were explicitly set to none",
        { mode: "auto", tracks: englishAndFrench, preferredLanguage: "", audioLanguage: "ja" },
        null,
      ],
      [
        "returns null when the subtitle language has no track",
        {
          mode: "auto",
          tracks: [{ index: 0, language: "fr", source: "embedded" }],
          preferredLanguage: "en",
          audioLanguage: "ja",
        },
        null,
      ],
    ])("%s", select);
  });
});

describe("bitmap (PGS) codec deprioritization", () => {
  // Each row is two English tracks; index 1 must win.
  it.each<[string, Partial<PlayerSubtitleInfo>, Partial<PlayerSubtitleInfo>]>([
    [
      "prefers a text track over a PGS track of the same language and source",
      { source: "embedded", codec: "hdmv_pgs_subtitle" },
      { source: "embedded", codec: "subrip" },
    ],
    [
      "prefers an external text track over an embedded PGS track of the same kind",
      { source: "embedded", codec: "hdmv_pgs_subtitle" },
      { source: "external", codec: "srt" },
    ],
    [
      "still ranks PGS as burn-in when the server publishes it burn_in_only",
      { source: "embedded", codec: "pgs", burn_in_only: true },
      { source: "external", codec: "srt", forced: true },
    ],
    [
      "ranks external PGS files as burn-in",
      { source: "external", codec: "hdmv_pgs_subtitle" },
      { source: "external", codec: "srt", forced: true },
    ],
    [
      "prefers an external text track over an embedded track that needs burn-in",
      { source: "embedded", codec: "dvd_subtitle" },
      { source: "external", codec: "srt" },
    ],
    [
      "prefers embedded text over an external PGS file, which can only be burned in",
      { source: "external", codec: "pgs" },
      { source: "embedded", codec: "subrip" },
    ],
    [
      "prefers a full external track over an embedded forced one",
      { source: "embedded", codec: "subrip", forced: true },
      { source: "external", codec: "srt" },
    ],
    [
      "honors the server's burn_in_only flag on a text track",
      { source: "embedded", codec: "subrip", burn_in_only: true },
      { source: "external", codec: "srt" },
    ],
  ])("%s", (_name, first, second) => {
    const tracks = [
      makeSub({ index: 0, language: "en", ...first }),
      makeSub({ index: 1, language: "en", ...second }),
    ];
    expect(findPreferredSubtitleIndex(tracks, "en")).toBe(1);
  });

  it("prefers full-dialogue embedded PGS over a forced or SDH text track", () => {
    const pgs = makeSub({ index: 0, source: "embedded", language: "en", codec: "pgs" });
    const forced = makeSub({
      index: 1,
      source: "external",
      language: "en",
      codec: "srt",
      forced: true,
    });
    const sdh = makeSub({
      index: 2,
      source: "external",
      language: "en",
      codec: "srt",
      hearing_impaired: true,
    });
    expect(findPreferredSubtitleIndex([pgs, forced], "en")).toBe(0);
    expect(findPreferredSubtitleIndex([pgs, sdh], "en")).toBe(0);
  });

  it("selects a PGS track when it is the only language match", () => {
    const tracks = [
      makeSub({ index: 0, source: "embedded", language: "fr", codec: "subrip" }),
      makeSub({ index: 1, source: "embedded", language: "en", codec: "pgs" }),
    ];
    expect(findPreferredSubtitleIndex(tracks, "en")).toBe(1);
  });

  it("auto-selects a forced PGS track when forced display is enabled", () => {
    const tracks = [
      makeSub({ index: 0, source: "embedded", language: "en", codec: "subrip" }),
      makeSub({
        index: 1,
        source: "embedded",
        language: "en",
        codec: "hdmv_pgs_subtitle",
        forced: true,
      }),
    ];
    expect(
      resolveSubtitleAutoSelect({
        mode: "off",
        tracks,
        preferredLanguage: null,
        audioLanguage: "en",
        showForcedSubtitles: true,
      }),
    ).toBe(1);
  });
});
