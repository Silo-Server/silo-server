import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { useSidebarEntrance, useSidebarSwap } from "./useSidebarSwap";

describe("useSidebarSwap", () => {
  it("reports a swap only on the render that crosses the settings boundary", () => {
    const { result, rerender } = renderHook(({ inSettings }) => useSidebarSwap(inSettings), {
      initialProps: { inSettings: false },
    });
    // A first load has nothing to swap from.
    expect(result.current).toBeNull();

    rerender({ inSettings: false });
    expect(result.current).toBeNull();

    rerender({ inSettings: true });
    expect(result.current).toBe("forward");
    rerender({ inSettings: true });
    expect(result.current).toBeNull();

    rerender({ inSettings: false });
    expect(result.current).toBe("back");
  });

  it("remembers the last sidebar across a shell remount", () => {
    // The app shell is mounted per route branch: the one leaving settings is
    // not the one that renders the destination.
    const before = renderHook(() => useSidebarSwap(false));
    before.unmount();

    const settings = renderHook(() => useSidebarSwap(true));
    expect(settings.result.current).toBe("forward");
    settings.unmount();

    const after = renderHook(() => useSidebarSwap(false));
    expect(after.result.current).toBe("back");
  });
});

describe("useSidebarEntrance", () => {
  it("keeps the swap it mounted with when later renders report none", () => {
    const { result, rerender } = renderHook(({ swap }) => useSidebarEntrance(swap), {
      initialProps: { swap: "forward" as "forward" | null },
    });
    rerender({ swap: null });

    expect(result.current).toBe("forward");
  });
});
