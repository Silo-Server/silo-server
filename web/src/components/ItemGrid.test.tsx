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

  // Cards report their natural height; the grid cell around them doesn't.
  function mockCardHeights(height: () => number) {
    vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (
      this: HTMLElement,
    ) {
      return this.parentElement?.getAttribute("role") === "listitem" ? height() : 0;
    });
  }

  it("reserves the rendered CSS gap when larger text scales rem spacing", () => {
    mockCardHeights(() => 328);
    const getStyle = window.getComputedStyle;
    vi.spyOn(window, "getComputedStyle").mockImplementation((element) => {
      const style = getStyle(element);
      Object.defineProperty(style, "rowGap", { value: "15px" });
      return style;
    });

    const { getByRole } = render(<ItemGrid items={items} />);

    expect(mocks.estimateSizes.at(-1)).toBe(328 + 1 + 15);
    expect(getByRole("list").style.gridAutoRows).toBe("329px");
  });

  it("never lowers the measured pitch, so mixed card heights can't oscillate", () => {
    let height = 328;
    mockCardHeights(() => height);

    const { rerender, getByRole } = render(<ItemGrid items={items} />);
    expect(mocks.estimateSizes[0]).toBe(ESTIMATED_ROW_HEIGHT);
    expect(mocks.estimateSizes.at(-1)).toBe(328 + 1 + GAP);
    expect(getByRole("list").style.gridAutoRows).toBe("329px");

    // Shorter cards in a later range must not shrink the reserved height.
    height = 308;
    rerender(<ItemGrid items={[...items]} />);
    expect(mocks.estimateSizes.at(-1)).toBe(341);
  });

  it("keeps the estimate when nothing has been measured", () => {
    render(<ItemGrid items={items} />);

    expect(new Set(mocks.estimateSizes)).toEqual(new Set([ESTIMATED_ROW_HEIGHT]));
  });
});
