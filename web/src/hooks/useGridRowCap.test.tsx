import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useGridRowCap } from "./useGridRowCap";

const observedNodes: Element[] = [];

/**
 * jsdom performs no layout, so every offsetTop is 0. Each test supplies the row
 * offsets it wants the grid to appear to have; the hook's contract is entirely
 * about how it reads those offsets, not about producing them.
 */
function Harness({
  visibleRows,
  tops,
  rendered = true,
}: {
  visibleRows: number;
  tops: number[];
  rendered?: boolean;
}) {
  const setGrid = useGridRowCap<HTMLDivElement>(visibleRows, rendered ? tops.length : 0);

  // Mirrors the real caller, which renders a skeleton until episodes arrive.
  if (!rendered) return <div>loading</div>;

  return (
    <div
      data-testid="grid"
      ref={(element) => {
        if (element) {
          Array.from(element.children).forEach((child, index) => {
            Object.defineProperty(child, "offsetTop", {
              configurable: true,
              get: () => tops[index] ?? 0,
            });
          });
        }
        setGrid(element);
      }}
      style={{ rowGap: "16px", paddingTop: "4px", paddingBottom: "0px" }}
    >
      {tops.map((_, index) => (
        <div key={index}>item {index}</div>
      ))}
    </div>
  );
}

/** `rows` rows of `columns` items, each row 100px below the last. */
function grid(rows: number, columns: number): number[] {
  return Array.from({ length: rows * columns }, (_, i) => 4 + Math.floor(i / columns) * 100);
}

describe("useGridRowCap", () => {
  beforeEach(() => {
    observedNodes.length = 0;
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe(node: Element) {
          observedNodes.push(node);
        }
        disconnect() {}
      },
    );
  });

  afterEach(() => vi.unstubAllGlobals());

  it("leaves a grid shorter than the cap uncapped", () => {
    // Four rows of three, capped at four rows: nothing is hidden, so the
    // section keeps its natural height instead of gaining an inert scrollport.
    render(<Harness visibleRows={4} tops={grid(4, 3)} />);
    expect(screen.getByTestId("grid").style.maxHeight).toBe("");
  });

  it("caps at the last visible row, excluding the gap before the hidden one", () => {
    // A fifth row starts at 404. Showing exactly four rows means stopping at
    // the bottom of row four — 404 minus the 16px gap — plus the 4px of top
    // padding that keeps the hover lift from being clipped.
    render(<Harness visibleRows={4} tops={grid(5, 3)} />);
    expect(screen.getByTestId("grid").style.maxHeight).toBe("388px");
  });

  it("caps by row rather than by item count, whatever the column count", () => {
    // The same cap in a two-column layout still lands on the fourth row, so a
    // breakpoint change re-measures to a new height rather than a new row.
    const wide = render(<Harness visibleRows={4} tops={grid(6, 5)} />);
    expect(wide.getByTestId("grid").style.maxHeight).toBe("388px");
    wide.unmount();

    const narrow = render(<Harness visibleRows={4} tops={grid(6, 2)} />);
    expect(narrow.getByTestId("grid").style.maxHeight).toBe("388px");
  });

  it("observes a grid that only mounts after loading finishes", () => {
    // Regression: binding the observer in an effect attached it to a null ref
    // while the skeleton was showing and never retried, so the cap froze at
    // whatever the first layout produced and ignored every later resize.
    const { rerender } = render(<Harness visibleRows={4} tops={grid(5, 3)} rendered={false} />);
    expect(observedNodes).toHaveLength(0);

    rerender(<Harness visibleRows={4} tops={grid(5, 3)} rendered />);
    expect(observedNodes).toContain(screen.getByTestId("grid"));
  });
});
