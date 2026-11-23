import type { RefObject } from "react";
import { useCallback, useLayoutEffect, useRef } from "react";

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
const EDGE_ZONE_PX = 80;
/** Fastest the loop will scroll, in pixels per frame, so it stays controllable. */
const MAX_SCROLL_SPEED_PX = 24;

/**
 * Signed scroll speed for a pointer at `clientY` over a visible scroll area
 * spanning `top`..`bottom` (viewport coordinates).
 *
 * Zero outside the edge zones; inside a zone the speed ramps linearly from 0
 * at the zone boundary to `maxSpeed` at (or past) the edge. Negative means
 * scroll up (pointer near the top), positive means scroll down. The zone
 * shrinks for short containers so the two zones never overlap.
 */
function edgeScrollSpeed(
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
function findScrollContainer(start: HTMLElement | null): Element | null {
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
 * Where pinned page headers (`data-sticky-header`, such as the admin phone
 * header or the impersonation banner) end at the top of the viewport right
 * now. The drag events over them go to the header, not the grid, so the top
 * edge zone has to start below them. ItemGrid's `stickyHeaderInset` reads the
 * same opt-in for where a header will sit once stuck.
 */
function pinnedHeaderBottom(): number {
  let bottom = 0;
  for (const header of document.querySelectorAll<HTMLElement>("[data-sticky-header]")) {
    const rect = header.getBoundingClientRect();
    if (rect.height > 0) bottom = Math.max(bottom, rect.bottom);
  }
  return bottom;
}

/**
 * The container's visible span in viewport coordinates — the edges the pointer
 * is measured against. For the document's scrolling element that is the
 * viewport below any pinned header; for an inner container it is its rect
 * clipped to the viewport.
 */
function scrollViewportBounds(container: Element): { top: number; bottom: number } {
  if (
    container === document.scrollingElement ||
    container === document.documentElement ||
    container === document.body
  ) {
    return { top: pinnedHeaderBottom(), bottom: window.innerHeight };
  }
  const rect = container.getBoundingClientRect();
  return { top: Math.max(rect.top, 0), bottom: Math.min(rect.bottom, window.innerHeight) };
}

interface AutoScrollState {
  raf: number | null;
  /** The pointer's latest position, or null while it is outside the window. */
  clientY: number | null;
  onScrolled: ((dy: number) => void) | null;
}

/** Cancels the next frame; the next position inside an edge zone restarts the loop. */
function pause(state: AutoScrollState) {
  if (state.raf !== null) {
    cancelAnimationFrame(state.raf);
    state.raf = null;
  }
}

/** What the auto-scroll follows: a native HTML5 drag, or a pointer-captured gesture. */
export type AutoScrollGesture = "drag" | "pointer";

/**
 * The stateful half. While `gesture` is set (a widget is being dragged or
 * resized), the RAF loop runs whenever the pointer sits in an edge zone and
 * pauses when it leaves them; when `gesture` clears, by whatever path the
 * gesture ended, the loop stops.
 *
 * A drag reports the pointer from the window: drag events over anything
 * outside the grid (the pinned phone header, the gutters, the sidebar, the
 * Add widget sheet the drag started from) never reach the grid's handlers. A
 * pointer gesture (resize), which fires no drag events, calls
 * `update(clientY)` on every pointermove instead.
 *
 * Scrolling happens in the loop, not in the event handler — dragover fires
 * continuously and only the latest `clientY` matters. `onScrolled` reports
 * each frame's actual scroll delta so a pointer-anchored session (resize) can
 * compensate for the content moving under a stationary pointer.
 */
export function useDragAutoScroll(
  anchorRef: RefObject<HTMLElement | null>,
  gesture: AutoScrollGesture | null,
) {
  const stateRef = useRef<AutoScrollState>({
    raf: null,
    clientY: null,
    onScrolled: null,
  });

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

      const step = () => {
        const bounds = scrollViewportBounds(container);
        const speed =
          state.clientY === null ? 0 : edgeScrollSpeed(state.clientY, bounds.top, bounds.bottom);
        if (speed === 0) {
          pause(state);
          return;
        }
        const before = container.scrollTop;
        container.scrollTop = before + speed;
        const dy = container.scrollTop - before;
        if (dy !== 0) state.onScrolled?.(dy);
        state.raf = requestAnimationFrame(step);
      };
      state.raf = requestAnimationFrame(step);
    },
    [anchorRef],
  );

  // A layout effect, so the loop stops in the commit that ends the gesture. A
  // gesture that ends outside an event handler (the grid's source check)
  // commits at default priority, and React can run its passive cleanup only
  // after another frame has scrolled.
  useLayoutEffect(() => {
    if (gesture === null) return;
    const state = stateRef.current;
    if (gesture === "pointer") return () => pause(state);
    const follow = (event: DragEvent) => update(event.clientY);
    // A drag that leaves the window fires a last dragleave and then no more
    // dragover, which would leave the loop scrolling on the last position.
    // Moving between elements fires dragleave too, so only one that enters no
    // element at a point outside the window counts. Chromium on Windows and
    // Linux reports that point as (0, 0).
    const leave = (event: DragEvent) => {
      const { clientX: x, clientY: y } = event;
      const outside =
        x < 0 || y < 0 || x >= window.innerWidth || y >= window.innerHeight || (x === 0 && y === 0);
      if (event.relatedTarget === null && outside) state.clientY = null;
    };
    window.addEventListener("dragover", follow, true);
    window.addEventListener("dragleave", leave, true);
    return () => {
      window.removeEventListener("dragover", follow, true);
      window.removeEventListener("dragleave", leave, true);
      pause(state);
    };
  }, [gesture, update]);

  return { update };
}
