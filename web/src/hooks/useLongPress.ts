import { useEffect, useRef } from "react";
import type { RefObject } from "react";

const LONG_PRESS_DELAY_MS = 500;
/** A hold that drifts further than this is a carousel swipe or a page scroll. */
const LONG_PRESS_MOVE_TOLERANCE_PX = 10;
/**
 * How long after a long press the follow-up click and callout are swallowed.
 * The browser delivers them a few frames later — or, when the platform opens
 * its own callout instead, not at all — so the window has to expire on its own.
 */
const LONG_PRESS_SUPPRESS_MS = 800;

interface UseLongPressOptions {
  onLongPress: () => void;
  /** Skips all listeners when false (e.g. a card with no actions to offer). */
  enabled?: boolean;
}

/**
 * Fires `onLongPress` when a touch (or pen) press on `targetRef` is held still
 * for {@link LONG_PRESS_DELAY_MS}, then suppresses the click and context menu
 * the platform sends afterwards so the card's own link does not also navigate.
 * Mouse presses are ignored: precise pointers use the hover controls instead.
 */
export function useLongPress(
  targetRef: RefObject<HTMLElement | null> | undefined,
  { onLongPress, enabled = true }: UseLongPressOptions,
) {
  // Held in a ref so a caller's inline callback cannot re-attach the listeners
  // mid-press, which would cancel the hold on every parent render.
  const onLongPressRef = useRef(onLongPress);
  useEffect(() => {
    onLongPressRef.current = onLongPress;
  });

  useEffect(() => {
    const target = targetRef?.current;
    if (!target || !enabled) return;
    // Aliased so the null check above narrows inside the hoisted handlers below.
    const targetElement = target;

    let timer: number | null = null;
    let suppressionCleanupTimer: number | null = null;
    let press: { pointerId: number; clientX: number; clientY: number } | null = null;
    let suppressUntil = 0;

    function removeSuppressionListeners() {
      if (suppressionCleanupTimer !== null) {
        window.clearTimeout(suppressionCleanupTimer);
        suppressionCleanupTimer = null;
      }
      targetElement.removeEventListener("click", handleClickCapture, true);
      targetElement.removeEventListener("contextmenu", handleContextMenu);
    }

    function installSuppressionListeners() {
      targetElement.addEventListener("click", handleClickCapture, true);
      targetElement.addEventListener("contextmenu", handleContextMenu);
    }

    function stopTracking() {
      if (timer !== null) {
        window.clearTimeout(timer);
        timer = null;
      }
      press = null;
      window.removeEventListener("pointermove", handlePointerMove);
      window.removeEventListener("pointerup", handlePointerUp);
      window.removeEventListener("pointercancel", handlePointerUp);
    }

    function handlePointerMove(event: PointerEvent) {
      if (!press || event.pointerId !== press.pointerId) return;
      if (
        Math.abs(event.clientX - press.clientX) > LONG_PRESS_MOVE_TOLERANCE_PX ||
        Math.abs(event.clientY - press.clientY) > LONG_PRESS_MOVE_TOLERANCE_PX
      ) {
        stopTracking();
        removeSuppressionListeners();
      }
    }

    function handlePointerUp(event: PointerEvent) {
      if (press && event.pointerId !== press.pointerId) return;
      const longPressFired = Date.now() <= suppressUntil;
      stopTracking();
      if (!longPressFired) removeSuppressionListeners();
    }

    function handlePointerDown(event: PointerEvent) {
      suppressUntil = 0;
      stopTracking();
      removeSuppressionListeners();
      if (event.pointerType !== "touch" && event.pointerType !== "pen") return;

      press = { pointerId: event.pointerId, clientX: event.clientX, clientY: event.clientY };
      // These listeners are only needed while a touch/pen gesture is active
      // and for the short suppression window after a successful hold. Keeping
      // them off every idle card avoids hundreds of redundant listeners on
      // large home screens, which is especially noticeable in mobile Firefox.
      installSuppressionListeners();
      // Movement can leave the card (a swipe), so the follow-up listeners live
      // on the window rather than the element.
      window.addEventListener("pointermove", handlePointerMove);
      window.addEventListener("pointerup", handlePointerUp);
      window.addEventListener("pointercancel", handlePointerUp);

      timer = window.setTimeout(() => {
        timer = null;
        press = null;
        suppressUntil = Date.now() + LONG_PRESS_SUPPRESS_MS;
        onLongPressRef.current();
        suppressionCleanupTimer = window.setTimeout(() => {
          suppressUntil = 0;
          removeSuppressionListeners();
        }, LONG_PRESS_SUPPRESS_MS);
      }, LONG_PRESS_DELAY_MS);
    }

    function handleClickCapture(event: MouseEvent) {
      if (Date.now() > suppressUntil) return;
      suppressUntil = 0;
      event.preventDefault();
      event.stopPropagation();
      removeSuppressionListeners();
    }

    function handleContextMenu(event: Event) {
      if (!press && Date.now() > suppressUntil) return;
      event.preventDefault();
    }

    targetElement.addEventListener("pointerdown", handlePointerDown);
    return () => {
      stopTracking();
      removeSuppressionListeners();
      targetElement.removeEventListener("pointerdown", handlePointerDown);
    };
  }, [enabled, targetRef]);
}
