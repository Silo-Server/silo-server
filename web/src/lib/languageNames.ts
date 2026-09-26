import "@formatjs/intl-displaynames/polyfill-force.js";
import "@formatjs/intl-displaynames/locale-data/en.js";
import { canonicalLanguageTag } from "@/lib/languageTags";

export {
  canonicalLanguageTag,
  canonicalLanguageWireValue,
  languageIdentity,
  normalizeLanguageCode,
} from "@/lib/languageTags";

const englishLanguageNames = new Intl.DisplayNames(["en"], {
  type: "language",
  fallback: "none",
});
const englishScriptNames = new Intl.DisplayNames(["en"], {
  type: "script",
  fallback: "none",
});
const englishRegionNames = new Intl.DisplayNames(["en"], {
  type: "region",
  fallback: "none",
});

function displayName(names: Intl.DisplayNames, value: string): string | null {
  try {
    return names.of(value) ?? null;
  } catch {
    return null;
  }
}

/**
 * Returns a deterministic English CLDR name for an ISO/BCP 47 value.
 *
 * Some CLDR releases omit a precomposed language-and-script label. In that
 * case, compose the independently standardized language, script, and region
 * names so tags such as `sr-Latn` remain distinct from `sr`.
 */
export function englishLanguageName(value: string): string | null {
  const canonical = canonicalLanguageTag(value);
  if (!canonical) return null;

  let locale: Intl.Locale;
  try {
    locale = new Intl.Locale(canonical);
  } catch {
    return null;
  }

  const language = displayName(englishLanguageNames, locale.language);
  if (!language) return null;

  const exact = displayName(englishLanguageNames, canonical);
  const qualifiers = [
    locale.script ? displayName(englishScriptNames, locale.script) : null,
    locale.region ? displayName(englishRegionNames, locale.region) : null,
  ].filter((part): part is string => Boolean(part));

  if (exact && (qualifiers.length === 0 || exact !== language)) return exact;
  return qualifiers.length > 0 ? `${language} (${qualifiers.join(", ")})` : language;
}

/** User-facing name with an explicit fallback for unassigned or invalid tags. */
export function getLanguageName(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) return "Unknown";
  return englishLanguageName(trimmed) ?? `Unknown language (${trimmed})`;
}

/** Empty-preserving variant for optional metadata and filter labels. */
export function formatLanguage(value: string): string {
  const trimmed = value.trim();
  return trimmed ? getLanguageName(trimmed) : "";
}
