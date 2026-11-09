import "@formatjs/intl-displaynames/polyfill-force.js";
import "@formatjs/intl-displaynames/locale-data/en.js";
import { t } from "@/i18n";
import { appLanguage, type AppLanguage } from "@/lib/appLocale";
import { canonicalLanguageTag } from "@/lib/languageTags";

export {
  canonicalLanguageTag,
  canonicalLanguageWireValue,
  languageIdentity,
  normalizeLanguageCode,
} from "@/lib/languageTags";

/**
 * CLDR name data for each non-English app language, loaded when that language
 * is applied. The data modules are named by CLDR locale, which is not always
 * the app tag (there is no `pt-BR` module; `pt` is Brazilian Portuguese).
 */
const NAME_DATA: Record<Exclude<AppLanguage, "en">, () => Promise<unknown>> = {
  es: () => import("@formatjs/intl-displaynames/locale-data/es.js"),
  de: () => import("@formatjs/intl-displaynames/locale-data/de.js"),
  fr: () => import("@formatjs/intl-displaynames/locale-data/fr.js"),
  "pt-BR": () => import("@formatjs/intl-displaynames/locale-data/pt.js"),
  ja: () => import("@formatjs/intl-displaynames/locale-data/ja.js"),
  "zh-Hans": () => import("@formatjs/intl-displaynames/locale-data/zh-Hans.js"),
  ar: () => import("@formatjs/intl-displaynames/locale-data/ar.js"),
};

const CLDR_LOCALE: Partial<Record<AppLanguage, string>> = { "pt-BR": "pt" };

interface NameSet {
  language: Intl.DisplayNames;
  script: Intl.DisplayNames;
  region: Intl.DisplayNames;
}

function nameSet(locale: string): NameSet {
  const options = { fallback: "none" } as const;
  return {
    language: new Intl.DisplayNames([locale], { ...options, type: "language" }),
    script: new Intl.DisplayNames([locale], { ...options, type: "script" }),
    region: new Intl.DisplayNames([locale], { ...options, type: "region" }),
  };
}

const englishNames = nameSet("en");
const loadedNames = new Map<AppLanguage, NameSet>([["en", englishNames]]);
const loadListeners = new Set<() => void>();

/** Called whenever a language's name data finishes loading. */
export function subscribeLanguageNameData(listener: () => void): () => void {
  loadListeners.add(listener);
  return () => loadListeners.delete(listener);
}

/**
 * Load the name data for an app language. `LocaleProvider` awaits this before
 * switching languages, so names are ready by the time the switch renders.
 */
export async function loadLanguageNames(language: AppLanguage): Promise<void> {
  if (language === "en" || loadedNames.has(language)) return;
  await NAME_DATA[language]();
  loadedNames.set(language, nameSet(CLDR_LOCALE[language] ?? language));
  for (const listener of loadListeners) listener();
}

/** The language names currently come out in. */
export function namesLanguage(): AppLanguage {
  const language = appLanguage();
  return loadedNames.has(language) ? language : "en";
}

/** Names in the interface language, or English until its data has loaded. */
function interfaceNames(): NameSet {
  return loadedNames.get(namesLanguage()) ?? englishNames;
}

function displayName(names: Intl.DisplayNames, value: string): string | null {
  try {
    return names.of(value) ?? null;
  } catch {
    return null;
  }
}

/**
 * Returns a deterministic CLDR name for an ISO/BCP 47 value.
 *
 * Some CLDR releases omit a precomposed language-and-script label. In that
 * case, compose the independently standardized language, script, and region
 * names so tags such as `sr-Latn` remain distinct from `sr`.
 */
function cldrLanguageName(names: NameSet, value: string): string | null {
  const canonical = canonicalLanguageTag(value);
  if (!canonical) return null;

  let locale: Intl.Locale;
  try {
    locale = new Intl.Locale(canonical);
  } catch {
    return null;
  }

  const language = displayName(names.language, locale.language);
  if (!language) return null;

  const exact = displayName(names.language, canonical);
  const script = locale.script ? displayName(names.script, locale.script) : null;
  const region = locale.region ? displayName(names.region, locale.region) : null;

  if (exact && ((!script && !region) || exact !== language)) return exact;
  if (script && region)
    return t("web.languageName.withScriptAndRegion", { language, script, region });
  if (script) return t("web.languageName.withScript", { language, script });
  if (region) return t("web.languageName.withRegion", { language, region });
  return language;
}

/** The name in the interface language, or null for an unassigned or invalid tag. */
export function displayLanguageName(value: string): string | null {
  return cldrLanguageName(interfaceNames(), value);
}

/** User-facing name with an explicit fallback for unassigned or invalid tags. */
export function getLanguageName(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) return t("web.languageName.unknown");
  return displayLanguageName(trimmed) ?? t("web.languageName.unknownTag", { tag: trimmed });
}

/** Empty-preserving variant for optional metadata and filter labels. */
export function formatLanguage(value: string): string {
  const trimmed = value.trim();
  return trimmed ? getLanguageName(trimmed) : "";
}
