import { act, renderHook } from "@testing-library/react";
import type { EmblaCarouselType } from "embla-carousel";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  useEmblaCarousel: vi.fn(),
  wheelGesturesPlugin: vi.fn(() => ({ name: "wheelGestures" })),
}));

vi.mock("embla-carousel-react", () => ({ default: mocks.useEmblaCarousel }));
vi.mock("embla-carousel-wheel-gestures", () => ({
  WheelGesturesPlugin: mocks.wheelGesturesPlugin,
}));

import { useCarouselEmbla } from "./useCarouselEmbla";

describe("useCarouselEmbla", () => {
  let canScrollPrev = false;
  let canScrollNext = true;
  let api: EmblaCarouselType;
  let listeners: Map<string, (api: EmblaCarouselType) => void>;

  beforeEach(() => {
    canScrollPrev = false;
    canScrollNext = true;
    listeners = new Map();
    const container = document.createElement("div");
    api = {
      canScrollPrev: vi.fn(() => canScrollPrev),
      canScrollNext: vi.fn(() => canScrollNext),
      containerNode: vi.fn(() => container),
      on: vi.fn((event: string, callback: (api: EmblaCarouselType) => void) => {
        listeners.set(event, callback);
        return api;
      }),
      off: vi.fn((event: string) => {
        listeners.delete(event);
        return api;
      }),
      reInit: vi.fn(),
      scrollPrev: vi.fn(),
      scrollNext: vi.fn(),
    } as unknown as EmblaCarouselType;
    mocks.useEmblaCarousel.mockReset();
    mocks.useEmblaCarousel.mockReturnValue([vi.fn(), api]);
  });

  it("installs the coalesced resize observer and keeps arrow state current after reInit", () => {
    const { result } = renderHook(() => useCarouselEmbla());
    const emblaOptions = mocks.useEmblaCarousel.mock.calls[0]?.[0];

    expect(typeof emblaOptions.watchResize).toBe("function");
    expect(result.current.canScrollPrev).toBe(false);
    expect(result.current.canScrollNext).toBe(true);

    canScrollPrev = true;
    canScrollNext = false;
    act(() => listeners.get("reInit")?.(api));

    expect(result.current.canScrollPrev).toBe(true);
    expect(result.current.canScrollNext).toBe(false);
  });

  it("preserves a deliberate resize-observer opt-out", () => {
    renderHook(() => useCarouselEmbla({ options: { watchResize: false } }));

    expect(mocks.useEmblaCarousel.mock.calls[0]?.[0].watchResize).toBe(false);
  });
});
