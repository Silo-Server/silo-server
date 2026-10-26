import { useSyncExternalStore } from "react";

/**
 * Lets background widgets wait until the page's first content has loaded.
 *
 * On a plain-HTTP server the browser opens at most six connections per origin,
 * so every request the shell starts on first load queues ahead of the ones the
 * page is waiting on. Widgets nobody looks at in the first second (the admin
 * activity indicator, unread badges) read this flag and start their requests
 * once Home has its first rows, or after a short fallback on any page.
 */

/** How long a widget waits at most when nothing marks the content loaded. */
export const STARTUP_DEFER_FALLBACK_MS = 1500;

let loaded = false;
let fallbackTimer: ReturnType<typeof setTimeout> | undefined;
const listeners = new Set<() => void>();

/** Mark the page's first content as loaded; deferred widgets start now. */
export function markCriticalContentLoaded() {
  if (loaded) return;
  loaded = true;
  if (fallbackTimer !== undefined) clearTimeout(fallbackTimer);
  fallbackTimer = undefined;
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  if (!loaded && fallbackTimer === undefined) {
    fallbackTimer = setTimeout(markCriticalContentLoaded, STARTUP_DEFER_FALLBACK_MS);
  }
  return () => {
    listeners.delete(listener);
  };
}

function getSnapshot() {
  return loaded;
}

/** True once the page's first content has loaded (or the fallback elapsed). */
export function useCriticalContentLoaded(): boolean {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}

/** Test hook: return to the initial, not-yet-loaded state. */
export function resetCriticalContentLoadedForTests() {
  loaded = false;
  if (fallbackTimer !== undefined) clearTimeout(fallbackTimer);
  fallbackTimer = undefined;
}
