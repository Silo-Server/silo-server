/**
 * Choices for request routing conditions that TMDB describes with standard
 * codes: a title's original language (ISO 639-1) and its origin countries
 * (ISO 3166-1). These are the common ones, not the full standards; a rule
 * saved through the API with another code still shows it by code.
 */
export interface CodeOption {
  code: string;
  name: string;
}

export const ROUTING_LANGUAGES: readonly CodeOption[] = [
  { code: "en", name: "English" },
  { code: "ja", name: "Japanese" },
  { code: "ko", name: "Korean" },
  { code: "zh", name: "Chinese" },
  // TMDB's own code for Cantonese; it is not ISO 639-1, but it is what TMDB
  // reports as the original language of most Hong Kong films.
  { code: "cn", name: "Cantonese" },
  { code: "fr", name: "French" },
  { code: "de", name: "German" },
  { code: "es", name: "Spanish" },
  { code: "it", name: "Italian" },
  { code: "pt", name: "Portuguese" },
  { code: "ru", name: "Russian" },
  { code: "hi", name: "Hindi" },
  { code: "ta", name: "Tamil" },
  { code: "te", name: "Telugu" },
  { code: "ml", name: "Malayalam" },
  { code: "th", name: "Thai" },
  { code: "id", name: "Indonesian" },
  { code: "tl", name: "Tagalog" },
  { code: "vi", name: "Vietnamese" },
  { code: "tr", name: "Turkish" },
  { code: "ar", name: "Arabic" },
  { code: "he", name: "Hebrew" },
  { code: "fa", name: "Persian" },
  { code: "nl", name: "Dutch" },
  { code: "sv", name: "Swedish" },
  { code: "da", name: "Danish" },
  { code: "no", name: "Norwegian" },
  { code: "fi", name: "Finnish" },
  { code: "is", name: "Icelandic" },
  { code: "pl", name: "Polish" },
  { code: "cs", name: "Czech" },
  { code: "hu", name: "Hungarian" },
  { code: "ro", name: "Romanian" },
  { code: "el", name: "Greek" },
  { code: "uk", name: "Ukrainian" },
];

export const ROUTING_COUNTRIES: readonly CodeOption[] = [
  { code: "US", name: "United States" },
  { code: "GB", name: "United Kingdom" },
  { code: "CA", name: "Canada" },
  { code: "AU", name: "Australia" },
  { code: "NZ", name: "New Zealand" },
  { code: "IE", name: "Ireland" },
  { code: "JP", name: "Japan" },
  { code: "KR", name: "South Korea" },
  { code: "CN", name: "China" },
  { code: "HK", name: "Hong Kong" },
  { code: "TW", name: "Taiwan" },
  { code: "IN", name: "India" },
  { code: "TH", name: "Thailand" },
  { code: "ID", name: "Indonesia" },
  { code: "PH", name: "Philippines" },
  { code: "FR", name: "France" },
  { code: "DE", name: "Germany" },
  { code: "ES", name: "Spain" },
  { code: "IT", name: "Italy" },
  { code: "PT", name: "Portugal" },
  { code: "NL", name: "Netherlands" },
  { code: "BE", name: "Belgium" },
  { code: "CH", name: "Switzerland" },
  { code: "AT", name: "Austria" },
  { code: "SE", name: "Sweden" },
  { code: "NO", name: "Norway" },
  { code: "DK", name: "Denmark" },
  { code: "FI", name: "Finland" },
  { code: "IS", name: "Iceland" },
  { code: "PL", name: "Poland" },
  { code: "CZ", name: "Czechia" },
  { code: "HU", name: "Hungary" },
  { code: "GR", name: "Greece" },
  { code: "RU", name: "Russia" },
  { code: "UA", name: "Ukraine" },
  { code: "TR", name: "Turkey" },
  { code: "IL", name: "Israel" },
  { code: "IR", name: "Iran" },
  { code: "EG", name: "Egypt" },
  { code: "NG", name: "Nigeria" },
  { code: "ZA", name: "South Africa" },
  { code: "BR", name: "Brazil" },
  { code: "MX", name: "Mexico" },
  { code: "AR", name: "Argentina" },
];

/** Decade shortcuts for a rule's release-year range. */
export const ROUTING_DECADES: readonly { label: string; from: number; to: number }[] = [
  1970, 1980, 1990, 2000, 2010, 2020,
].map((from) => ({ label: `${from}s`, from, to: from + 9 }));

function nameOf(options: readonly CodeOption[], code: string): string {
  return options.find((option) => option.code === code)?.name ?? code;
}

export function routingLanguageName(code: string): string {
  return nameOf(ROUTING_LANGUAGES, code.toLowerCase());
}

export function routingCountryName(code: string): string {
  return nameOf(ROUTING_COUNTRIES, code.toUpperCase());
}
