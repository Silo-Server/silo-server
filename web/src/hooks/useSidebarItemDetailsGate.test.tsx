import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useSidebarItemDetailsGate } from "./useSidebarItemDetailsGate";

describe("useSidebarItemDetailsGate", () => {
  it("gates every non-item to item entry, including re-entering a history entry", () => {
    const { result, rerender } = renderHook(
      ({
        locationKey,
        pathname,
        isItem,
      }: {
        locationKey: string;
        pathname: string;
        isItem: boolean;
      }) => useSidebarItemDetailsGate(locationKey, pathname, isItem),
      { initialProps: { locationKey: "catalog", pathname: "/catalog", isItem: false } },
    );

    rerender({ locationKey: "movie", pathname: "/item/movie", isItem: true });
    expect(result.current.itemDetailsReady).toBe(false);

    act(() => result.current.reveal("movie"));
    expect(result.current.itemDetailsReady).toBe(true);

    rerender({ locationKey: "catalog", pathname: "/catalog", isItem: false });
    rerender({ locationKey: "movie", pathname: "/item/movie", isItem: true });
    expect(result.current.itemDetailsReady).toBe(false);
  });

  it("does not gate a direct initial item render", () => {
    const { result } = renderHook(() => useSidebarItemDetailsGate("movie", "/item/movie", true));

    expect(result.current.itemDetailsReady).toBe(true);
    expect(result.current.pendingLocationKey).toBeNull();
    expect(result.current.enteredItemFromHome).toBe(false);
  });

  it("remembers a Home item entry after its detail gate settles", () => {
    const { result, rerender } = renderHook(
      ({
        locationKey,
        pathname,
        isItem,
      }: {
        locationKey: string;
        pathname: string;
        isItem: boolean;
      }) => useSidebarItemDetailsGate(locationKey, pathname, isItem),
      { initialProps: { locationKey: "home", pathname: "/", isItem: false } },
    );

    rerender({ locationKey: "movie", pathname: "/item/movie", isItem: true });
    expect(result.current.enteredItemFromHome).toBe(true);
    expect(result.current.animateHomeItemEntry).toBe(true);

    rerender({ locationKey: "movie", pathname: "/item/movie", isItem: true });
    expect(result.current.enteredItemFromHome).toBe(true);

    act(() => result.current.reveal("movie"));
    expect(result.current.enteredItemFromHome).toBe(true);

    rerender({ locationKey: "home-again", pathname: "/", isItem: false });
    expect(result.current.enteredItemFromHome).toBe(false);
    expect(result.current.returnedHomeFromItem).toBe(true);

    rerender({ locationKey: "settings", pathname: "/settings", isItem: false });
    expect(result.current.returnedHomeFromItem).toBe(false);
  });

  it("renders an item that was cached before navigation without staging its Home entry", () => {
    const { result, rerender } = renderHook(
      ({ locationKey, pathname, isItem, availableOnEntry }) =>
        useSidebarItemDetailsGate(locationKey, pathname, isItem, {
          itemRouteLocation: pathname,
          itemDetailsAvailableOnEntry: availableOnEntry,
        }),
      {
        initialProps: {
          locationKey: "home",
          pathname: "/",
          isItem: false,
          availableOnEntry: false,
        },
      },
    );

    act(() => result.current.prepareItemNavigation("/item/movie", true));

    rerender({
      locationKey: "movie",
      pathname: "/item/movie",
      isItem: true,
      availableOnEntry: false,
    });

    expect(result.current.itemDetailsReady).toBe(true);
    expect(result.current.pendingLocationKey).toBeNull();
    expect(result.current.enteredItemFromHome).toBe(true);
    expect(result.current.animateHomeItemEntry).toBe(false);

    // Availability is an entry-time snapshot. A later render cannot replay
    // the gate or the animation for the already committed route.
    rerender({
      locationKey: "movie",
      pathname: "/item/movie",
      isItem: true,
      availableOnEntry: false,
    });
    expect(result.current.itemDetailsReady).toBe(true);
    expect(result.current.animateHomeItemEntry).toBe(false);

    rerender({
      locationKey: "home-again",
      pathname: "/",
      isItem: false,
      availableOnEntry: false,
    });
    expect(result.current.returnedHomeFromItem).toBe(true);
  });

  it("keeps the Home return origin across item-to-item navigation without replaying entry state", () => {
    const { result, rerender } = renderHook(
      ({ locationKey, pathname, isItem }) =>
        useSidebarItemDetailsGate(locationKey, pathname, isItem),
      { initialProps: { locationKey: "home", pathname: "/", isItem: false } },
    );

    rerender({ locationKey: "movie-a", pathname: "/item/movie-a", isItem: true });
    expect(result.current.enteredItemFromHome).toBe(true);

    rerender({ locationKey: "movie-b", pathname: "/item/movie-b", isItem: true });
    expect(result.current.itemDetailsReady).toBe(true);
    expect(result.current.enteredItemFromHome).toBe(false);
    expect(result.current.returnedHomeFromItem).toBe(false);

    rerender({ locationKey: "home-again", pathname: "/", isItem: false });
    expect(result.current.returnedHomeFromItem).toBe(true);
  });

  it("does not mark non-Home item exits or unrelated Home arrivals as item returns", () => {
    const { result, rerender } = renderHook(
      ({ locationKey, pathname, isItem }) =>
        useSidebarItemDetailsGate(locationKey, pathname, isItem),
      {
        initialProps: { locationKey: "library", pathname: "/library/1", isItem: false },
      },
    );

    rerender({ locationKey: "movie", pathname: "/item/movie", isItem: true });
    rerender({ locationKey: "episode", pathname: "/item/episode", isItem: true });
    expect(result.current.enteredItemFromHome).toBe(false);
    rerender({ locationKey: "home", pathname: "/", isItem: false });
    expect(result.current.returnedHomeFromItem).toBe(false);

    rerender({ locationKey: "settings", pathname: "/settings", isItem: false });
    rerender({ locationKey: "home-direct", pathname: "/", isItem: false });
    expect(result.current.returnedHomeFromItem).toBe(false);
  });

  it("tracks repeated same-item and different-item Home cycles without retaining origin state", () => {
    const { result, rerender } = renderHook(
      ({ locationKey, pathname, isItem }) =>
        useSidebarItemDetailsGate(locationKey, pathname, isItem),
      { initialProps: { locationKey: "home-0", pathname: "/", isItem: false } },
    );

    for (let cycle = 1; cycle <= 24; cycle++) {
      const itemId = cycle <= 12 ? "same" : `different-${cycle}`;
      rerender({ locationKey: `item-${cycle}`, pathname: `/item/${itemId}`, isItem: true });
      expect(result.current.enteredItemFromHome).toBe(true);
      expect(result.current.returnedHomeFromItem).toBe(false);

      rerender({ locationKey: `home-${cycle}`, pathname: "/", isItem: false });
      expect(result.current.enteredItemFromHome).toBe(false);
      expect(result.current.returnedHomeFromItem).toBe(true);
    }

    rerender({ locationKey: "library", pathname: "/library/1", isItem: false });
    expect(result.current.returnedHomeFromItem).toBe(false);
  });

  it("does not hold cached details for non-Home item entries", () => {
    const { result, rerender } = renderHook(
      ({
        locationKey,
        pathname,
        isItem,
      }: {
        locationKey: string;
        pathname: string;
        isItem: boolean;
      }) => useSidebarItemDetailsGate(locationKey, pathname, isItem),
      { initialProps: { locationKey: "library", pathname: "/library/1", isItem: false } },
    );

    rerender({ locationKey: "movie", pathname: "/item/movie", isItem: true });

    expect(result.current.enteredItemFromHome).toBe(false);
  });

  it("discards an abandoned gate and allows the next item entry", () => {
    const { result, rerender } = renderHook(
      ({
        locationKey,
        pathname,
        isItem,
      }: {
        locationKey: string;
        pathname: string;
        isItem: boolean;
      }) => useSidebarItemDetailsGate(locationKey, pathname, isItem),
      { initialProps: { locationKey: "catalog", pathname: "/catalog", isItem: false } },
    );

    rerender({ locationKey: "abandoned-movie", pathname: "/item/abandoned", isItem: true });
    expect(result.current.itemDetailsReady).toBe(false);

    rerender({ locationKey: "search", pathname: "/catalog", isItem: false });
    expect(result.current.itemDetailsReady).toBe(true);

    rerender({ locationKey: "next-movie", pathname: "/item/next", isItem: true });
    expect(result.current.itemDetailsReady).toBe(false);

    act(() => result.current.reveal("abandoned-movie"));
    expect(result.current.itemDetailsReady).toBe(false);

    act(() => result.current.reveal("next-movie"));
    expect(result.current.itemDetailsReady).toBe(true);
  });
});
