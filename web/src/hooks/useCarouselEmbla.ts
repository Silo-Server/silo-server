import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { EmblaCarouselType, EmblaOptionsType } from "embla-carousel";
import useEmblaCarousel from "embla-carousel-react";
import { WheelGesturesPlugin } from "embla-carousel-wheel-gestures";
import { Direction } from "radix-ui";
import {
  createCarouselResizeController,
  getCarouselEmblaOptions,
  getCarouselWheelGestureOptions,
  wheelGesturesForDirection,
} from "@/lib/carouselEmbla";

interface UseCarouselEmblaOptions {
  options?: EmblaOptionsType;
}

export function useCarouselEmbla({ options }: UseCarouselEmblaOptions = {}) {
  // Embla reinitialises when the options change, so a language switch re-lays the slides.
  const direction = Direction.useDirection();
  const [canScrollPrev, setCanScrollPrev] = useState(false);
  const [canScrollNext, setCanScrollNext] = useState(false);
  const canScrollPrevRef = useRef(false);
  const canScrollNextRef = useRef(false);
  const resizeControllerRef = useRef<ReturnType<typeof createCarouselResizeController> | null>(
    null,
  );
  if (resizeControllerRef.current === null) {
    resizeControllerRef.current = createCarouselResizeController();
  }
  const resizeController = resizeControllerRef.current;
  const emblaOptions = useMemo(
    () =>
      getCarouselEmblaOptions({
        direction,
        ...options,
        // Preserve deliberate opt-outs and custom observers. `true` means the
        // shared default, which is the coalesced path for every Silo carousel.
        watchResize:
          options?.watchResize === false || typeof options?.watchResize === "function"
            ? options.watchResize
            : resizeController.watchResize,
      }),
    [direction, options, resizeController],
  );
  const emblaPlugins = useMemo(
    () => [wheelGesturesForDirection(WheelGesturesPlugin(getCarouselWheelGestureOptions()))],
    [],
  );
  const [emblaRef, emblaApi] = useEmblaCarousel(emblaOptions, emblaPlugins);

  const updateScrollState = useCallback((api: EmblaCarouselType) => {
    const nextCanScrollPrev = api.canScrollPrev();
    const nextCanScrollNext = api.canScrollNext();

    if (nextCanScrollPrev !== canScrollPrevRef.current) {
      canScrollPrevRef.current = nextCanScrollPrev;
      setCanScrollPrev(nextCanScrollPrev);
    }
    if (nextCanScrollNext !== canScrollNextRef.current) {
      canScrollNextRef.current = nextCanScrollNext;
      setCanScrollNext(nextCanScrollNext);
    }
  }, []);

  useEffect(() => () => resizeController.cancel(), [resizeController]);

  useEffect(() => {
    if (!emblaApi) return;

    updateScrollState(emblaApi);
    emblaApi.on("select", updateScrollState);
    emblaApi.on("reInit", updateScrollState);

    return () => {
      emblaApi.off("select", updateScrollState);
      emblaApi.off("reInit", updateScrollState);
    };
  }, [emblaApi, updateScrollState]);

  const scrollPrev = useCallback(() => {
    emblaApi?.scrollPrev();
  }, [emblaApi]);

  const scrollNext = useCallback(() => {
    emblaApi?.scrollNext();
  }, [emblaApi]);

  return {
    emblaApi,
    emblaRef,
    canScrollPrev,
    canScrollNext,
    scrollPrev,
    scrollNext,
  };
}
