import { useCallback, useEffect, useRef } from "react";

const DEFAULT_DWELL_DELAY_MS = 140;

/**
 * Avoids starting network/cache work while the pointer is only sweeping across
 * a media row. Keyboard focus and touch intent still prefetch immediately.
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
    timerRef.current = window.setTimeout(() => {
      timerRef.current = null;
      prefetch();
    }, delayMs);
  }, [cancel, delayMs, prefetch]);

  const immediately = useCallback(() => {
    cancel();
    prefetch();
  }, [cancel, prefetch]);

  useEffect(() => cancel, [cancel]);

  return {
    onMouseEnter: schedule,
    onMouseLeave: cancel,
    onFocus: immediately,
    onTouchStart: immediately,
  };
}
