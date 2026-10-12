import { useCallback, useEffect, useRef } from "react";
import type { FocusEvent, PointerEvent } from "react";
import { prefersReducedData } from "@/lib/networkInformation";

const DEFAULT_DWELL_DELAY_MS = 100;

/**
 * Starts `prefetch` once a card shows intent to be opened: a mouse or pen
 * resting on it, or keyboard focus staying on it, for `delayMs`. Leaving or
 * blurring first cancels, so a pointer sweeping across a row, or arrow keys
 * stepping through one, starts nothing for the cards it only passes.
 *
 * Touch gives no intent before the tap itself, and a scrolling finger lands on
 * cards it never meant to open, so touch pointers are ignored. Nothing runs
 * while the browser asks to save data. Callers rely on the query cache to
 * dedupe repeat intents.
 */
export function useDwellPrefetch(prefetch: () => void, delayMs = DEFAULT_DWELL_DELAY_MS) {
  const timerRef = useRef<number | null>(null);

  const cancel = useCallback(() => {
    if (timerRef.current === null) return;
    window.clearTimeout(timerRef.current);
    timerRef.current = null;
  }, []);

  const schedule = useCallback(() => {
    cancel();
    if (prefersReducedData()) return;
    timerRef.current = window.setTimeout(() => {
      timerRef.current = null;
      prefetch();
    }, delayMs);
  }, [cancel, delayMs, prefetch]);

  const onPointerEnter = useCallback(
    (event: PointerEvent) => {
      if (event.pointerType !== "touch") schedule();
    },
    [schedule],
  );

  // A tap focuses the link it lands on in some browsers; only focus the
  // browser would draw a ring for (keyboard and programmatic) counts as intent.
  // React bubbles focus out of portals, so a menu opened from the card is
  // excluded by checking the DOM tree.
  const onFocus = useCallback(
    (event: FocusEvent) => {
      const target = event.target;
      if (
        target instanceof Element &&
        event.currentTarget.contains(target) &&
        target.matches(":focus-visible")
      ) {
        schedule();
      }
    },
    [schedule],
  );

  useEffect(() => cancel, [cancel]);

  return {
    onPointerEnter,
    onPointerLeave: cancel,
    onFocus,
    onBlur: cancel,
  };
}
