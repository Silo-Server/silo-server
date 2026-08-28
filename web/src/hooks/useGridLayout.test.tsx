import { act, render, screen } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useGridLayout } from "./useGridLayout";

let notifyResize: ResizeObserverCallback | undefined;
let containerWidth = 400;

class ResizeObserverStub {
  constructor(callback: ResizeObserverCallback) {
    notifyResize = callback;
  }

  observe() {}
  disconnect() {}
}

function Harness() {
  const mounted = useRef(false);
  const { containerRef, layout } = useGridLayout({ gap: 10, textAreaHeight: 20 });

  return (
    <div>
      <output>{`${layout.columnCount}:${layout.rowHeight}`}</output>
      <div
        ref={(element) => {
          containerRef.current = element;
          if (element && !mounted.current) {
            Object.defineProperty(element, "clientWidth", {
              configurable: true,
              get: () => containerWidth,
            });
            mounted.current = true;
          }
        }}
        style={{ display: "grid", gridTemplateColumns: "100px 100px" }}
      />
    </div>
  );
}

describe("useGridLayout", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.stubGlobal("ResizeObserver", ResizeObserverStub);
    containerWidth = 400;
    notifyResize = undefined;
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("reconciles virtual row geometry once a continuous resize settles", () => {
    render(<Harness />);
    expect(screen.getByRole("status")).toHaveTextContent("2:322.5");

    containerWidth = 360;
    act(() => {
      notifyResize?.([], {} as ResizeObserver);
      vi.advanceTimersByTime(80);
      notifyResize?.([], {} as ResizeObserver);
      vi.advanceTimersByTime(119);
    });
    expect(screen.getByRole("status")).toHaveTextContent("2:322.5");

    act(() => vi.advanceTimersByTime(1));
    expect(screen.getByRole("status")).toHaveTextContent("2:292.5");
  });
});
