import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useSidebarItemDetailsGate } from "./useSidebarItemDetailsGate";

describe("useSidebarItemDetailsGate", () => {
  it("gates every non-item to item entry, including re-entering a history entry", () => {
    const { result, rerender } = renderHook(
      ({ locationKey, isItem }: { locationKey: string; isItem: boolean }) =>
        useSidebarItemDetailsGate(locationKey, isItem),
      { initialProps: { locationKey: "catalog", isItem: false } },
    );

    rerender({ locationKey: "movie", isItem: true });
    expect(result.current.itemDetailsReady).toBe(false);

    act(() => result.current.reveal("movie"));
    expect(result.current.itemDetailsReady).toBe(true);

    rerender({ locationKey: "catalog", isItem: false });
    rerender({ locationKey: "movie", isItem: true });
    expect(result.current.itemDetailsReady).toBe(false);
  });

  it("does not gate a direct initial item render", () => {
    const { result } = renderHook(() => useSidebarItemDetailsGate("movie", true));

    expect(result.current.itemDetailsReady).toBe(true);
    expect(result.current.pendingLocationKey).toBeNull();
  });

  it("discards an abandoned gate and allows the next item entry", () => {
    const { result, rerender } = renderHook(
      ({ locationKey, isItem }: { locationKey: string; isItem: boolean }) =>
        useSidebarItemDetailsGate(locationKey, isItem),
      { initialProps: { locationKey: "catalog", isItem: false } },
    );

    rerender({ locationKey: "abandoned-movie", isItem: true });
    expect(result.current.itemDetailsReady).toBe(false);

    rerender({ locationKey: "search", isItem: false });
    expect(result.current.itemDetailsReady).toBe(true);

    rerender({ locationKey: "next-movie", isItem: true });
    expect(result.current.itemDetailsReady).toBe(false);

    act(() => result.current.reveal("abandoned-movie"));
    expect(result.current.itemDetailsReady).toBe(false);

    act(() => result.current.reveal("next-movie"));
    expect(result.current.itemDetailsReady).toBe(true);
  });
});
