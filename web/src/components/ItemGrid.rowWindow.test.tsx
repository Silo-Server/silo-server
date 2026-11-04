import { createRef } from "react";
import { act, cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { BrowseItem } from "@/api/types";
import { resetScrollRestoration } from "@/lib/scrollRestoration";
import ItemGrid, { type ItemGridHandle } from "./ItemGrid";

// The real window virtualizer, with two 300px columns: a touch grid's window
// holds 1,200,000 / 300 = 4,000 rows, a desktop grid's at a pixel ratio of 1
// 11,000,000 / 300 = 36,666.
const gridLayout = vi.hoisted(() => ({ columnCount: 2, rowHeight: 300 }));
vi.mock("@/hooks/useGridLayout", async () => {
  const { useRef } = await import("react");
  return {
    useGridLayout: () => ({
      containerRef: useRef<HTMLDivElement | null>(null),
      layout: { ...gridLayout },
    }),
  };
});
vi.mock("@/hooks/useOverlayPrefs", () => ({
  useOverlayPrefs: () => ({ prefs: {}, quickActionMode: "none" }),
}));
vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({ cardPresentation: { poster_size: "standard" } }),
}));
vi.mock("./ItemCard", () => ({
  default: ({ item }: { item: BrowseItem }) => <div data-id={item.content_id}>card</div>,
}));

const PAGE_SIZE = 100;

function pagesAround(...pageIndexes: number[]) {
  return new Map(
    pageIndexes.map((page) => [
      page,
      Array.from(
        { length: PAGE_SIZE },
        (_, i) => ({ content_id: `item-${page * PAGE_SIZE + i}` }) as BrowseItem,
      ),
    ]),
  );
}

function setScrollY(top: number) {
  Object.defineProperty(window, "scrollY", { configurable: true, value: top });
}

// The viewer scrolls: the page moves and the browser reports it.
function userScroll(top: number) {
  setScrollY(top);
  window.dispatchEvent(new Event("scroll"));
}

// The scroll position rests long enough for the row window to move.
function settle() {
  vi.advanceTimersByTime(1000);
}

function usePointer(touch: boolean) {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: touch && query === "(any-pointer: coarse)",
    media: query,
    onchange: null,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    addListener: () => undefined,
    removeListener: () => undefined,
    dispatchEvent: () => false,
  }));
}

function renderGrid(totalItems: number, pages: Map<number, BrowseItem[]>) {
  const gridRef = createRef<ItemGridHandle>();
  const onTopRowChange = vi.fn();
  const onVisibleRangeChange = vi.fn();
  const props = {
    gridRef,
    pageSize: PAGE_SIZE,
    onVisibleRangeChange,
    onTopRowChange,
  };
  const view = render(<ItemGrid {...props} totalItems={totalItems} pages={pages} />);
  const extent = () => (view.getByRole("list").parentElement as HTMLElement).style.height;
  const rendered = (id: number) => view.container.querySelector(`[data-id="item-${id}"]`) !== null;
  const rerender = (nextTotal: number) =>
    view.rerender(<ItemGrid {...props} totalItems={nextTotal} pages={pages} />);
  return { ...view, gridRef, onTopRowChange, onVisibleRangeChange, extent, rendered, rerender };
}

