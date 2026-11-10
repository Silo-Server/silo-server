import { useSyncExternalStore } from "react";

import { appIntl, subscribeAppIntl } from "@/i18n";
import { subscribeAppLocale } from "@/lib/appLocale";

type LanguageNames = typeof import("@/lib/languageNames");

// languageNames.ts bundles the formatjs DisplayNames polyfill and its English
// locale data so every browser shows the same names. Card overlays render on
// the launch screen, so they load it on first use instead of pulling it into
// the entry chunk.
let languageNames: LanguageNames | null = null;
let loading: Promise<void> | null = null;
const listeners = new Set<() => void>();

function notify(): void {
  for (const listener of listeners) listener();
}

function importLanguageNames(): void {
  loading ??= import("@/lib/languageNames").then(
    (module) => {
      languageNames = module;
      // Names switch language when that language's data lands.
      module.subscribeLanguageNameData(notify);
      notify();
    },
    () => {
      // Leave the label empty; the next lookup retries the import.
      loading = null;
    },
  );
}

/**
 * Returns the display name for a language tag, or null while the name data is
 * still loading. The first call starts the load; `useLanguageNamesLoaded`
 * re-renders the caller once it lands.
 */
export function formatLanguageWhenLoaded(value: string): string | null {
  if (languageNames) return languageNames.formatLanguage(value) || null;
  importLanguageNames();
  return null;
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  // Names follow the interface language, so a language change re-renders too.
  const unsubscribeLocale = subscribeAppLocale(listener);
  // Composed names ("Language (Region)") follow the messages.
  const unsubscribeMessages = subscribeAppIntl(listener);
  return () => {
    listeners.delete(listener);
    unsubscribeLocale();
    unsubscribeMessages();
  };
}

function ignoreChanges(): () => void {
  return () => {};
}

/** The languages the loaded names and their wording are in, or null while they load. */
function loadedLanguage(): string | null {
  return languageNames ? `${languageNames.namesLanguage()}|${appIntl().locale}` : null;
}

/**
 * Re-renders the calling component when the language name data finishes
 * loading, and when the names change language. Pass `active: false` when the
 * caller shows no language names, so the load does not re-render it.
 */
export function useLanguageNamesLoaded(active = true): boolean {
  return (
    useSyncExternalStore(active ? subscribe : ignoreChanges, loadedLanguage, loadedLanguage) !==
    null
  );
}
