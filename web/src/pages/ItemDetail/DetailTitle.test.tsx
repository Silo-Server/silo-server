import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import DetailTitle from "./DetailTitle";

afterEach(() => {
  cleanup();
  delete document.documentElement.dataset.textScale;
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

it("refits when text scaling changes without a viewport resize", async () => {
  vi.useFakeTimers();
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
    },
  );
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) =>
    setTimeout(() => callback(0), 0),
  );
  vi.stubGlobal("cancelAnimationFrame", clearTimeout);
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(350);
  const computedStyle = window.getComputedStyle;
  vi.spyOn(window, "getComputedStyle").mockImplementation((element) => {
    if (element.tagName !== "H1") return computedStyle(element);
    return {
      fontSize: document.documentElement.dataset.textScale === "x-large" ? "25px" : "22.5px",
      letterSpacing: "0px",
      fontFamily: "sans-serif",
      fontWeight: "800",
      fontStyle: "normal",
    } as CSSStyleDeclaration;
  });
  render(<DetailTitle title="Light" className="" bounded />);
  expect(screen.getByRole("heading").style.fontSize).toBe("22.5px");
  await act(async () => {
    document.documentElement.dataset.textScale = "x-large";
    await Promise.resolve();
    vi.runOnlyPendingTimers();
  });
  expect(screen.getByRole("heading").style.fontSize).toBe("25px");
});
