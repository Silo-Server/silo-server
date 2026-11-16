// @vitest-environment node

import { describe, expect, it } from "vitest";

import { SETTING_KEYS } from "./settingsContract";
import { namedLanguageOptionsFor, withCurrentLanguageOption } from "./languageOptions";

describe("languageOptions", () => {
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

  it("offers every shipped app language, not just the contract floor", () => {
    const values = namedLanguageOptionsFor(SETTING_KEYS.UI_APP_LANGUAGE).map(
      (option) => option.value,
    );

    expect(values).toEqual(expect.arrayContaining(["en", "nl", "ko", "zh-Hant"]));
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
