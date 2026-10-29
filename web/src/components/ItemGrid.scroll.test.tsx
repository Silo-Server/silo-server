import { createRef } from "react";
import { act, cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { BrowseItem } from "@/api/types";
import {
  awaitingGridRestore,
  enterHistoryEntry,
  isRestorePending,
  recordGridAnchor,
  recordScroll,
  resetScrollRestoration,
  savedGridIndex,
} from "@/lib/scrollRestoration";
import ItemGrid, { type ItemGridHandle } from "./ItemGrid";

// Keep the real window virtualizer: taller cards must be discovered only
// when they enter its rendered range, rather than all being measured at mount.
vi.mock("@/hooks/useGridLayout", async () => {
  const { useRef } = await import("react");
  return {
    useGridLayout: () => ({
      containerRef: useRef<HTMLDivElement | null>(null),
      layout: { columnCount: 2, rowHeight: 300 },
    }),
  };
});
vi.mock("@/hooks/useOverlayPrefs", () => ({
  useOverlayPrefs: () => ({ prefs: {}, quickActionMode: "none" }),
}));
vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({
    cardPresentation: { poster_size: "standard" },
  }),
}));
vi.mock("./ItemCard", () => ({
  default: ({ item }: { item: BrowseItem }) => <div data-id={item.content_id}>card</div>,
}));

const items = Array.from({ length: 400 }, (_, i) => ({ content_id: `item-${i}` }) as BrowseItem);

function scrollTo(top: number) {
  Object.defineProperty(window, "scrollY", { configurable: true, value: top });
  window.dispatchEvent(new Event("scroll"));
}

