import { useSyncExternalStore } from "react";

type LanguageNames = typeof import("@/lib/languageNames");

// languageNames.ts bundles the formatjs DisplayNames polyfill and its English
// locale data so every browser shows the same names. Card overlays render on
// the launch screen, so they load it on first use instead of pulling it into
// the entry chunk.
let languageNames: LanguageNames | null = null;
let loading: Promise<void> | null = null;
const listeners = new Set<() => void>();

function loadLanguageNames(): void {
  loading ??= import("@/lib/languageNames").then(
    (module) => {
      languageNames = module;
      for (const listener of listeners) listener();
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
  loadLanguageNames();
  return null;
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function ignoreChanges(): () => void {
  return () => {};
}

function isLoaded(): boolean {
  return languageNames !== null;
}

/**
 * Re-renders the calling component when the language name data finishes
 * loading. Pass `active: false` when the caller shows no language names, so the
 * load does not re-render it.
 */
export function useLanguageNamesLoaded(active = true): boolean {
  return useSyncExternalStore(active ? subscribe : ignoreChanges, isLoaded, isLoaded);
}
