import type { RefObject } from "react";
import { useCallback, useEffect, useRef } from "react";

/**
 * Drag auto-scroll for the dashboard grid.
 *
 * Native HTML5 drag-and-drop does not auto-scroll inner scroll containers (and
 * browser window auto-scroll is unreliable), so while a widget drag is active
 * the grid scrolls its own container: when the pointer sits within an edge
 * zone of the visible scroll area, a requestAnimationFrame loop nudges the
 * container every frame, faster the closer the pointer is to the edge.
 */

/** How far from a scroll edge the pointer starts auto-scrolling. */
export const EDGE_ZONE_PX = 80;
/** Fastest the loop will scroll, in pixels per frame, so it stays controllable. */
export const MAX_SCROLL_SPEED_PX = 24;

/**
 * Signed scroll speed for a pointer at `clientY` over a visible scroll area
 * spanning `top`..`bottom` (viewport coordinates).
 *
 * Zero outside the edge zones; inside a zone the speed ramps linearly from 0
 * at the zone boundary to `maxSpeed` at (or past) the edge. Negative means
 * scroll up (pointer near the top), positive means scroll down. The zone
 * shrinks for short containers so the two zones never overlap.
 */
export function edgeScrollSpeed(
  clientY: number,
  top: number,
  bottom: number,
  zone: number = EDGE_ZONE_PX,
  maxSpeed: number = MAX_SCROLL_SPEED_PX,
): number {
  const height = bottom - top;
  if (height <= 0) return 0;
  const effectiveZone = Math.min(zone, height / 3);
  if (effectiveZone <= 0) return 0;
  const ramp = (distance: number) =>
    maxSpeed * (1 - Math.min(Math.max(distance, 0), effectiveZone) / effectiveZone);
  const topDistance = clientY - top;
  const bottomDistance = bottom - clientY;
  if (topDistance < effectiveZone && topDistance <= bottomDistance) {
    return -ramp(topDistance);
  }
  if (bottomDistance < effectiveZone) {
    return ramp(bottomDistance);
  }
  return 0;
}

/**
 * The nearest ancestor that actually scrolls vertically, or the document's
 * scrolling element when nothing between `start` and the root does. The admin
 * layout scrolls on the document today, but an inner `overflow-y: auto` region
 * (a future layout, an embedding) resolves the same way.
 */
export function findScrollContainer(start: HTMLElement | null): Element | null {
  for (let el = start?.parentElement ?? null; el; el = el.parentElement) {
    const overflowY = window.getComputedStyle(el).overflowY;
    const scrollable = overflowY === "auto" || overflowY === "scroll" || overflowY === "overlay";
    if (scrollable && el.scrollHeight > el.clientHeight) {
      return el;
    }
  }
  return document.scrollingElement;
}

/**
 * The container's visible span in viewport coordinates — the edges the pointer
 * is measured against. For the document's scrolling element that is the
 * viewport itself; for an inner container it is its rect clipped to the
 * viewport.
 */
export function scrollViewportBounds(container: Element): { top: number; bottom: number } {
  if (
    container === document.scrollingElement ||
    container === document.documentElement ||
    container === document.body
  ) {
    return { top: 0, bottom: window.innerHeight };
  }
  const rect = container.getBoundingClientRect();
  return { top: Math.max(rect.top, 0), bottom: Math.min(rect.bottom, window.innerHeight) };
}

interface AutoScrollState {
  raf: number | null;
  clientY: number;
  container: Element | null;
  onScrolled: ((dy: number) => void) | null;
}

/**
 * The stateful half: `update(clientY)` on every dragover (or pointermove), and
 * `stop()` on every way a drag can end. The first update inside an edge zone
 * starts the RAF loop; the loop stops itself the moment the pointer leaves the
 * zones, and `stop` (also run on unmount) kills it from the outside.
 *
 * Scrolling happens in the loop, not in the event handler — dragover fires
 * continuously and only the latest `clientY` matters. `onScrolled` reports
 * each frame's actual scroll delta so a pointer-anchored session (resize) can
 * compensate for the content moving under a stationary pointer.
 */
export function useDragAutoScroll(anchorRef: RefObject<HTMLElement | null>) {
  const stateRef = useRef<AutoScrollState>({
    raf: null,
    clientY: 0,
    container: null,
    onScrolled: null,
  });

  const stop = useCallback(() => {
    const state = stateRef.current;
    if (state.raf !== null) {
      cancelAnimationFrame(state.raf);
      state.raf = null;
    }
    state.container = null;
    state.onScrolled = null;
  }, []);

  const update = useCallback(
    (clientY: number, onScrolled?: (dy: number) => void) => {
      const state = stateRef.current;
      state.clientY = clientY;
      state.onScrolled = onScrolled ?? null;
      if (state.raf !== null) return;

      const container = findScrollContainer(anchorRef.current);
      if (!container) return;
      const { top, bottom } = scrollViewportBounds(container);
      if (edgeScrollSpeed(clientY, top, bottom) === 0) return;
      state.container = container;

      const step = () => {
        const current = stateRef.current;
        const target = current.container;
        if (!target) {
          current.raf = null;
          return;
        }
        const bounds = scrollViewportBounds(target);
        const speed = edgeScrollSpeed(current.clientY, bounds.top, bounds.bottom);
        if (speed === 0) {
          // Left the edge zones; the next qualifying dragover restarts the loop.
          current.raf = null;
          current.container = null;
          return;
        }
        const before = target.scrollTop;
        target.scrollTop = before + speed;
        const dy = target.scrollTop - before;
        if (dy !== 0) current.onScrolled?.(dy);
        current.raf = requestAnimationFrame(step);
      };
      state.raf = requestAnimationFrame(step);
    },
    [anchorRef],
  );

  // A drag interrupted by unmount must not leave the loop running.
  useEffect(() => stop, [stop]);

  return { update, stop };
}
