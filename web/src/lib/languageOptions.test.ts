import { describe, expect, it } from "vitest";

import { SETTING_KEYS } from "./settingsContract";
import {
  languageOptionsFor,
  namedLanguageOptionsFor,
  withCurrentLanguageOption,
} from "./languageOptions";

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

  it("uses English names for ISO and BCP 47 values instead of exposing raw tags", () => {
    const options = namedLanguageOptionsFor(SETTING_KEYS.CATALOG_METADATA_LANGUAGE, undefined, [
      "sa",
      "se",
      "yue",
      "pt-BR",
      "sr-Latn",
      "xx",
    ]);
    const labels = Object.fromEntries(options.map((option) => [option.value, option.label]));

    expect(labels.sa).toBe("Sanskrit");
    expect(labels.se).toBe("Northern Sami");
    expect(labels.yue).toBe("Cantonese");
    expect(labels["pt-BR"]).toBe("Brazilian Portuguese");
    expect(labels["sr-Latn"]).toBe("Serbian (Latin)");
    expect(labels.xx).toBe("Unknown language (xx)");
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

  it("keeps an exact current ISO alias without showing its semantic duplicate", () => {
    const options = withCurrentLanguageOption(
      [
        { value: "en", label: "English" },
        { value: "fr", label: "French" },
      ],
      "eng",
    );

    expect(options).toEqual([
      { value: "eng", label: "English" },
      { value: "fr", label: "French" },
    ]);
  });
});
