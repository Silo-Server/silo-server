import { useCallback, useEffect, useRef } from "react";
import type { FocusEvent, PointerEvent } from "react";
import {
  ensurePointerTracking,
  pointerSettledIn,
  speculationAllowed,
  startIntentPrefetch,
  touchTravel,
  TOUCH_HOLD_SLOP_PX,
  TOUCH_SLOP_PX,
  type IntentTrigger,
} from "@/lib/intentPrefetch";

/**
 * `press` prefetches on a mouse button going down, a tap, or a finger held
 * still on the element: about a click's worth of lead. `intent` also prefetches when the pointer
 * settles on the element or keyboard focus rests on it, for the cards people
 * aim at; a pointer sweeping across a row never settles, so it fetches nothing.
 */
export type IntentPrefetchMode = "press" | "intent";

/** How often a hovered element checks whether the pointer has settled. */
const SETTLE_POLL_MS = 50;
/** Tabbing through a grid passes over cards; focus has to rest this long. */
const FOCUS_REST_MS = 200;
/** A touch held this long without moving is probably a tap, not a swipe. */
const TOUCH_HOLD_MS = 80;

export interface IntentPrefetchHandlers {
  onPointerEnter?: (event: PointerEvent<HTMLElement>) => void;
  onPointerLeave?: () => void;
  onPointerDown?: (event: PointerEvent<HTMLElement>) => void;
  onPointerUp?: (event: PointerEvent<HTMLElement>) => void;
  onPointerCancel?: () => void;
  onFocus?: (event: FocusEvent<HTMLElement>) => void;
  onBlur?: () => void;
}

const NO_HANDLERS: IntentPrefetchHandlers = {};

function isKeyboardFocus(element: HTMLElement): boolean {
  try {
    return element.matches(":focus-visible");
  } catch {
    return false;
  }
}

/**
 * Event handlers that run `prefetch` once the user shows intent to open
 * `target` (see `IntentPrefetchMode`). A null target disables them.
 */
export function useIntentPrefetch(
  target: string | null,
  prefetch: () => Promise<unknown>,
  mode: IntentPrefetchMode,
): IntentPrefetchHandlers {
  const timerRef = useRef<number | null>(null);
  // The touch in progress, and whether its hold already started a prefetch.
  const touchRef = useRef<{ pointerId: number; prefetched: boolean } | null>(null);
  const prefetchRef = useRef(prefetch);
  useEffect(() => {
    prefetchRef.current = prefetch;
  });

  const cancel = useCallback(() => {
    if (timerRef.current !== null) window.clearTimeout(timerRef.current);
    timerRef.current = null;
  }, []);

  /** Replaces any pending check with `run` after `ms`. */
  const schedule = useCallback(
    (ms: number, run: () => void) => {
      cancel();
      timerRef.current = window.setTimeout(() => {
        timerRef.current = null;
        run();
      }, ms);
    },
    [cancel],
  );

  const fire = useCallback(
    (trigger: IntentTrigger) =>
      target !== null && startIntentPrefetch(target, trigger, () => prefetchRef.current()),
    [target],
  );

  useEffect(() => {
    if (target !== null) ensurePointerTracking();
  }, [target]);
  // A pending check belongs to the target it was scheduled for.
  useEffect(() => cancel, [cancel, target]);

  if (target === null) return NO_HANDLERS;

  return {
    onPointerEnter: (event) => {
      if (
        mode !== "intent" ||
        (event.pointerType !== "mouse" && event.pointerType !== "pen") ||
        !speculationAllowed()
      ) {
        return;
      }
      const element = event.currentTarget;
      const enteredAt = performance.now();
      const poll = () =>
        schedule(SETTLE_POLL_MS, () => {
          // A refused speculative prefetch (another one is running) retries
          // on the next poll while the pointer stays.
          if (!pointerSettledIn(element, enteredAt) || !fire("settle")) poll();
        });
      poll();
    },
    onPointerLeave: cancel,
    onPointerDown: (event) => {
      if (
        event.defaultPrevented ||
        event.button !== 0 ||
        event.metaKey ||
        event.ctrlKey ||
        event.shiftKey ||
        event.altKey
      ) {
        return;
      }
      cancel();
      if (event.pointerType !== "touch") {
        fire("press");
        return;
      }
      const touch = { pointerId: event.pointerId, prefetched: false };
      touchRef.current = touch;
      schedule(TOUCH_HOLD_MS, () => {
        touch.prefetched = touchTravel(touch.pointerId) <= TOUCH_HOLD_SLOP_PX && fire("touch-hold");
      });
    },
    onPointerUp: (event) => {
      const touch = touchRef.current;
      if (event.pointerType !== "touch" || touch?.pointerId !== event.pointerId) return;
      touchRef.current = null;
      cancel();
      // A tap is a press and skips the speculative limits, so it prefetches
      // unless the hold already did.
      if (!touch.prefetched && touchTravel(touch.pointerId) <= TOUCH_SLOP_PX) fire("tap");
    },
    onPointerCancel: cancel,
    onFocus: (event) => {
      if (mode !== "intent" || !isKeyboardFocus(event.currentTarget)) return;
      schedule(FOCUS_REST_MS, () => fire("focus"));
    },
    onBlur: cancel,
  };
}
