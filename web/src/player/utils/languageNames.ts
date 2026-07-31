import { getLanguageName } from "@/lib/languageNames";

export { getLanguageName, normalizeLanguageCode } from "@/lib/languageNames";

const COMMON_LANGUAGE_CODES = [
  "en",
  "es",
  "fr",
  "de",
  "it",
  "pt",
  "nl",
  "pl",
  "ru",
  "zh",
  "ja",
  "ko",
  "ar",
  "tr",
  "sv",
  "da",
  "no",
  "fi",
  "hu",
  "cs",
  "ro",
  "he",
  "th",
  "vi",
  "el",
  "bg",
  "hr",
  "sk",
  "sl",
  "uk",
  "id",
  "ms",
  "hi",
  "ta",
  "te",
  "bn",
  "fa",
] as const;

/** Language option for dropdowns (search modal, etc). */
export interface LanguageOption {
  code: string;
  label: string;
}

/** Sorted common-language list; labels come from the shared English CLDR resolver. */
export const LANGUAGES: LanguageOption[] = COMMON_LANGUAGE_CODES.map((code) => ({
  code,
  label: getLanguageName(code),
})).sort((a, b) => a.label.localeCompare(b.label));
