import { useCallback, useSyncExternalStore } from "react";

// One MediaQueryList per distinct query, shared by every consumer: getSnapshot
// runs on every render of every subscriber (hundreds of cards on a home page),
// and matchMedia re-parses the query on each call. Keyed to the matchMedia
// function itself so a test that stubs matchMedia never reads a list cached
// from a different stub.
let cachedMatchMedia: typeof window.matchMedia | undefined;
const mediaQueryLists = new Map<string, MediaQueryList>();

function getMediaQueryList(query: string): MediaQueryList | undefined {
  if (typeof window === "undefined" || !window.matchMedia) return undefined;
  if (window.matchMedia !== cachedMatchMedia) {
    cachedMatchMedia = window.matchMedia;
    mediaQueryLists.clear();
  }
  let list = mediaQueryLists.get(query);
  if (!list) {
    list = window.matchMedia(query);
    mediaQueryLists.set(query, list);
  }
  return list;
}

/**
 * Tracks a CSS media query.
 *
 * `fallback` answers where matchMedia does not exist — server rendering, tests,
 * and older browsers — so a caller can say which side of the query those
 * environments should be treated as.
 */
export function useMediaQuery(query: string, fallback = false): boolean {
  const subscribe = useCallback(
    (onStoreChange: () => void) => {
      const media = getMediaQueryList(query);
      media?.addEventListener("change", onStoreChange);
      return () => media?.removeEventListener("change", onStoreChange);
    },
    [query],
  );

  const getSnapshot = useCallback(
    () => getMediaQueryList(query)?.matches ?? fallback,
    [query, fallback],
  );

  return useSyncExternalStore(subscribe, getSnapshot, () => fallback);
}
