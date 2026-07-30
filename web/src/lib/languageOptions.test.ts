import { describe, expect, it } from "vitest";

import { SETTING_KEYS } from "./settingsContract";
import { languageOptionsFor, namedLanguageOptionsFor } from "./languageOptions";

describe("languageOptions", () => {
  it("uses the definition-specific generated option set", () => {
    const options = namedLanguageOptionsFor(SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE);

    expect(options.map((option) => option.value)).toContain("en");
    expect(options.map((option) => option.value)).toContain("te");
    expect(options.some((option) => option.value === "")).toBe(false);
  });

  it("merges deployment values and preserves an exact current regional tag", () => {
    const options = namedLanguageOptionsFor(SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE, "pt-BR", [
      "eng",
      "pt",
      "es-MX",
    ]);

    expect(options.map((option) => option.value)).toContain("en");
    expect(options.map((option) => option.value)).not.toContain("eng");
    expect(options.map((option) => option.value)).toContain("es-MX");
    expect(options.map((option) => option.value)).toContain("pt-BR");
    expect(options.map((option) => option.value)).toContain("pt");
  });

  it("uses each nullable definition's context-specific unset copy", () => {
    expect(languageOptionsFor(SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE)[0]).toEqual({
      value: "",
      label: "No preference",
    });
    expect(languageOptionsFor(SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE)[0]).toEqual({
      value: "",
      label: "None",
    });
    expect(languageOptionsFor(SETTING_KEYS.CATALOG_METADATA_LANGUAGE)[0]).toEqual({
      value: "",
      label: "Library default",
    });
  });
});
