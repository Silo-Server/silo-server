import type { EmblaCarouselType, EmblaOptionsType } from "embla-carousel";

export const CAROUSEL_RESIZE_SETTLE_DELAY_MS = 120;

type EmblaWatchResize = (api: EmblaCarouselType, entries: ResizeObserverEntry[]) => boolean;

export interface CarouselResizeController {
  watchResize: EmblaWatchResize;
  cancel: () => void;
}

function observedInlineSize(entry: ResizeObserverEntry): number {
  const borderBoxSize = entry.borderBoxSize;
  const borderBox = Array.isArray(borderBoxSize) ? borderBoxSize[0] : borderBoxSize;
  if (borderBox && Number.isFinite(borderBox.inlineSize)) {
    return borderBox.inlineSize;
  }

  if (entry.contentRect && Number.isFinite(entry.contentRect.width)) {
    return entry.contentRect.width;
  }

  return entry.target.getBoundingClientRect().width;
}

const CAROUSEL_EMBLA_OPTIONS: EmblaOptionsType = {
  align: "start",
  containScroll: "trimSnaps",
  dragFree: true,
  slidesToScroll: "auto",
};

export function getCarouselEmblaOptions(overrides: EmblaOptionsType = {}): EmblaOptionsType {
  return {
    ...CAROUSEL_EMBLA_OPTIONS,
    ...overrides,
  };
}

export function getCarouselWheelGestureOptions(target?: Element | null) {
  return {
    forceWheelAxis: "x" as const,
    target: target ?? undefined,
  };
}

/**
 * Keeps Embla from rebuilding its measurements on every frame of an animated
 * viewport resize. A standalone slide resize still uses Embla's immediate
 * default handler; once a container resize starts, related slide observations
 * are collected into one reInit after the layout has settled.
 */
export function createCarouselResizeController(
  settleDelayMs = CAROUSEL_RESIZE_SETTLE_DELAY_MS,
): CarouselResizeController {
  let resizeTimer: number | null = null;
  let pendingApi: EmblaCarouselType | null = null;
  const observedContainerWidths = new WeakMap<Element, number>();

  const cancel = () => {
    if (resizeTimer !== null) {
      window.clearTimeout(resizeTimer);
      resizeTimer = null;
    }
    pendingApi = null;
  };

  const scheduleReInit = (api: EmblaCarouselType) => {
    if (resizeTimer !== null) {
      window.clearTimeout(resizeTimer);
    }
    pendingApi = api;
    resizeTimer = window.setTimeout(() => {
      resizeTimer = null;
      const apiToReInit = pendingApi;
      pendingApi = null;
      apiToReInit?.reInit();
    }, settleDelayMs);
  };

  const watchResize: EmblaWatchResize = (api, entries) => {
    const container = api.containerNode();
    const containerEntry = entries.find((entry) => entry.target === container);

    if (containerEntry) {
      const nextWidth = observedInlineSize(containerEntry);
      const previousWidth = observedContainerWidths.get(container);

      // ResizeObserver delivers the current size as soon as Embla starts
      // observing. Treat that first delivery as the baseline: scheduling a
      // reInit for it creates a new observer, which delivers the same initial
      // entry and can otherwise keep the carousel rebuilding forever.
      if (previousWidth === undefined) {
        observedContainerWidths.set(container, nextWidth);
        return true;
      }

      if (Math.abs(nextWidth - previousWidth) >= 0.5) {
        observedContainerWidths.set(container, nextWidth);
        scheduleReInit(api);
        return false;
      }

      // Let Embla compare any slide entries batched with an unchanged
      // container delivery. Its own cached geometry prevents a rebuild when
      // this is merely the observer's initial notification after reInit.
      return resizeTimer === null;
    }

    if (resizeTimer !== null) return false;

    // Let Embla handle isolated slide-content changes immediately.
    return true;
  };

  return { watchResize, cancel };
}
