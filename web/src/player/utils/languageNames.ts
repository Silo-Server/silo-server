import { appLanguage } from "@/lib/appLocale";
import { getLanguageName } from "@/lib/languageNames";

export { canonicalLanguageTag, getLanguageName, normalizeLanguageCode } from "@/lib/languageNames";

const COMMON_LANGUAGE_CODES = [
  "en",
  "es",
  "fr",
  "de",
  "it",
  "pt",
  "pt-BR",
  "pt-PT",
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

let cachedOptions: { language: string; options: LanguageOption[] } | null = null;

/**
 * Sorted common-language list, named and sorted in the interface language.
 * Built per language, so a language change mid-session relabels it.
 */
export function languageOptions(): LanguageOption[] {
  const language = appLanguage();
  if (cachedOptions?.language !== language) {
    const options = COMMON_LANGUAGE_CODES.map((code) => ({
      code,
      label: getLanguageName(code),
    })).sort((a, b) => a.label.localeCompare(b.label));
    cachedOptions = { language, options };
  }
  return cachedOptions.options;
}
