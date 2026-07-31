import { describe, expect, it } from "vitest";

import {
  canonicalLanguageTag,
  englishLanguageName,
  getLanguageName,
  normalizeLanguageCode,
} from "./languageNames";

describe("languageNames", () => {
  it("uses bundled English CLDR names for ISO 639-1 and ISO 639-3 values", () => {
    expect(englishLanguageName("sa")).toBe("Sanskrit");
    expect(englishLanguageName("se")).toBe("Northern Sami");
    expect(englishLanguageName("sm")).toBe("Samoan");
    expect(englishLanguageName("yue")).toBe("Cantonese");
  });

  it("preserves region and script specificity in names", () => {
    expect(englishLanguageName("pt-BR")).toBe("Brazilian Portuguese");
    expect(englishLanguageName("sr-Latn")).toBe("Serbian (Latin)");
  });

  it("canonicalizes ISO aliases without changing regional identity", () => {
    expect(canonicalLanguageTag("eng")).toBe("en");
    expect(canonicalLanguageTag("pt_BR")).toBe("pt-BR");
    expect(normalizeLanguageCode("fre-CA")).toBe("fr");
  });

  it("distinguishes an unassigned tag from a translated language name", () => {
    expect(englishLanguageName("xx")).toBeNull();
    expect(getLanguageName("xx")).toBe("Unknown language (xx)");
  });
});
