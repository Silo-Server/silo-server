import { carouselCardWidthPx, type PosterSize } from "@/lib/uiCustomization";

/** Slides mounted up front when the viewport width is unknown (SSR, tests). */
export const CAROUSEL_WINDOW_FALLBACK_SLIDES = 12;
/** Extra real slides past the visible edge, so a short drag never shows a gap. */
export const CAROUSEL_WINDOW_BUFFER_SLIDES = 2;
/** Slides mounted per idle callback once the first paint is done. */
export const CAROUSEL_WINDOW_IDLE_BATCH = 8;
const CAROUSEL_WINDOW_IDLE_TIMEOUT_MS = 1500;
const CAROUSEL_LG_MIN_WIDTH_PX = 1024;

/** MediaCarousel's slide gap: `gap-4 lg:gap-5`. */
function carouselSlideGapPx(viewportWidth: number): number {
  return viewportWidth >= CAROUSEL_LG_MIN_WIDTH_PX ? 20 : 16;
}

function currentViewportWidth(): number | null {
  if (typeof window === "undefined" || !(window.innerWidth > 0)) return null;
  return window.innerWidth;
}

/**
 * How many carousel cards of this size fit across the viewport. The viewport
 * width overestimates the row (sidebar, page padding), which errs toward
 * mounting a card too many rather than leaving a visible slide empty.
 */
export function visibleCarouselSlideCount(
  size: PosterSize | "wide",
  viewportWidth: number | null = currentViewportWidth(),
): number {
  if (viewportWidth === null) return CAROUSEL_WINDOW_FALLBACK_SLIDES;
  const slideWidth = carouselCardWidthPx(size, viewportWidth) + carouselSlideGapPx(viewportWidth);
  return Math.max(1, Math.ceil(viewportWidth / slideWidth));
}

/** The cards a windowed carousel row mounts in its first commit. */
export function initialCarouselSlideCount(
  size: PosterSize | "wide",
  viewportWidth: number | null = currentViewportWidth(),
): number {
  if (viewportWidth === null) return CAROUSEL_WINDOW_FALLBACK_SLIDES;
  return visibleCarouselSlideCount(size, viewportWidth) + CAROUSEL_WINDOW_BUFFER_SLIDES;
}

/** Runs `task` in the next idle window (or after a timeout at the latest). */
export function scheduleCarouselIdleTask(task: () => void): () => void {
  if (typeof globalThis.requestIdleCallback === "function") {
    const handle = globalThis.requestIdleCallback(() => task(), {
      timeout: CAROUSEL_WINDOW_IDLE_TIMEOUT_MS,
    });
    return () => globalThis.cancelIdleCallback?.(handle);
  }
  // Safari has no requestIdleCallback; a short timer still yields to input
  // and paint between batches.
  const handle = globalThis.setTimeout(task, 16);
  return () => globalThis.clearTimeout(handle);
}
