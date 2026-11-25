/** True when `el` has room to scroll further in the direction of `deltaY`. */
function canScrollVertically(el: HTMLElement, deltaY: number): boolean {
  if (deltaY < 0) return el.scrollTop > 0;
  if (deltaY > 0) return el.scrollTop + el.clientHeight < el.scrollHeight - 1;
  return false;
}

/**
 * Callback ref that keeps wheel input over a fixed panel from scrolling the
 * page behind it. A non-modal Radix sheet doesn't lock page scroll, and
 * `overscroll-behavior` can't help where nothing in the panel scrolls (its
 * header, or a list too short to overflow). An element inside the panel marked
 * `data-wheel-scroller` still scrolls natively while it has room in the
 * wheel's direction.
 */
export function containWheel(panel: HTMLElement | null) {
  if (!panel) return;
  const onWheel = (event: WheelEvent) => {
    // Ctrl+wheel is the browser's zoom gesture (and trackpad pinch).
    if (event.ctrlKey) return;
    const scroller = (event.target as Element).closest("[data-wheel-scroller]");
    if (
      scroller instanceof HTMLElement &&
      panel.contains(scroller) &&
      canScrollVertically(scroller, event.deltaY)
    ) {
      return;
    }
    event.preventDefault();
  };
  panel.addEventListener("wheel", onWheel, { passive: false });
  return () => panel.removeEventListener("wheel", onWheel);
}
