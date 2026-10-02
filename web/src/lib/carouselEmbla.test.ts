import type { EmblaCarouselType } from "embla-carousel";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  CAROUSEL_RESIZE_SETTLE_DELAY_MS,
  createCarouselResizeController,
  getCarouselEmblaOptions,
  getCarouselWheelGestureOptions,
} from "./carouselEmbla";

describe("carouselEmbla", () => {
  it("enables drag-free momentum with trimmed edge scrolling", () => {
    expect(getCarouselEmblaOptions()).toMatchObject({
      align: "start",
      containScroll: "trimSnaps",
      dragFree: true,
    });
  });

  it("forces wheel gestures onto the horizontal axis", () => {
    const target = { nodeName: "DIV" } as unknown as HTMLElement;

    expect(getCarouselWheelGestureOptions(target)).toMatchObject({
      forceWheelAxis: "x",
      target,
    });
  });

  describe("resize controller", () => {
    beforeEach(() => vi.useFakeTimers());
    afterEach(() => vi.useRealTimers());

    function createApi() {
      const container = document.createElement("div");
      const reInit = vi.fn();
      const api = {
        containerNode: () => container,
        reInit,
      } as unknown as EmblaCarouselType;
      return { api, container, reInit };
    }

    function resizeEntry(target: Element, width = 0): ResizeObserverEntry {
      return {
        target,
        contentRect: { width },
      } as ResizeObserverEntry;
    }

    it("coalesces continuous container resizing into one settled reInit", () => {
      const controller = createCarouselResizeController();
      const { api, container, reInit } = createApi();

      expect(controller.watchResize(api, [resizeEntry(container, 900)])).toBe(true);
      vi.advanceTimersByTime(CAROUSEL_RESIZE_SETTLE_DELAY_MS);
      expect(reInit).not.toHaveBeenCalled();

      expect(controller.watchResize(api, [resizeEntry(container, 860)])).toBe(false);
      vi.advanceTimersByTime(CAROUSEL_RESIZE_SETTLE_DELAY_MS - 20);
      expect(controller.watchResize(api, [resizeEntry(container, 820)])).toBe(false);
      vi.advanceTimersByTime(CAROUSEL_RESIZE_SETTLE_DELAY_MS - 1);
      expect(reInit).not.toHaveBeenCalled();

      vi.advanceTimersByTime(1);
      expect(reInit).toHaveBeenCalledTimes(1);
    });

    it("lets Embla immediately process an isolated slide-content resize", () => {
      const controller = createCarouselResizeController();
      const { api } = createApi();
      const slide = document.createElement("article");

      expect(controller.watchResize(api, [resizeEntry(slide)])).toBe(true);
    });

    it("folds slide observations into an active container resize", () => {
      const controller = createCarouselResizeController();
      const { api, container, reInit } = createApi();
      const slide = document.createElement("article");

      controller.watchResize(api, [resizeEntry(container, 900)]);
      controller.watchResize(api, [resizeEntry(container, 860)]);
      vi.advanceTimersByTime(20);
      expect(controller.watchResize(api, [resizeEntry(slide)])).toBe(false);
      vi.advanceTimersByTime(CAROUSEL_RESIZE_SETTLE_DELAY_MS);

      expect(reInit).toHaveBeenCalledTimes(1);
    });

    it("cancels a pending reInit during teardown", () => {
      const controller = createCarouselResizeController();
      const { api, container, reInit } = createApi();

      controller.watchResize(api, [resizeEntry(container, 900)]);
      controller.watchResize(api, [resizeEntry(container, 860)]);
      controller.cancel();
      vi.advanceTimersByTime(CAROUSEL_RESIZE_SETTLE_DELAY_MS);

      expect(reInit).not.toHaveBeenCalled();
    });

    it("does not reinitialize again for the unchanged observer delivery after reInit", () => {
      const controller = createCarouselResizeController();
      const { api, container, reInit } = createApi();
      reInit.mockImplementation(() => {
        expect(controller.watchResize(api, [resizeEntry(container, 860)])).toBe(true);
      });

      controller.watchResize(api, [resizeEntry(container, 900)]);
      controller.watchResize(api, [resizeEntry(container, 860)]);
      vi.advanceTimersByTime(CAROUSEL_RESIZE_SETTLE_DELAY_MS);
      vi.advanceTimersByTime(CAROUSEL_RESIZE_SETTLE_DELAY_MS * 3);

      expect(reInit).toHaveBeenCalledTimes(1);
    });

    it("accumulates subpixel width changes against the last accepted size", () => {
      const controller = createCarouselResizeController();
      const { api, container, reInit } = createApi();

      controller.watchResize(api, [resizeEntry(container, 900)]);
      controller.watchResize(api, [resizeEntry(container, 899.7)]);
      controller.watchResize(api, [resizeEntry(container, 899.4)]);
      vi.advanceTimersByTime(CAROUSEL_RESIZE_SETTLE_DELAY_MS);

      expect(reInit).toHaveBeenCalledTimes(1);
    });
  });
});
