/**
 * The app language: which shipped language the interface uses, and which
 * locale dates and numbers are formatted with.
 *
 * Like the date/time preferences in lib/datetime, the applied locale lives in
 * module state so plain helpers can format with it; `LocaleProvider` resolves
 * it from the profile's `ui.app_language` and keeps this state in sync.
 */

// Relative with its extension: lib/datetime imports this module, and a
// datetime test loads it in plain Node, which resolves neither form otherwise.
import { canonicalLanguageTag } from "./languageTags.ts";

/**
 * Languages the web client ships, with each one's name in itself for the
 * language pickers. A fixed table rather than `Intl.DisplayNames`, so the
 * signed-out screens load no CLDR data and every browser shows the same names.
 */
export const SHIPPED_LANGUAGES = [
  { tag: "en", endonym: "English" },
  { tag: "es", endonym: "Español" },
  { tag: "de", endonym: "Deutsch" },
  { tag: "fr", endonym: "Français" },
  { tag: "pt-BR", endonym: "Português (Brasil)" },
  { tag: "ja", endonym: "日本語" },
  { tag: "zh-Hans", endonym: "简体中文" },
  { tag: "ar", endonym: "العربية" },
] as const;

export type AppLanguage = (typeof SHIPPED_LANGUAGES)[number]["tag"];

export interface AppLocale {
  /** The shipped language the interface uses. */
  language: AppLanguage;
  /**
   * Locale for dates and numbers. `undefined` means the browser's own default,
   * which is what every format call used before the app had a language.
   */
  formatLocale: string | undefined;
  /**
   * Whether dates and numbers come out in the interface language, so formats
   * that pin an English regional locale should follow it instead.
   */
  localizedFormats: boolean;
  dir: "ltr" | "rtl";
}

function maximized(tag: string): Intl.Locale | null {
  try {
    return new Intl.Locale(tag.replace(/_/g, "-")).maximize();
  } catch {
    return null;
  }
}

const SHIPPED_MAXIMIZED = SHIPPED_LANGUAGES.map((shipped) => ({
  tag: shipped.tag,
  locale: maximized(shipped.tag)!,
}));

/**
 * The shipped language a tag selects, or null. A tag matches the shipped
 * language with the same language and script once both are maximized, so a
 * regional tag gets its language (`es-MX` → `es`, `pt-PT` → `pt-BR`) and
 * Chinese follows its script (`zh-CN`, `zh-SG` → `zh-Hans`; `zh-TW` → none).
 */
export function matchShippedLanguage(tag: string | null | undefined): AppLanguage | null {
  if (!tag) return null;
  const locale = maximized(tag);
  if (!locale) return null;
  const match = SHIPPED_MAXIMIZED.find(
    (shipped) =>
      shipped.locale.language === locale.language && shipped.locale.script === locale.script,
  );
  return match?.tag ?? null;
}

/**
 * Resolve the app locale, highest precedence first:
 *
 * 1. the profile's `ui.app_language` (`stored`), when it selects a shipped
 *    language; null or an unshipped tag falls through;
 * 2. the first browser language that selects a shipped language with a
 *    translation catalog (until a language has one, a browser in that language
 *    keeps today's English interface rather than English text marked and
 *    worded as another language);
 * 3. English.
 *
 * Formatting keeps the browser's default locale unless the person chose a
 * language whose default differs: someone who picked German on an English
 * browser gets German dates, but nobody who did not choose sees any change,
 * and choosing English on an `en-GB` browser keeps British dates.
 */
export function resolveAppLocale(input: {
  stored: unknown;
  navigatorLanguages: readonly string[];
  runtimeLocale: string;
  hasCatalog: (language: AppLanguage) => boolean;
}): AppLocale {
  const stored = typeof input.stored === "string" ? canonicalLanguageTag(input.stored) : null;
  const chosen = matchShippedLanguage(stored);
  const fromBrowser = input.navigatorLanguages
    .map(matchShippedLanguage)
    .find((match) => match === "en" || (match !== null && input.hasCatalog(match)));
  const language = chosen ?? fromBrowser ?? "en";
  const runtimeLanguage = matchShippedLanguage(input.runtimeLocale);
  const formatLocale = chosen && runtimeLanguage !== chosen ? (stored ?? undefined) : undefined;
  return {
    language,
    formatLocale,
    localizedFormats: language !== "en" && (formatLocale ? true : runtimeLanguage === language),
    dir: language === "ar" ? "rtl" : "ltr",
  };
}

/** The browser inputs `resolveAppLocale` takes besides the stored setting. */
export function browserLocaleInputs(): {
  navigatorLanguages: readonly string[];
  runtimeLocale: string;
} {
  const navigatorLanguages =
    typeof navigator === "undefined"
      ? []
      : navigator.languages?.length
        ? navigator.languages
        : [navigator.language];
  return {
    navigatorLanguages,
    runtimeLocale: new Intl.DateTimeFormat().resolvedOptions().locale,
  };
}

let current: AppLocale = {
  language: "en",
  formatLocale: undefined,
  localizedFormats: false,
  dir: "ltr",
};
const listeners = new Set<() => void>();

function applyToDocument(locale: AppLocale): void {
  if (typeof document === "undefined") return;
  document.documentElement.lang = locale.language;
  document.documentElement.dir = locale.dir;
}

export function getAppLocale(): AppLocale {
  return current;
}

/** The interface language, for text: lists, relative times, language names. */
export function appLanguage(): AppLanguage {
  return current.language;
}

/** The locale for dates and numbers; pass it where `toLocale*String` took none. */
export function formatLocale(): string | undefined {
  return current.formatLocale;
}

/**
 * For formats that pin an English regional locale (`en-GB` day-first dates):
 * the pinned locale, unless dates come out in a non-English interface
 * language, which then wins (`undefined` is the browser's own default).
 */
export function pinnedLocaleUnlessLocalized(english: string): string | undefined {
  return current.localizedFormats ? current.formatLocale : english;
}

export function setAppLocale(next: AppLocale): void {
  applyToDocument(next);
  if (
    next.language === current.language &&
    next.formatLocale === current.formatLocale &&
    next.localizedFormats === current.localizedFormats
  ) {
    return;
  }
  current = next;
  for (const listener of listeners) listener();
}

export function subscribeAppLocale(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
