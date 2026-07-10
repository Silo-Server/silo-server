import { describe, expect, it } from "vitest";

import {
  formatAudioTrackSummary,
  formatSubtitleCandidateSummary,
  formatSubtitlePillSummary,
  inferSubtitleFlagsFromTitle,
} from "./prePlaySelection";

describe("formatAudioTrackSummary", () => {
  it("uses language and compact format metadata instead of a container title", () => {
    expect(
      formatAudioTrackSummary({
        language: "en",
        codec: "eac3",
        channels: 6,
        title: "ATSC A/52B (AC-3, E-AC-3)",
      }),
    ).toBe("English · EAC3 · 5.1");
  });
});

describe("formatSubtitleCandidateSummary", () => {
  it("leaves forced and accessibility state to the row badges", () => {
    expect(
      formatSubtitleCandidateSummary({
        languageLabel: "English",
        forced: true,
        hearingImpaired: false,
      }),
    ).toBe("English");
  });
});

describe("inferSubtitleFlagsFromTitle", () => {
  it("recognizes flag-only embedded titles", () => {
    expect(inferSubtitleFlagsFromTitle("Forced")).toEqual({
      forced: true,
      hearingImpaired: false,
      flagOnly: true,
    });
    expect(inferSubtitleFlagsFromTitle("SDH")).toEqual({
      forced: false,
      hearingImpaired: true,
      flagOnly: true,
    });
  });

  it("preserves meaningful regional titles as detail text", () => {
    expect(inferSubtitleFlagsFromTitle("Latin American").flagOnly).toBe(false);
  });
});

describe("formatSubtitlePillSummary", () => {
  it("uses language and a friendly format when the track title repeats the codec", () => {
    expect(
      formatSubtitlePillSummary({
        label: "SUBRIP",
        languageLabel: "English",
        codec: "subrip",
      }),
    ).toBe("English · SRT");
  });

  it("keeps accessibility markers in the compact summary", () => {
    expect(
      formatSubtitlePillSummary({
        label: "SDH",
        languageLabel: "English",
        codec: "subrip",
        hearingImpaired: true,
      }),
    ).toBe("English (SDH) · SRT");
  });

  it("keeps forced markers in the compact summary", () => {
    expect(
      formatSubtitlePillSummary({
        label: "Forced",
        languageLabel: "English",
        codec: "subrip",
        forced: true,
      }),
    ).toBe("English (Forced) · SRT");
  });
});
