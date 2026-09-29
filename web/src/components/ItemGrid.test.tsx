import { cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { BrowseItem } from "@/api/types";
import ItemGrid from "./ItemGrid";

const mocks = vi.hoisted(() => ({
  estimateSizes: [] as number[],
}));

// The estimate the grid layout computes before anything is measured.
const ESTIMATED_ROW_HEIGHT = 300;
const GAP = 12;

vi.mock("@tanstack/react-virtual", () => ({
  useWindowVirtualizer: (options: { count: number; estimateSize: () => number }) => {
    const size = options.estimateSize();
    mocks.estimateSizes.push(size);
    const rows = Array.from({ length: options.count }, (_, index) => ({
      index,
      start: index * size,
      size,
      key: index,
    }));
    return {
      getVirtualItems: () => rows,
      getTotalSize: () => options.count * size,
      measure: () => undefined,
    };
  },
}));

vi.mock("@/hooks/useGridLayout", async () => {
  const { useRef } = await import("react");
  return {
    useGridLayout: () => ({
      containerRef: useRef<HTMLDivElement | null>(null),
      layout: { columnCount: 2, rowHeight: ESTIMATED_ROW_HEIGHT },
    }),
  };
});
vi.mock("@/hooks/useOverlayPrefs", () => ({
  useOverlayPrefs: () => ({ prefs: {}, quickActionMode: "hover" }),
}));
vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({
    cardPresentation: { poster_size: "medium", caption: "title_metadata" },
  }),
}));
vi.mock("./ItemCard", () => ({ default: () => <div>card</div> }));

const items = Array.from({ length: 6 }, (_, i) => ({ content_id: `item-${i}` }) as BrowseItem);

describe("ItemGrid row pitch", () => {
  beforeEach(() => {
    mocks.estimateSizes = [];
  });
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("sizes rows from the rendered grid, not the caption estimate (#1613)", () => {
    // Three rendered rows at a real pitch of 340px (captions taller than the
    // estimate): the grid is 3 * 340 - gap tall.
    const realPitch = 340;
    vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (
      this: HTMLElement,
    ) {
      return this.getAttribute("role") === "list" ? 3 * realPitch - GAP : 0;
    });

    render(<ItemGrid items={items} />);

    expect(mocks.estimateSizes[0]).toBe(ESTIMATED_ROW_HEIGHT);
    expect(mocks.estimateSizes.at(-1)).toBe(realPitch);
  });

  it("never lowers the measured pitch, so mixed row heights can't oscillate", () => {
    let pitch = 340;
    vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (
      this: HTMLElement,
    ) {
      return this.getAttribute("role") === "list" ? 3 * pitch - GAP : 0;
    });

    const { rerender } = render(<ItemGrid items={items} />);
    expect(mocks.estimateSizes.at(-1)).toBe(340);

    // A shorter window of rows must not shrink the reserved height.
    pitch = 320;
    rerender(<ItemGrid items={[...items]} />);
    expect(mocks.estimateSizes.at(-1)).toBe(340);
  });

  it("keeps the estimate when nothing has been measured", () => {
    render(<ItemGrid items={items} />);

    expect(new Set(mocks.estimateSizes)).toEqual(new Set([ESTIMATED_ROW_HEIGHT]));
  });
});
