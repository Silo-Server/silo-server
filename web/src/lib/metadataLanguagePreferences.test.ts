import { describe, expect, it } from "vitest";

import {
  ORIGINAL_METADATA_LANGUAGE,
  normalizeMetadataLanguageOverrides,
  withMetadataLanguageOverride,
  withoutMetadataLanguageOverride,
} from "./metadataLanguagePreferences";

describe("metadata language preferences", () => {
  it("normalizes source aliases and drops malformed cached entries", () => {
    expect(
      normalizeMetadataLanguageOverrides({
        nor: ORIGINAL_METADATA_LANGUAGE,
        JA: "en",
        invalid_language: "fr",
        de: 42,
      }),
    ).toEqual({ no: ORIGINAL_METADATA_LANGUAGE, ja: "en" });
  });

  it("adds and removes one original-language exception without mutating the input", () => {
    const initial = { ja: "en" };
    const added = withMetadataLanguageOverride(initial, "nor", ORIGINAL_METADATA_LANGUAGE);

    expect(added).toEqual({ ja: "en", no: ORIGINAL_METADATA_LANGUAGE });
    expect(initial).toEqual({ ja: "en" });
    expect(withoutMetadataLanguageOverride(added, "no")).toEqual({ ja: "en" });
  });
});
