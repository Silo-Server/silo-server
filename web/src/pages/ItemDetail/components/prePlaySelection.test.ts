import { describe, expect, it } from "vitest";

import { formatSubtitlePillSummary } from "./prePlaySelection";

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
});