describe("ItemGrid scroll anchoring", () => {
  beforeEach(() => {
    // Every scroll arms the virtualizer's is-scrolling reset timer, which
    // unmounting does not clear. A real one can fire after the file's DOM is
    // torn down and fail the run with "window is not defined".
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    vi.spyOn(window, "scrollTo").mockImplementation(
      (options: ScrollToOptions | number, y?: number) => {
        const top = typeof options === "number" ? y : options.top;
        if (top !== undefined && top !== window.scrollY) scrollTo(top);
      },
    );
    vi.spyOn(document.documentElement, "scrollHeight", "get").mockReturnValue(100_000);
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(
      () => ({ top: -window.scrollY }) as DOMRect,
    );
    scrollTo(0);
    vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (
      this: HTMLElement,
    ) {
      const id = this.dataset.id;
      if (!id) return 0;
      return Number(id.slice("item-".length)) >= 150 ? 327 : 287;
    });
  });
  afterEach(() => {
    cleanup();
    vi.clearAllTimers();
    vi.useRealTimers();
    vi.restoreAllMocks();
    resetScrollRestoration();
    scrollTo(0);
  });

  it("keeps the visible row and its offset when taller cards enter overscan", () => {
    const { getByRole } = render(<ItemGrid items={items} />);
    act(() => scrollTo(20_000));
    expect(getByRole("list").style.gridAutoRows).toBe("288px");

    // Row 71 is 17px above the viewport. Overscan now reaches the taller
    // cards in row 75, which grow the pitch from 300px to 340px.
    act(() => scrollTo(21_317));

    expect(getByRole("list").style.gridAutoRows).toBe("328px");
    expect(window.scrollY).toBe(71 * 340 + 17);
    expect(getByRole("list").querySelector('[data-id="item-142"]')).not.toBeNull();
  });

  it("reports a jump's row as the top row when the scroll settles a fraction short", () => {
    // On a long list the browser can settle a fraction of a pixel above a
    // jump's offset; that must not count as the row above it.
    vi.spyOn(window, "scrollTo").mockImplementation((options: ScrollToOptions | number) => {
      if (typeof options !== "number" && options.top !== undefined) scrollTo(options.top - 0.5);
    });
    const gridRef = createRef<ItemGridHandle>();
    const onTopRowChange = vi.fn();
    render(<ItemGrid items={items} gridRef={gridRef} onTopRowChange={onTopRowChange} />);

    act(() => gridRef.current?.scrollToItem(41));

    expect(window.scrollY % 1).toBe(0.5);
    expect(onTopRowChange).toHaveBeenLastCalledWith(40, 41);
  });

  it("keeps the visible row when a windowed page arrives while scrolled", () => {
    const pages = new Map([[0, items.slice(0, 100)]]);
    const props = {
      totalItems: items.length,
      pageSize: 100,
      onVisibleRangeChange: vi.fn(),
    };
    const { rerender, getByRole } = render(<ItemGrid {...props} pages={pages} />);
    act(() => scrollTo(24_019));
    expect(getByRole("list").style.gridAutoRows).toBe("288px");

    rerender(<ItemGrid {...props} pages={new Map([...pages, [1, items.slice(100, 200)]])} />);

    expect(getByRole("list").style.gridAutoRows).toBe("328px");
    expect(window.scrollY).toBe(80 * 340 + 19);
    expect(getByRole("list").querySelector('[data-id="item-160"]')).not.toBeNull();
  });

  it("returns to the row it left after remounting at the estimated pitch", () => {
    enterHistoryEntry("list", false);
    const first = render(<ItemGrid items={items} />);
    // Taller cards in overscan grow the pitch to 340px; leave 19px into row 80.
    act(() => scrollTo(24_019));
    act(() => scrollTo(80 * 340 + 19));
    first.unmount();
    scrollTo(0);

    // Back to the list: the new mount starts again from the 300px estimate,
    // where the same pixel offset would be row 90.
    enterHistoryEntry("detail", false);
    enterHistoryEntry("list", true);
    const { getByRole } = render(<ItemGrid items={items} />);

    expect(getByRole("list").style.gridAutoRows).toBe("328px");
    expect(window.scrollY).toBe(80 * 340 + 19);
    expect(getByRole("list").querySelector('[data-id="item-160"]')).not.toBeNull();
    act(() => vi.advanceTimersByTime(1000));
    expect(isRestorePending()).toBe(false);
  });

  it.each([
    // The viewport sits in the page content after the grid's 200 rows, which
    // grow to the 340px pitch of the taller cards near the end.
    { name: "keeps no row anchor below the grid", y: 200 * 340 + 500, expected: undefined },
    { name: "anchors the row at the top of the viewport", y: 40 * 300 + 7, expected: 80 },
  ])("$name", ({ y, expected }) => {
    enterHistoryEntry("list", false);
    render(<ItemGrid items={items} />);
    // What the app's scroll manager records on every scroll.
    const onScroll = () => recordScroll(window.scrollY);
    window.addEventListener("scroll", onScroll);
    act(() => scrollTo(y));
    window.removeEventListener("scroll", onScroll);

    expect(savedGridIndex("list")).toBe(expected);
  });

  it("anchors a replacement entry while the grid stays put", () => {
    // A same-URL link replaces the entry without a scroll or a remount.
    enterHistoryEntry("list", false);
    render(<ItemGrid items={items} />);
    act(() => scrollTo(40 * 300 + 7));

    act(() => {
      enterHistoryEntry("replaced", false);
    });

    expect(savedGridIndex("replaced")).toBe(80);
  });

  describe("restoring a row past the list's current extent", () => {
    // Row 150, 19px in; the list's total first covers rows 0-99.
    function returnToRow150() {
      enterHistoryEntry("list", false);
      recordGridAnchor(150 * 340 + 19, { index: 300, offset: 19 });
      enterHistoryEntry("detail", false);
      enterHistoryEntry("list", true);
    }
    const firstPage = new Map([[0, items.slice(0, 100)]]);

    it("asks for the row's page and waits for it while the total is estimated", () => {
      returnToRow150();
      const onVisibleRangeChange = vi.fn();
      const props = { pageSize: 100, onVisibleRangeChange };
      const { rerender } = render(
        <ItemGrid {...props} totalItems={200} knownItems={100} pages={firstPage} />,
      );

      expect(awaitingGridRestore()).toBe(true);
      expect(onVisibleRangeChange).toHaveBeenLastCalledWith(300, 399);

      rerender(
        <ItemGrid
          {...props}
          totalItems={500}
          knownItems={400}
          pages={new Map([...firstPage, [3, items.slice(300, 400)]])}
        />,
      );

      expect(awaitingGridRestore()).toBe(false);
      expect(window.scrollY).toBe(150 * 340 + 19);
    });

    it("lands once the row's page loads when the total is exact", () => {
      returnToRow150();
      const props = { pageSize: 100, onVisibleRangeChange: vi.fn(), totalItems: 400 };
      const { rerender } = render(<ItemGrid {...props} pages={firstPage} />);

      expect(awaitingGridRestore()).toBe(true);

      rerender(<ItemGrid {...props} pages={new Map([...firstPage, [3, items.slice(300, 400)]])} />);

      expect(awaitingGridRestore()).toBe(false);
      expect(window.scrollY).toBe(150 * 340 + 19);
    });

    it("restores nothing against a previous query's pages", () => {
      returnToRow150();
      const props = {
        pageSize: 100,
        onVisibleRangeChange: vi.fn(),
        totalItems: 400,
        pages: new Map([...firstPage, [3, items.slice(300, 400)]]),
      };
      const { rerender } = render(<ItemGrid {...props} stale />);

      expect(awaitingGridRestore()).toBe(true);

      rerender(<ItemGrid {...props} />);

      expect(awaitingGridRestore()).toBe(false);
      expect(window.scrollY).toBe(150 * 340 + 19);
    });

    it.each([
      {
        name: "the row's page came back empty",
        knownItems: 100,
        pages: new Map([...firstPage, [3, []]]),
      },
      { name: "the exact total ends before it", knownItems: undefined, pages: firstPage },
    ])("stops at the end once $name", ({ knownItems, pages }) => {
      returnToRow150();
      render(
        <ItemGrid
          pageSize={100}
          onVisibleRangeChange={vi.fn()}
          totalItems={200}
          knownItems={knownItems}
          pages={pages}
        />,
      );

      expect(awaitingGridRestore()).toBe(false);
      expect(window.scrollY).toBe(99 * 300);
    });
  });
});
