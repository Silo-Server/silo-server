import { useEffect, useLayoutEffect, useRef } from "react";
import { useLocation, useNavigationType } from "react-router";

import {
  enterHistoryEntry,
  finishRestore,
  historyEntryId,
  isRestorePending,
  recordScroll,
  relayoutRestore,
  restoreLanded,
} from "@/lib/scrollRestoration";

/** How long a page may take to grow tall enough for its saved position. */
const RESTORE_TIMEOUT_MS = 10_000;

/** Input that means the user is moving the page themselves. */
const USER_SCROLL_EVENTS = ["wheel", "touchstart", "keydown", "pointerdown"] as const;

function maxScrollY(): number {
  return document.documentElement.scrollHeight - window.innerHeight;
}

/**
 * Opens a new page at the top and returns Back and Forward to where the entry
 * was left. Mount once, above every route.
 *
 * A push or replace to a new pathname scrolls to the top. One that keeps the
 * pathname, such as a filter or page change in the URL, leaves the scroll
 * alone. A history pop restores the entry's saved position: a page with an
 * `ItemGrid` restores its own anchor once its rows exist, and any other page
 * gets the pixel offset once the document is tall enough for it. Either way
 * the restore is re-applied as the loading page changes height until it holds
 * still, and gives up when the user scrolls, taps or types first, or after
 * `RESTORE_TIMEOUT_MS`.
 */
export function useScrollRestoration(): void {
  const location = useLocation();
  const navigationType = useNavigationType();
  const previousPathname = useRef<string | null>(null);

  useEffect(() => {
    const previous = window.history.scrollRestoration;
    // The browser's own restore would race this one and lose to the page's
    // loading layout.
    window.history.scrollRestoration = "manual";
    const onScroll = () => recordScroll(window.scrollY);
    window.addEventListener("scroll", onScroll, { passive: true });
    for (const type of USER_SCROLL_EVENTS) {
      // The user scrolled, tapped or typed first; their position wins.
      window.addEventListener(type, finishRestore, { capture: true, passive: true });
    }
    return () => {
      window.history.scrollRestoration = previous;
      window.removeEventListener("scroll", onScroll);
      for (const type of USER_SCROLL_EVENTS) {
        window.removeEventListener(type, finishRestore, { capture: true });
      }
    };
  }, []);

  // Layout effect, not effect: after paint the browser has already shown one
  // frame of the new route at the old route's scroll offset, which reads as
  // a jump rather than an arrival.
  useLayoutEffect(() => {
    const pathnameChanged = previousPathname.current !== location.pathname;
    previousPathname.current = location.pathname;
    const saved = enterHistoryEntry(historyEntryId(location), navigationType === "POP");

    if (!saved) {
      if (pathnameChanged) window.scrollTo(0, 0);
      // An entry that keeps the previous offset may never scroll; record it now.
      recordScroll(window.scrollY);
      return;
    }

    // As close as the page allows right now; a grid moves it to its anchor.
    const scrollToSaved = () => {
      window.scrollTo(0, saved.y);
      if (maxScrollY() >= saved.y) restoreLanded();
    };
    if (saved.grid) window.scrollTo(0, saved.y);
    else scrollToSaved();
    const timeout = window.setTimeout(finishRestore, RESTORE_TIMEOUT_MS);

    const settle = () => {
      if (!isRestorePending()) return;
      if (saved.grid) relayoutRestore();
      else scrollToSaved();
    };
    if (typeof ResizeObserver === "undefined") return () => window.clearTimeout(timeout);
    const observer = new ResizeObserver(settle);
    observer.observe(document.body);
    return () => {
      window.clearTimeout(timeout);
      observer.disconnect();
    };
  }, [location, navigationType]);
}
