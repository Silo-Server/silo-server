// Language-tag parsing needs only Intl.Locale, so it lives apart from the
// display-name data in languageNames.ts. Code on the launch path (subtitle
// auto-selection in the playback chrome) can then use it without loading the
// DisplayNames polyfill.

/** Canonical BCP 47 identity used only for comparison; wire values stay untouched. */
export function canonicalLanguageTag(value: string): string | null {
  const aliases: Record<string, string> = {
    arabic: "ar",
    english: "en",
    spanish: "es",
    french: "fr",
    german: "de",
    portuguese: "pt",
    "brazilian portuguese": "pt-BR",
    "portuguese (brazil)": "pt-BR",
    "chinese (traditional)": "zh-Hant",
    "traditional chinese": "zh-Hant",
  };
  const trimmed = (aliases[value.trim().toLowerCase()] ?? value).trim();
  if (!trimmed) return null;
  try {
    const locale = new Intl.Locale(trimmed.replace(/_/g, "-"));
    if (!/^[a-z]{2,3}$/i.test(locale.language)) return null;
    return locale.toString();
  } catch {
    return null;
  }
}

/** Canonical value for API request bodies; invalid values stay absent. */
export function canonicalLanguageWireValue(value: string | null | undefined): string | null {
  return value == null ? null : canonicalLanguageTag(value);
}

/** Stable identity that de-duplicates ISO aliases without collapsing script or region subtags. */
export function languageIdentity(value: string): string {
  return canonicalLanguageTag(value) ?? value.trim().toLowerCase();
}

/** Canonical ISO language subtag used for language matching and override keys. */
export function normalizeLanguageCode(value: string | null | undefined): string {
  const canonical = canonicalLanguageTag(value ?? "");
  if (!canonical) return "";
  return new Intl.Locale(canonical).language;
}
