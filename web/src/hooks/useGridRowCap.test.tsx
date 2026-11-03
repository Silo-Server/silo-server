import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useGridRowCap } from "./useGridRowCap";

const observedNodes: Element[] = [];
let resizeCallback: (() => void) | null = null;

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

/** A grid whose cards are keyed like the episode grid's, by content id. */
function KeyedHarness({ ids }: { ids: string[] }) {
  const setGrid = useGridRowCap<HTMLDivElement>(4, ids.length);
  return (
    <div data-testid="grid" ref={setGrid}>
      {ids.map((id) => (
        <div key={id}>{id}</div>
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
    resizeCallback = null;
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(callback: () => void) {
          resizeCallback = callback;
        }
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

  it("caps by row rather than by item count, whatever the column count", () => {
    // The same cap in a two-column layout still lands on the fourth row, so a
    // breakpoint change re-measures to a new height rather than a new row.
    const wide = render(<Harness visibleRows={4} tops={grid(6, 5)} />);
    expect(wide.getByTestId("grid").style.maxHeight).toBe("388px");
    wide.unmount();

    const narrow = render(<Harness visibleRows={4} tops={grid(6, 2)} />);
    expect(narrow.getByTestId("grid").style.maxHeight).toBe("388px");
  });

  it("re-measures when a card changes height but the episode count does not", () => {
    // Descriptions appearing once spoiler settings load make every row taller
    // while the capped grid keeps its box, so only the cards report a resize.
    const tops = grid(6, 3);
    render(<Harness visibleRows={4} tops={tops} />);
    const element = screen.getByTestId("grid");
    for (const child of element.children) expect(observedNodes).toContain(child);
    expect(element.style.maxHeight).toBe("388px");

    tops.splice(
      0,
      tops.length,
      ...Array.from({ length: 18 }, (_, i) => 4 + Math.floor(i / 3) * 140),
    );
    resizeCallback?.();
    expect(element.style.maxHeight).toBe("548px");
  });

  it("observes the new cards when a list with the same count replaces the old one", async () => {
    // A cached season with as many episodes as the last swaps every card while
    // the grid stays mounted; the old cards are gone, so the new ones must be
    // watched for the description-loading resize above to reach the cap.
    const { rerender } = render(<KeyedHarness ids={["a1", "a2", "a3"]} />);
    rerender(<KeyedHarness ids={["b1", "b2", "b3"]} />);
    await Promise.resolve();
    for (const child of screen.getByTestId("grid").children) {
      expect(observedNodes).toContain(child);
    }
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