describe("ItemGrid row window", () => {
  let programmaticScrolls: number[];

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    programmaticScrolls = [];
    // A programmatic scroll moves the page at once; as in a browser, its
    // scroll event comes later, so nothing here delivers it.
    vi.spyOn(window, "scrollTo").mockImplementation(
      (options: ScrollToOptions | number, y?: number) => {
        const top = typeof options === "number" ? y : options.top;
        if (top === undefined) return;
        programmaticScrolls.push(top);
        setScrollY(top);
      },
    );
    // The virtualizer clamps a scroll to the document's height.
    vi.spyOn(document.documentElement, "scrollHeight", "get").mockReturnValue(100_000_000);
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(
      () => ({ top: -window.scrollY, height: 0 }) as DOMRect,
    );
    // Cards fill the 300px pitch exactly, so measuring never changes it.
    vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (
      this: HTMLElement,
    ) {
      return this.dataset.id ? 287 : 0;
    });
    userScroll(0);
    settle();
  });
  afterEach(() => {
    cleanup();
    vi.clearAllTimers();
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    resetScrollRestoration();
    setScrollY(0);
    gridLayout.columnCount = 2;
  });

  it("keeps a desktop grid at the whole list's height", () => {
    usePointer(false);
    const { gridRef, extent } = renderGrid(60_000, pagesAround(0, 500));
    expect(extent()).toBe(`${30_000 * 300}px`);

    act(() => gridRef.current?.scrollToItem(50_001));

    expect(window.scrollY).toBe(25_000 * 300);
  });

  it("windows a desktop grid taller than a browser lets an element grow", () => {
    usePointer(false);
    const { gridRef, extent, onTopRowChange, rendered } = renderGrid(100_000, pagesAround(0, 800));
    expect(extent()).toBe(`${36_666 * 300}px`);

    act(() => gridRef.current?.scrollToItem(80_001));

    // Row 40,000 is in the list's last window, which starts at row 13,334.
    expect(window.scrollY).toBe(26_666 * 300);
    expect(onTopRowChange).toHaveBeenLastCalledWith(80_000, 80_001);
    expect(rendered(80_000)).toBe(true);
  });

  it("jumps deep into a touch grid without scrolling the page deep", () => {
    usePointer(true);
    const { gridRef, extent, onTopRowChange, onVisibleRangeChange, rendered } = renderGrid(
      100_000,
      pagesAround(0, 799, 800, 801),
    );
    expect(extent()).toBe(`${4_000 * 300}px`);

    act(() => gridRef.current?.scrollToItem(80_001));

    // Row 40,000 lands in the middle of a window starting at row 38,000,
    // drawn before the jump's scroll event arrives.
    expect(window.scrollY).toBe(2_000 * 300);
    expect(rendered(80_000)).toBe(true);
    expect(onTopRowChange).toHaveBeenLastCalledWith(80_000, 80_001);
    expect(onVisibleRangeChange.mock.lastCall?.[0]).toBeLessThanOrEqual(80_000);
    expect(onVisibleRangeChange.mock.lastCall?.[1]).toBeGreaterThan(80_001);
    expect(extent()).toBe(`${4_000 * 300}px`);
  });

  it("moves the window once a scroll settles near its end, keeping the row in place", () => {
    usePointer(true);
    const { gridRef, onTopRowChange, rendered } = renderGrid(100_000, pagesAround(821, 822, 823));
    act(() => gridRef.current?.scrollToItem(80_000));

    // 17px into the window's row 3,100 (row 41,100), in its last quarter.
    act(() => userScroll(3_100 * 300 + 17));
    expect(window.scrollY).toBe(3_100 * 300 + 17);

    act(() => settle());

    // The window now starts at row 40,000: the same row is its row 1,100,
    // and its cards are drawn without waiting for the scroll event.
    expect(window.scrollY).toBe(1_100 * 300 + 17);
    expect(rendered(82_200)).toBe(true);
    expect(onTopRowChange).toHaveBeenLastCalledWith(82_200, 82_201);
  });

  it("moves the window back once a scroll settles near its start", () => {
    usePointer(true);
    const { gridRef, onTopRowChange, rendered } = renderGrid(100_000, pagesAround(777, 778));
    act(() => gridRef.current?.scrollToItem(80_000));

    // The window's row 900 (row 38,900), in its first quarter.
    act(() => userScroll(900 * 300));
    act(() => settle());

    // The window now starts at row 36,000: the same row is its row 2,900.
    expect(window.scrollY).toBe(2_900 * 300);
    expect(rendered(77_800)).toBe(true);
    expect(onTopRowChange).toHaveBeenLastCalledWith(77_800, 77_801);
  });

  it("lets a smooth scroll to the top run to the start of the list", () => {
    usePointer(true);
    const { gridRef, onTopRowChange, rendered } = renderGrid(100_000, pagesAround(0, 800));
    act(() => gridRef.current?.scrollToItem(80_000));
    const scrollsBefore = programmaticScrolls.length;

    // The scroll-to-top button's smooth scroll passes through the window's
    // first quarter; moving the window there would stop it. A browser can
    // pause such a scroll between frames for longer than the virtualizer's
    // scrolling flag lasts.
    for (let top = 2_000 * 300; top >= 0; top -= 50_000) {
      act(() => userScroll(top));
      if (top === 100_000) act(() => vi.advanceTimersByTime(300));
    }
    act(() => userScroll(0));

    expect(programmaticScrolls).toHaveLength(scrollsBefore);
    expect(window.scrollY).toBe(0);
    expect(onTopRowChange).toHaveBeenLastCalledWith(0, 1);
    expect(rendered(0)).toBe(true);
  });

  it("keeps the viewer's row when the list ends sooner under the window", () => {
    usePointer(true);
    const { gridRef, onTopRowChange, rerender } = renderGrid(100_000, pagesAround(970));
    act(() => gridRef.current?.scrollToItem(97_000));
    // Row 48,500 sits in the list's last window, starting at row 46,000.
    expect(window.scrollY).toBe(2_500 * 300);

    // An estimated total shrinks to the exact count: the last window now
    // starts at row 45,500.
    act(() => rerender(99_000));

    expect(window.scrollY).toBe(3_000 * 300);
    expect(onTopRowChange).toHaveBeenLastCalledWith(97_000, 97_001);
  });

  it("keeps moving the window after the list empties while a new sort loads", () => {
    usePointer(true);
    const { gridRef, rerender } = renderGrid(100_000, pagesAround(0, 800));
    act(() => gridRef.current?.scrollToItem(80_000));
    act(() => rerender(0));
    act(() => rerender(100_000));
    act(() => gridRef.current?.scrollToItem(0));
    const scrollsBefore = programmaticScrolls.length;

    // The window's row 3,100, in its last quarter.
    act(() => userScroll(3_100 * 300));
    act(() => settle());

    expect(programmaticScrolls.length).toBeGreaterThan(scrollsBefore);
    expect(window.scrollY).toBe(1_100 * 300);
  });

  it("lands a jump past the end on the last row and keeps moving the window", () => {
    usePointer(true);
    const { gridRef, onTopRowChange } = renderGrid(100_000, pagesAround(999));
    act(() => gridRef.current?.scrollToItem(100_500));
    // The last row, 49,999, in the list's last window, starting at row 46,000.
    expect(window.scrollY).toBe(3_999 * 300);
    expect(onTopRowChange).toHaveBeenLastCalledWith(99_998, 99_999);

    // The window's row 900, in its first quarter.
    act(() => userScroll(900 * 300));
    act(() => settle());

    // The window now starts at row 44,000: the same row is its row 2,900.
    expect(window.scrollY).toBe(2_900 * 300);
  });

  it("keeps the viewer's item when the column count changes", () => {
    usePointer(true);
    const { gridRef, onTopRowChange, rerender } = renderGrid(100_000, pagesAround(800));
    act(() => gridRef.current?.scrollToItem(80_000));

    // Four columns: item 80,000 is now row 20,000, which a window starting
    // at row 18,000 holds in its middle.
    gridLayout.columnCount = 4;
    act(() => rerender(100_000));

    expect(window.scrollY).toBe(2_000 * 300);
    expect(onTopRowChange).toHaveBeenLastCalledWith(80_000, 80_003);
  });

  it("keeps the viewer's item when a column change keeps the window", () => {
    usePointer(true);
    const { gridRef, onTopRowChange, rerender } = renderGrid(100_000, pagesAround(110));
    // Item 11,000 is row 5,500, in the middle of a window starting at 3,500.
    act(() => gridRef.current?.scrollToItem(11_000));

    // Three columns: the item is now row 3,666, still inside that window, so
    // the window's start, size and pitch all stay as they were.
    gridLayout.columnCount = 3;
    act(() => rerender(100_000));

    expect(window.scrollY).toBe(166 * 300);
    expect(onTopRowChange).toHaveBeenLastCalledWith(10_998, 11_000);

    // The landed move leaves nothing pending: a rest near the window's end
    // still moves it.
    act(() => userScroll(3_100 * 300));
    act(() => settle());
    expect(window.scrollY).toBe(1_100 * 300);
  });

  it("keeps a short touch grid at its full height", () => {
    usePointer(true);
    const { gridRef, extent } = renderGrid(1_000, pagesAround(0, 1, 9));
    expect(extent()).toBe(`${500 * 300}px`);

    act(() => gridRef.current?.scrollToItem(901));

    expect(window.scrollY).toBe(450 * 300);
  });
});
