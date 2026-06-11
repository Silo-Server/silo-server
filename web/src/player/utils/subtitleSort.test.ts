import { describe, expect, it } from "vitest";
import { getLanguageName } from "./languageNames";
import {
  sortSubtitlesBySource,
  findPreferredSubtitleIndex,
  resolveSubtitleAutoSelect,
} from "./subtitleSort";
import type { PlayerSubtitleInfo, SubtitleMode } from "../types";

function makeSub(overrides: Partial<PlayerSubtitleInfo>): PlayerSubtitleInfo {
  return {
    index: 0,
    language: "en",
    label: "English",
    url: "/test",
    ...overrides,
  };
}

describe("getLanguageName", () => {
  it("returns full name for 2-letter codes", () => {
    expect(getLanguageName("en")).toBe("English");
    expect(getLanguageName("ja")).toBe("Japanese");
  });

  it("returns full name for 3-letter codes", () => {
    expect(getLanguageName("eng")).toBe("English");
    expect(getLanguageName("spa")).toBe("Spanish");
    expect(getLanguageName("jpn")).toBe("Japanese");
    expect(getLanguageName("fre")).toBe("French");
    expect(getLanguageName("fra")).toBe("French");
  });

  it("is case-insensitive", () => {
    expect(getLanguageName("EN")).toBe("English");
    expect(getLanguageName("ENG")).toBe("English");
  });

  it("falls back to capitalized code for unknown", () => {
    expect(getLanguageName("xx")).toBe("Xx");
  });

  it("returns 'Unknown' for empty string", () => {
    expect(getLanguageName("")).toBe("Unknown");
  });
});

describe("sortSubtitlesBySource", () => {
  it("sorts external before downloaded before embedded", () => {
    const tracks = [
      makeSub({ index: 0, source: "embedded" }),
      makeSub({ index: 1, source: "downloaded" }),
      makeSub({ index: 2, source: "external" }),
    ];
    const sorted = sortSubtitlesBySource(tracks);
    expect(sorted.map((t) => t.source)).toEqual(["external", "downloaded", "embedded"]);
  });

  it("preserves relative order within same source", () => {
    const tracks = [
      makeSub({ index: 0, source: "external", language: "en" }),
      makeSub({ index: 1, source: "external", language: "es" }),
      makeSub({ index: 2, source: "embedded", language: "fr" }),
    ];
    const sorted = sortSubtitlesBySource(tracks);
    expect(sorted.map((t) => t.language)).toEqual(["en", "es", "fr"]);
  });

  it("treats missing source as embedded", () => {
    const tracks = [
      makeSub({ index: 0, source: undefined }),
      makeSub({ index: 1, source: "external" }),
    ];
    const sorted = sortSubtitlesBySource(tracks);
    expect(sorted.map((t) => t.source)).toEqual(["external", undefined]);
  });

  it("does not mutate the original array", () => {
    const tracks = [
      makeSub({ index: 0, source: "embedded" }),
      makeSub({ index: 1, source: "external" }),
    ];
    const original = [...tracks];
    sortSubtitlesBySource(tracks);
    expect(tracks).toEqual(original);
  });
});

