// @vitest-environment node

import { describe, expect, it } from "vitest";

import { matchShippedLanguage, resolveAppLocale } from "./appLocale";

describe("matchShippedLanguage", () => {
  it.each([
    ["de", "de"],
    ["es-MX", "es"],
    ["es-419", "es"],
    ["en-GB", "en"],
    ["pt", "pt-BR"],
    ["pt-PT", "pt-BR"],
    ["pt_br", "pt-BR"],
    ["zh", "zh-Hans"],
    ["zh-CN", "zh-Hans"],
    ["zh-SG", "zh-Hans"],
    ["zh-Hans-HK", "zh-Hans"],
    ["zh-TW", null],
    ["zh-Hant", null],
    ["ar-EG", "ar"],
    ["it", null],
    ["not a tag", null],
    ["", null],
  ])("%s selects %s", (tag, expected) => {
    expect(matchShippedLanguage(tag)).toBe(expected);
  });
});

describe("resolveAppLocale", () => {
  const withCatalogs =
    (...languages: string[]) =>
    (language: string) =>
      languages.includes(language);
  const browser = (
    navigatorLanguages: string[],
    runtimeLocale = "en-US",
    hasCatalog = withCatalogs("es", "de", "fr", "pt-BR", "ja", "zh-Hans", "ar"),
    pseudoLocales = false,
  ) => ({ navigatorLanguages, runtimeLocale, hasCatalog, pseudoLocales });
  const pseudo = { language: "en", formatLocale: undefined, localizedFormats: false };

  it.each([
    {
      name: "a stored shipped language wins over the browser",
      stored: "de",
      ...browser(["fr-FR", "en"]),
      expected: { language: "de", formatLocale: "de", localizedFormats: true, dir: "ltr" },
    },
    {
      name: "a stored regional tag selects its language and formats in its region",
      stored: "es-MX",
      ...browser(["en-US"]),
      expected: { language: "es", formatLocale: "es-MX", localizedFormats: true, dir: "ltr" },
    },
    {
      name: "null follows the first browser language that ships",
      stored: null,
      ...browser(["it-IT", "fr-CA", "de"], "it-IT"),
      expected: { language: "fr", formatLocale: undefined, localizedFormats: false, dir: "ltr" },
    },
    {
      name: "a stored tag the web does not ship falls through to the browser",
      stored: "it",
      ...browser(["ja-JP"]),
      expected: { language: "ja", formatLocale: undefined, localizedFormats: false, dir: "ltr" },
    },
    {
      name: "a value that is not a tag falls through",
      stored: 42,
      ...browser(["de-AT"]),
      expected: { language: "de", formatLocale: undefined, localizedFormats: false, dir: "ltr" },
    },
    {
      name: "nothing that ships falls back to English, formatted as the browser formats",
      stored: null,
      ...browser(["it-IT"], "it-IT"),
      expected: { language: "en", formatLocale: undefined, localizedFormats: false, dir: "ltr" },
    },
    {
      name: "choosing English on a British browser keeps British formatting",
      stored: "en",
      ...browser(["en-GB"], "en-GB"),
      expected: { language: "en", formatLocale: undefined, localizedFormats: false, dir: "ltr" },
    },
    {
      name: "choosing English on a German browser formats in English",
      stored: "en",
      ...browser(["de-DE"], "de-DE"),
      expected: { language: "en", formatLocale: "en", localizedFormats: false, dir: "ltr" },
    },
    {
      name: "a browser language without a choice keeps the browser's formatting",
      stored: null,
      ...browser(["de-DE"], "en-US"),
      expected: { language: "de", formatLocale: undefined, localizedFormats: false, dir: "ltr" },
    },
    {
      name: "a browser language without a catalog keeps the English interface",
      stored: null,
      ...browser(["de-DE", "ar"], "de-DE", withCatalogs()),
      expected: { language: "en", formatLocale: undefined, localizedFormats: false, dir: "ltr" },
    },
    {
      name: "a chosen language applies without a catalog",
      stored: "de",
      ...browser(["en-US"], "en-US", withCatalogs()),
      expected: { language: "de", formatLocale: "de", localizedFormats: true, dir: "ltr" },
    },
    {
      name: "choosing the browser's own language formats as the browser does",
      stored: "de",
      ...browser(["de-DE"], "de-DE"),
      expected: { language: "de", formatLocale: undefined, localizedFormats: true, dir: "ltr" },
    },
    {
      name: "Arabic is right to left",
      stored: "ar",
      ...browser(["en-US"]),
      expected: { language: "ar", formatLocale: "ar", localizedFormats: true, dir: "rtl" },
    },
    {
      name: "a stored pseudo-locale applies in a build that carries them",
      stored: "en-XA",
      ...browser(["de-DE"], "de-DE", withCatalogs(), true),
      expected: { ...pseudo, catalog: "en-XA", dir: "ltr" },
    },
    {
      name: "the right-to-left pseudo-locale mirrors",
      stored: "ar-xb",
      ...browser(["en-US"], "en-US", withCatalogs(), true),
      expected: { ...pseudo, catalog: "ar-XB", dir: "rtl" },
    },
    {
      name: "without pseudo-locales built in, en-XA is plain English",
      stored: "en-XA",
      ...browser(["en-US"], "en-US", withCatalogs(), false),
      expected: { ...pseudo, catalog: "en", dir: "ltr" },
    },
  ])("$name", ({ stored, expected, ...inputs }) => {
    expect(resolveAppLocale({ stored, ...inputs })).toEqual({
      catalog: expected.language,
      ...expected,
    });
  });
});
