// @vitest-environment node

import { afterEach, describe, expect, it } from "vitest";

import { resolveAppLocale, setAppLocale } from "./appLocale";
import {
  canonicalLanguageTag,
  canonicalLanguageWireValue,
  displayLanguageName,
  getLanguageName,
  loadLanguageNames,
  normalizeLanguageCode,
} from "./languageNames";

function applyLanguage(stored: string) {
  setAppLocale(
    resolveAppLocale({
      stored,
      navigatorLanguages: [],
      runtimeLocale: "en-US",
      hasCatalog: () => true,
    }),
  );
}

describe("languageNames", () => {
  afterEach(() => applyLanguage("en"));

  it("uses bundled English CLDR names for ISO 639-1 and ISO 639-3 values", () => {
    expect(displayLanguageName("sa")).toBe("Sanskrit");
    expect(displayLanguageName("se")).toBe("Northern Sami");
    expect(displayLanguageName("sm")).toBe("Samoan");
    expect(displayLanguageName("yue")).toBe("Cantonese");
  });

  it("preserves region and script specificity in names", () => {
    expect(displayLanguageName("pt-BR")).toBe("Brazilian Portuguese");
    expect(displayLanguageName("sr-Latn")).toBe("Serbian (Latin)");
  });

  it("canonicalizes ISO aliases without changing regional identity", () => {
    expect(canonicalLanguageTag("eng")).toBe("en");
    expect(canonicalLanguageTag("pt_BR")).toBe("pt-BR");
    expect(normalizeLanguageCode("fre-CA")).toBe("fr");
    expect(canonicalLanguageWireValue("Arabic")).toBe("ar");
    expect(canonicalLanguageWireValue("zh-Hant")).toBe("zh-Hant");
    expect(canonicalLanguageWireValue("unknown")).toBeNull();
  });

  it("distinguishes an unassigned tag from a translated language name", () => {
    expect(displayLanguageName("xx")).toBeNull();
    expect(getLanguageName("xx")).toBe("Unknown language (xx)");
    expect(getLanguageName("")).toBe("Unknown");
    expect(getLanguageName("ENG")).toBe("English");
  });

  it("names languages in the interface language once its data has loaded", async () => {
    applyLanguage("pt-BR");
    expect(displayLanguageName("de")).toBe("German");
    await loadLanguageNames("pt-BR");
    expect(displayLanguageName("de")).toBe("alemão");

    applyLanguage("zh-Hans");
    await loadLanguageNames("zh-Hans");
    expect(displayLanguageName("fr")).toBe("法语");
  });
});