describe("findPreferredSubtitleIndex", () => {
  it("prefers external over embedded for same language", () => {
    const tracks = [
      makeSub({ index: 0, source: "embedded", language: "en" }),
      makeSub({ index: 1, source: "external", language: "en" }),
    ];
    expect(findPreferredSubtitleIndex(tracks, "en")).toBe(1);
  });

  it("prefers external over downloaded", () => {
    const tracks = [
      makeSub({ index: 0, source: "downloaded", language: "en" }),
      makeSub({ index: 1, source: "external", language: "en" }),
    ];
    expect(findPreferredSubtitleIndex(tracks, "en")).toBe(1);
  });

  it("prefers downloaded over embedded", () => {
    const tracks = [
      makeSub({ index: 0, source: "embedded", language: "en" }),
      makeSub({ index: 1, source: "downloaded", language: "en" }),
    ];
    expect(findPreferredSubtitleIndex(tracks, "en")).toBe(1);
  });

  it("returns -1 when no language match", () => {
    const tracks = [makeSub({ index: 0, source: "external", language: "es" })];
    expect(findPreferredSubtitleIndex(tracks, "en")).toBe(-1);
  });

  it("returns the only match when there is one", () => {
    const tracks = [
      makeSub({ index: 0, source: "embedded", language: "en" }),
      makeSub({ index: 1, source: "embedded", language: "es" }),
    ];
    expect(findPreferredSubtitleIndex(tracks, "en")).toBe(0);
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
  function opts(overrides: {
    mode: SubtitleMode;
    tracks?: Partial<PlayerSubtitleInfo>[];
    preferredLanguage?: string | null;
    audioLanguage?: string | null;
    profileLanguage?: string | null;
    showForcedSubtitles?: boolean;
  }) {
    return {
      mode: overrides.mode,
      tracks: (overrides.tracks ?? []).map((t) => makeSub(t)),
      preferredLanguage: overrides.preferredLanguage ?? null,
      audioLanguage: overrides.audioLanguage ?? null,
      profileLanguage: overrides.profileLanguage ?? null,
      showForcedSubtitles: overrides.showForcedSubtitles ?? true,
    };
  }

  describe("off mode", () => {
    it("returns null when forced subtitles are disabled", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "off",
            tracks: [{ index: 0, language: "en", source: "external" }],
            preferredLanguage: "en",
            showForcedSubtitles: false,
          }),
        ),
      ).toBeNull();
    });

    it("selects a forced subtitle matching the audio language when enabled", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "off",
            tracks: [
              { index: 0, language: "en", forced: true, source: "embedded" },
              { index: 1, language: "ja", forced: true, source: "external" },
            ],
            audioLanguage: "ja",
            profileLanguage: "en",
          }),
        ),
      ).toBe(1);
    });

    it("returns null with empty tracks", () => {
      expect(resolveSubtitleAutoSelect(opts({ mode: "off" }))).toBeNull();
    });
  });

  describe("always mode", () => {
    it("selects preferred language track", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "always",
            tracks: [
              { index: 0, language: "es", source: "embedded" },
              { index: 1, language: "en", source: "external" },
            ],
            preferredLanguage: "en",
          }),
        ),
      ).toBe(1);
    });

    it("returns null when no preferred language set", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "always",
            tracks: [{ index: 0, language: "en" }],
            preferredLanguage: null,
          }),
        ),
      ).toBeNull();
    });

    it("returns null when preferred language has no match", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "always",
            tracks: [{ index: 0, language: "es" }],
            preferredLanguage: "en",
          }),
        ),
      ).toBeNull();
    });

    it("prefers external over embedded for same language", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "always",
            tracks: [
              { index: 0, language: "en", source: "embedded" },
              { index: 1, language: "en", source: "external" },
            ],
            preferredLanguage: "en",
          }),
        ),
      ).toBe(1);
    });
  });

  describe("auto mode", () => {
    it("returns null when audio matches profile language and forced subtitles are disabled", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "auto",
            tracks: [{ index: 0, language: "en", source: "external" }],
            preferredLanguage: "en",
            audioLanguage: "en",
            profileLanguage: "en",
            showForcedSubtitles: false,
          }),
        ),
      ).toBeNull();
    });

    it("selects a forced subtitle in the audio language when audio matches the profile language", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "auto",
            tracks: [
              { index: 0, language: "en", forced: true, source: "embedded" },
              { index: 1, language: "en", forced: true, source: "external" },
            ],
            preferredLanguage: "fr",
            audioLanguage: "en",
            profileLanguage: "en",
          }),
        ),
      ).toBe(1);
    });

    it("selects subtitle when audio differs from profile language", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "auto",
            tracks: [
              { index: 0, language: "en", source: "embedded" },
              { index: 1, language: "ja", source: "embedded" },
            ],
            preferredLanguage: "en",
            audioLanguage: "ja",
            profileLanguage: "en",
          }),
        ),
      ).toBe(0);
    });

    it("selects preferred subtitles when spoken language is original and audio is non-English", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "auto",
            tracks: [{ index: 0, language: "en", source: "external" }],
            preferredLanguage: "en",
            audioLanguage: "ja",
            profileLanguage: "original",
            showForcedSubtitles: false,
          }),
        ),
      ).toBe(0);
    });

    it("does not auto-enable subtitles when spoken language is original and audio matches the subtitle language", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "auto",
            tracks: [{ index: 0, language: "en", source: "external" }],
            preferredLanguage: "en",
            audioLanguage: "en",
            profileLanguage: "original",
            showForcedSubtitles: false,
          }),
        ),
      ).toBeNull();
    });

    it("falls back to profile language when no preferred subtitle language", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "auto",
            tracks: [{ index: 0, language: "en", source: "embedded" }],
            preferredLanguage: null,
            audioLanguage: "ja",
            profileLanguage: "en",
          }),
        ),
      ).toBe(0);
    });

    it("does not fall back to the profile language when subtitles were explicitly set to none", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "auto",
            tracks: [{ index: 0, language: "en", source: "embedded" }],
            preferredLanguage: "",
            audioLanguage: "ja",
            profileLanguage: "en",
          }),
        ),
      ).toBeNull();
    });

    it("uses the profile language as the audio-language fallback for forced subtitles", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "auto",
            tracks: [{ index: 0, language: "en", forced: true }],
            preferredLanguage: "en",
            audioLanguage: null,
            profileLanguage: "en",
          }),
        ),
      ).toBe(0);
    });

    it("defaults to 'en' when profileLanguage is null", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "auto",
            tracks: [{ index: 0, language: "en", source: "embedded" }],
            preferredLanguage: "en",
            audioLanguage: "ja",
            profileLanguage: null,
          }),
        ),
      ).toBe(0);
    });

    it("treats ISO 639-1 and ISO 639-2 codes as the same language for auto suppression", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "auto",
            tracks: [{ index: 0, language: "en", source: "external" }],
            preferredLanguage: "en",
            audioLanguage: "eng",
            profileLanguage: "en",
            showForcedSubtitles: false,
          }),
        ),
      ).toBeNull();
    });

    it("returns null when preferred language has no matching tracks", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "auto",
            tracks: [{ index: 0, language: "fr", source: "embedded" }],
            preferredLanguage: "en",
            audioLanguage: "ja",
            profileLanguage: "en",
          }),
        ),
      ).toBeNull();
    });

    it("returns null when no forced subtitle matches the audio language", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "auto",
            tracks: [
              { index: 0, language: "fr", forced: true },
              { index: 1, language: "es", forced: true },
            ],
            preferredLanguage: "en",
            audioLanguage: "ja",
            profileLanguage: "ja",
          }),
        ),
      ).toBeNull();
    });

    it("does not fall back to forced subtitles in the preferred subtitle language", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "auto",
            tracks: [
              { index: 0, language: "en", forced: true },
              { index: 1, language: "fr", forced: true },
            ],
            preferredLanguage: "fr",
            audioLanguage: "en",
            profileLanguage: "en",
          }),
        ),
      ).toBe(0);
    });

    it("reuses source priority among forced tracks", () => {
      expect(
        resolveSubtitleAutoSelect(
          opts({
            mode: "off",
            tracks: [
              { index: 0, language: "en", forced: true, source: "embedded" },
              { index: 1, language: "en", forced: true, source: "external" },
            ],
            audioLanguage: "en",
            profileLanguage: "en",
          }),
        ),
      ).toBe(1);
    });
  });

  describe("edge cases", () => {
    it("returns null for empty tracks regardless of mode", () => {
      for (const mode of ["off", "auto", "always"] as SubtitleMode[]) {
        expect(resolveSubtitleAutoSelect(opts({ mode }))).toBeNull();
      }
    });
  });
});

describe("bitmap (PGS) codec deprioritization", () => {
  it("prefers a text track over a PGS track of the same language and source", () => {
    const tracks = [
      makeSub({ index: 0, source: "embedded", language: "en", codec: "hdmv_pgs_subtitle" }),
      makeSub({ index: 1, source: "embedded", language: "en", codec: "subrip" }),
    ];
    expect(findPreferredSubtitleIndex(tracks, "en")).toBe(1);
  });

  it("still prefers a better source even when it is bitmap-free elsewhere", () => {
    // External text beats embedded PGS, and embedded text beats embedded PGS,
    // but an external PGS-like entry would still beat embedded text — source
    // remains the primary key.
    const tracks = [
      makeSub({ index: 0, source: "embedded", language: "en", codec: "subrip" }),
      makeSub({ index: 1, source: "external", language: "en", codec: "srt" }),
    ];
    expect(findPreferredSubtitleIndex(tracks, "en")).toBe(1);
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
        profileLanguage: "en",
        showForcedSubtitles: true,
      }),
    ).toBe(1);
  });
});
