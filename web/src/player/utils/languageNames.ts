/** ISO 639-1 (2-letter) language codes → full display name. */
const LANGUAGE_NAMES_2: Record<string, string> = {
  en: "English",
  es: "Spanish",
  fr: "French",
  de: "German",
  it: "Italian",
  pt: "Portuguese",
  nl: "Dutch",
  pl: "Polish",
  ru: "Russian",
  zh: "Chinese",
  ja: "Japanese",
  ko: "Korean",
  ar: "Arabic",
  tr: "Turkish",
  sv: "Swedish",
  da: "Danish",
  no: "Norwegian",
  fi: "Finnish",
  hu: "Hungarian",
  cs: "Czech",
  ro: "Romanian",
  he: "Hebrew",
  th: "Thai",
  vi: "Vietnamese",
  el: "Greek",
  bg: "Bulgarian",
  hr: "Croatian",
  sk: "Slovak",
  sl: "Slovenian",
  uk: "Ukrainian",
  id: "Indonesian",
  ms: "Malay",
  hi: "Hindi",
  ta: "Tamil",
  te: "Telugu",
  bn: "Bengali",
  fa: "Persian",
};

/** ISO 639-2/B (3-letter) language codes → full display name. */
const LANGUAGE_NAMES_3: Record<string, string> = {
  eng: "English",
  spa: "Spanish",
  fre: "French",
  fra: "French",
  ger: "German",
  deu: "German",
  ita: "Italian",
  por: "Portuguese",
  dut: "Dutch",
  nld: "Dutch",
  pol: "Polish",
  rus: "Russian",
  chi: "Chinese",
  zho: "Chinese",
  jpn: "Japanese",
  kor: "Korean",
  ara: "Arabic",
  tur: "Turkish",
  swe: "Swedish",
  dan: "Danish",
  nor: "Norwegian",
  fin: "Finnish",
  hun: "Hungarian",
  cze: "Czech",
  ces: "Czech",
  rum: "Romanian",
  ron: "Romanian",
  heb: "Hebrew",
  tha: "Thai",
  vie: "Vietnamese",
  gre: "Greek",
  ell: "Greek",
  bul: "Bulgarian",
  hrv: "Croatian",
  slo: "Slovak",
  slk: "Slovak",
  slv: "Slovenian",
  ukr: "Ukrainian",
  ind: "Indonesian",
  may: "Malay",
  msa: "Malay",
  hin: "Hindi",
  tam: "Tamil",
  tel: "Telugu",
  ben: "Bengali",
  per: "Persian",
  fas: "Persian",
};

const LANGUAGE_CODE_ALIASES: Record<string, string> = {
  eng: "en",
  spa: "es",
  fre: "fr",
  fra: "fr",
  ger: "de",
  deu: "de",
  ita: "it",
  por: "pt",
  dut: "nl",
  nld: "nl",
  pol: "pl",
  rus: "ru",
  chi: "zh",
  zho: "zh",
  jpn: "ja",
  kor: "ko",
  ara: "ar",
  tur: "tr",
  swe: "sv",
  dan: "da",
  nor: "no",
  fin: "fi",
  hun: "hu",
  cze: "cs",
  ces: "cs",
  rum: "ro",
  ron: "ro",
  heb: "he",
  tha: "th",
  vie: "vi",
  gre: "el",
  ell: "el",
  bul: "bg",
  hrv: "hr",
  slo: "sk",
  slk: "sk",
  slv: "sl",
  ukr: "uk",
  ind: "id",
  may: "ms",
  msa: "ms",
  hin: "hi",
  tam: "ta",
  tel: "te",
  ben: "bn",
  per: "fa",
  fas: "fa",
};

/** Combined lookup: supports both 2-letter and 3-letter codes. */
const LANGUAGE_NAMES: Record<string, string> = {
  ...LANGUAGE_NAMES_2,
  ...LANGUAGE_NAMES_3,
};

export function normalizeLanguageCode(code: string | null | undefined): string {
  const normalized = (code ?? "").trim().toLowerCase();
  if (!normalized) return "";
  const separator = normalized.search(/[-_]/);
  const base = separator >= 0 ? normalized.slice(0, separator) : normalized;
  return LANGUAGE_CODE_ALIASES[base] ?? base;
}

/**
 * Returns the full language name for an ISO 639-1 or 639-2 code.
 * Falls back to the code itself (uppercased first letter) if unknown.
 */
export function getLanguageName(code: string): string {
  if (!code) return "Unknown";
  const lower = code.toLowerCase();
  const normalized = normalizeLanguageCode(code);
  return (
    LANGUAGE_NAMES_2[normalized] ??
    LANGUAGE_NAMES[lower] ??
    code.charAt(0).toUpperCase() + code.slice(1)
  );
}

/** Language option for dropdowns (search modal, etc). */
export interface LanguageOption {
  code: string;
  label: string;
}

/** Sorted list of languages for UI dropdowns (uses 2-letter codes for API compatibility). */
export const LANGUAGES: LanguageOption[] = Object.entries(LANGUAGE_NAMES_2)
  .map(([code, label]) => ({ code, label }))
  .sort((a, b) => a.label.localeCompare(b.label));
