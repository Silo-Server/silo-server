import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useAmbientColor } from "./useAmbientColor";

vi.mock("@/lib/thumbhash", () => ({
  getAverageColor: (thumbhash: string) =>
    thumbhash === "first" ? "rgb(10 20 30)" : "rgb(40 50 60)",
}));

describe("useAmbientColor", () => {
  afterEach(() => {
    document.documentElement.style.removeProperty("--ambient");
    vi.unstubAllGlobals();
  });

  it("does not clear the ambient color between replacing detail surfaces", () => {
    const cleanups: Array<() => void> = [];
    vi.stubGlobal("queueMicrotask", (cleanup: () => void) => cleanups.push(cleanup));

    const { rerender, unmount } = renderHook(
      ({ thumbhash }: { thumbhash: string }) => useAmbientColor(thumbhash),
      { initialProps: { thumbhash: "first" } },
    );

    expect(document.documentElement.style.getPropertyValue("--ambient")).toBe("rgb(10 20 30)");

    rerender({ thumbhash: "second" });
    expect(document.documentElement.style.getPropertyValue("--ambient")).toBe("rgb(40 50 60)");

    act(() => cleanups.splice(0).forEach((cleanup) => cleanup()));
    expect(document.documentElement.style.getPropertyValue("--ambient")).toBe("rgb(40 50 60)");

    unmount();
    act(() => cleanups.splice(0).forEach((cleanup) => cleanup()));
    expect(document.documentElement.style.getPropertyValue("--ambient")).toBe("");
  });
});
