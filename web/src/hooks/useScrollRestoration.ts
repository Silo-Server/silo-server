import { useCallback, useEffect, useLayoutEffect, useRef } from "react";
import { useLocation, useNavigationType, type Location } from "react-router";

/**
 * How long a restore waits without the document growing before giving up. A
 * virtualized list sizes itself from an estimate that walks down as pages
 * load, so returning deep into a list can take several rounds of pages before
 * the document is tall enough for the remembered offset.
 */
const RESTORE_IDLE_MS = 2_000;

/**
 * The key an entry's offset is stored under. React Router gives the session's
 * first entry and every entry a plain `#anchor` link creates (the skip link,
 * an in-page link) the same key, "default", so those carry their URL as well:
 * otherwise following an anchor restores the first entry's offset over the
 * browser's jump to it.
 */
function entryKey(location: Location): string {
  return location.key === "default"
    ? `default:${location.pathname}${location.search}${location.hash}`
    : location.key;
}

interface PendingRestore {
  key: string;
  top: number;
  frame: number;
  height: number;
  deadline: number;
}

/**
 * Keeps each history entry's window scroll position and puts it back when the
 * reader returns to that entry.
 *
 * A new navigation still opens at the top: the reader is going somewhere they
 * have not been, and a list is expected to start at its first row. Back and
 * Forward are the reader returning, so they keep their place. Offsets are
 * keyed by `location.key`, not by URL, so a sidebar link to a library the
 * reader visited earlier is a fresh visit rather than a jump into the middle
 * of it.
 *
 * Browsers restore same-document history offsets themselves, but only against
 * the document as it stands when the pop lands. A virtualized list's rows
 * arrive after that, so the browser's offset is clamped short and never
 * retried. React Router's `<ScrollRestoration>` has the same one-shot
 * limitation. Taking manual control lets this hook reapply the offset as the
 * list grows, and the reader's own input cancels it rather than fighting a
 * jump under their hands.
 */
export function useScrollRestoration(): void {
  const location = useLocation();
  const navigationType = useNavigationType();
  const key = entryKey(location);
  const positions = useRef(new Map<string, number>());
  const activeKey = useRef(key);
  const previousPathname = useRef<string | null>(null);
  const pending = useRef<PendingRestore | null>(null);
  const stopWatchingForInput = useRef<(() => void) | null>(null);

  const clearInputListeners = useCallback(() => {
    stopWatchingForInput.current?.();
    stopWatchingForInput.current = null;
  }, []);

  const startRestore = useCallback(
    (key: string, top: number) => {
      const height = document.documentElement.scrollHeight;
      const restore: PendingRestore = {
        key,
        top,
        frame: 0,
        height,
        deadline: performance.now() + RESTORE_IDLE_MS,
      };
      pending.current = restore;

      function stop(landed: boolean) {
        if (pending.current !== restore) return;
        pending.current = null;
        if (landed) positions.current.set(restore.key, restore.top);
        clearInputListeners();
      }

      // A short document clamps the scroll to its current height. Retry until
      // lazily loaded rows make room, the reader takes over, or time runs out.
      function step() {
        if (pending.current !== restore) return;
        window.scrollTo(0, restore.top);
        if (Math.abs(window.scrollY - restore.top) <= 1) {
          stop(true);
          return;
        }
        // Each round of rows that arrives buys another idle window; the
        // restore only gives up once the list has stopped growing.
        const height = document.documentElement.scrollHeight;
        if (height > restore.height) {
          restore.height = height;
          restore.deadline = performance.now() + RESTORE_IDLE_MS;
        }
        if (performance.now() >= restore.deadline) {
          stop(false);
          return;
        }
        restore.frame = window.requestAnimationFrame(step);
      }

      // Anything the reader does before the position lands wins over it.
      const cancel = () => {
        if (pending.current !== restore) return;
        window.cancelAnimationFrame(restore.frame);
        stop(false);
      };
      const listenerOptions = { capture: true, passive: true } as const;
      const inputEvents = ["keydown", "pointerdown", "touchstart", "wheel"] as const;
      for (const event of inputEvents) {
        window.addEventListener(event, cancel, listenerOptions);
      }
      stopWatchingForInput.current = () => {
        for (const event of inputEvents) {
          window.removeEventListener(event, cancel, listenerOptions);
        }
      };

      // Wait one frame: a virtualized list mounts its own virtualizer in a
      // layout effect after this one and writes its cached offset back to the
      // window, which would undo an earlier restore. Animation frames run
      // before paint, so when the page is already tall enough the first
      // painted frame is the restored one; a list that still has to grow
      // shows each step on the way down.
      restore.frame = window.requestAnimationFrame(step);
    },
    [clearInputListeners],
  );

  const cancelPendingRestore = useCallback(() => {
    const restore = pending.current;
    pending.current = null;
    if (restore) window.cancelAnimationFrame(restore.frame);
    clearInputListeners();
  }, [clearInputListeners]);

  // Browsers remember scroll offsets for same-document history entries too.
  // Taking that over is what lets a restore wait for the list to grow instead
  // of being clamped against the page as it stood at the pop.
  useEffect(() => {
    const previousMode = window.history.scrollRestoration;
    window.history.scrollRestoration = "manual";
    return () => {
      window.history.scrollRestoration = previousMode;
    };
  }, []);

  useEffect(() => () => cancelPendingRestore(), [cancelPendingRestore]);

  useEffect(() => {
    const record = () => {
      // During a restore the offset is the clamped or not-yet-restored one.
      if (pending.current !== null) return;
      positions.current.set(activeKey.current, window.scrollY);
    };
    record();
    window.addEventListener("scroll", record, { passive: true });
    return () => window.removeEventListener("scroll", record);
  }, []);

  useLayoutEffect(() => {
    const pathnameChanged = previousPathname.current !== location.pathname;
    activeKey.current = key;
    previousPathname.current = location.pathname;

    if (navigationType === "POP") {
      cancelPendingRestore();
      const saved = positions.current.get(key);
      if (saved !== undefined) {
        startRestore(key, saved);
      } else if (pathnameChanged) {
        positions.current.set(key, 0);
        window.scrollTo(0, 0);
      }
      return;
    }

    if (pathnameChanged) {
      // A new destination opens at the top, as it always has.
      cancelPendingRestore();
      positions.current.set(key, 0);
      window.scrollTo(0, 0);
      return;
    }

    // A replace on the same pathname is the same screen with a corrected URL
    // (a normalized library query, say): hold the reader's place, and carry an
    // in-flight restore over to the entry's new key.
    if (navigationType === "REPLACE") {
      const restore = pending.current;
      if (restore) {
        positions.current.set(key, restore.top);
        restore.key = key;
      } else {
        positions.current.set(key, window.scrollY);
      }
      return;
    }

    // A push that only rewrites the query is another tab or filter set on the
    // same screen. It keeps the reader's offset, as it always has, but an
    // in-flight restore belongs to the entry behind it.
    cancelPendingRestore();
    positions.current.set(key, window.scrollY);
  }, [key, location.pathname, navigationType, cancelPendingRestore, startRestore]);
}
