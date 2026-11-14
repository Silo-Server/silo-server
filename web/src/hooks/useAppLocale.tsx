import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import type { ReactNode } from "react";
import { Direction } from "radix-ui";

import {
  settingsCapabilitiesSupportKey,
  useEffectiveSettings,
  useSettingsCapabilities,
} from "@/hooks/queries/settingValues";
import { useAppearanceCacheOwner } from "@/hooks/themePreferences";
import {
  adminMessagesRequested,
  AppIntlProvider,
  appIntl,
  createAppIntl,
  loadAdminTranslations,
  loadTranslations,
  PSEUDO_LOCALES_ENABLED,
  setAdminLoadError,
  setAppIntl,
  subscribeAdminRequest,
  type Messages,
} from "@/i18n";
import {
  type AppLocale,
  browserLocaleInputs,
  ENGLISH_APP_LOCALE,
  getAppLocale,
  hasCatalog,
  resolveAppLocale,
  setAppLocale,
  subscribeAppLocale,
} from "@/lib/appLocale";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { appearanceCache, storage } from "@/utils/storage";

const APP_LANGUAGE_KEYS = [SETTING_KEYS.UI_APP_LANGUAGE] as const;
const CACHE_KEY = storage.KEYS.UI_APP_LANGUAGE;
const SIGNED_OUT_KEY = storage.KEYS.UI_APP_LANGUAGE_SIGNED_OUT;
/** Stored for a signed-out "Match device", which must override the cached language. */
const FOLLOW_DEVICE = "device";

/**
 * The language to use before a profile's settings arrive: the one chosen on
 * the signed-out screens, else the one this browser last applied. With no
 * owner yet (signed out, or auth still loading) that is whoever last used the
 * browser.
 */
function languageBeforeSettings(owner: string | null): string | null {
  const signedOut = storage.get(SIGNED_OUT_KEY);
  if (signedOut !== null) return signedOut === FOLLOW_DEVICE ? null : signedOut;
  return appearanceCache.get(CACHE_KEY, owner);
}

/** The direction `<html dir>` has now (`setAppLocale` writes both). */
function appliedDirection() {
  return getAppLocale().dir;
}

function resolve(stored: string | null) {
  return resolveAppLocale({
    stored,
    ...browserLocaleInputs(),
    hasCatalog,
    pseudoLocales: PSEUDO_LOCALES_ENABLED,
  });
}

// Seed the app locale before the first render, so a page doesn't paint in one
// language and switch once the settings request lands.
setAppLocale(resolve(languageBeforeSettings(null)));

const signedOutListeners = new Set<() => void>();

function subscribeSignedOutLanguage(listener: () => void): () => void {
  signedOutListeners.add(listener);
  return () => signedOutListeners.delete(listener);
}

function signedOutLanguage(): string | null {
  return languageBeforeSettings(null);
}

/** The language the signed-out screens use; null follows the browser. */
export function useSignedOutLanguage(): string | null {
  return useSyncExternalStore(subscribeSignedOutLanguage, signedOutLanguage, signedOutLanguage);
}

/**
 * Choose the signed-out screens' language; null follows the browser. It
 * applies until a profile's own setting loads, which then clears it.
 */
export function setSignedOutLanguage(tag: string | null): void {
  storage.set(SIGNED_OUT_KEY, tag ?? FOLLOW_DEVICE);
  for (const listener of signedOutListeners) listener();
}

/**
 * Applies the app language: the active profile's effective `ui.app_language`
 * (the request carries this browser's device id, so a per-device value wins),
 * else the browser's languages, else English. Before the settings load, and on
 * the signed-out screens, it uses the signed-out choice or the language this
 * browser last applied.
 *
 * It loads the language's messages and name data first, then switches the
 * formatting locale, `<html lang dir>` and react-intl together.
 */
export function LocaleProvider({ children }: { children: ReactNode }) {
  const cacheOwner = useAppearanceCacheOwner();
  const { data: capabilities } = useSettingsCapabilities({ enabled: cacheOwner !== null });
  const supported = settingsCapabilitiesSupportKey(capabilities, SETTING_KEYS.UI_APP_LANGUAGE);
  const { data: effectiveSettings } = useEffectiveSettings({
    keys: APP_LANGUAGE_KEYS,
    enabled: cacheOwner !== null && supported,
  });
  // Re-render when the signed-out picker changes its choice.
  useSignedOutLanguage();

  const apiLoaded = cacheOwner !== null && effectiveSettings !== undefined;
  const apiValue = effectiveSettings?.[SETTING_KEYS.UI_APP_LANGUAGE]?.value;
  const apiLanguage = typeof apiValue === "string" ? apiValue : null;
  // Once loaded the server's answer is authoritative, null included.
  const stored = apiLoaded ? apiLanguage : languageBeforeSettings(cacheOwner);
  const { language, catalog, formatLocale, localizedFormats, dir } = useMemo(
    () => resolve(stored),
    [stored],
  );

  useEffect(() => {
    if (!apiLoaded) return;
    if (apiLanguage) appearanceCache.set(CACHE_KEY, apiLanguage, cacheOwner);
    else appearanceCache.remove(CACHE_KEY, cacheOwner);
    // A profile's own setting replaces whatever was picked while signed out.
    storage.remove(SIGNED_OUT_KEY);
  }, [apiLoaded, apiLanguage, cacheOwner]);

  const [intl, setIntl] = useState(appIntl);
  // Admin text loads once something asks for it (useAdminMessages), and then
  // with every later language.
  const withAdmin = useSyncExternalStore(
    subscribeAdminRequest,
    adminMessagesRequested,
    adminMessagesRequested,
  );

  useEffect(() => {
    let cancelled = false;
    const names =
      language === "en"
        ? undefined
        : import("@/lib/languageNames").then((module) => module.loadLanguageNames(language));
    const admin = withAdmin
      ? loadAdminTranslations(catalog).catch((error: unknown) => {
          if (!cancelled) setAdminLoadError(error);
          return undefined;
        })
      : undefined;
    void Promise.all([
      loadTranslations(catalog).catch(() => undefined),
      admin,
      // Without name data, language names stay English.
      names?.catch(() => undefined),
    ]).then(([messages, adminMessages]) => {
      if (cancelled) return;
      // Without its catalog (a chunk a newer release replaced) the language
      // renders as English: English text, plural rules, `lang` and `dir`, and
      // none of its admin part.
      const locale: AppLocale = messages
        ? { language, catalog, formatLocale, localizedFormats, dir }
        : ENGLISH_APP_LOCALE;
      const translated: Messages = messages ? { ...messages, ...adminMessages } : {};
      // The formatter first: helpers that subscribe to the app locale read it.
      const next = createAppIntl(locale.catalog, translated, locale.formatLocale);
      setAppIntl(next, { admin: adminMessages !== undefined });
      setAppLocale(locale);
      setIntl(next);
    });
    return () => {
      cancelled = true;
    };
  }, [language, catalog, formatLocale, localizedFormats, dir, withAdmin]);

  // Radix menus, selects, sliders and tabs, and useDirection() everywhere,
  // follow the direction the document has.
  const appliedDir = useSyncExternalStore(subscribeAppLocale, appliedDirection, appliedDirection);

  return (
    <AppIntlProvider intl={intl}>
      <Direction.Provider dir={appliedDir}>{children}</Direction.Provider>
    </AppIntlProvider>
  );
}
