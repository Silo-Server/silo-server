import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { BrowseItem } from "@/api/types";
import ItemGrid from "./ItemGrid";

const COLUMNS = 3;
const ROW_HEIGHT = 300;

const mocks = vi.hoisted(() => ({
  scrollToIndex: vi.fn(),
  /** Rows the virtualizer renders when a test mounts the grid. */
  initialRows: [0, 3] as [number, number],
}));

// Renders only a window of rows, like the real virtualizer, and moves the
// window when asked to scroll, so the grid has to render a card before it
// can focus it.
vi.mock("@tanstack/react-virtual", async () => {
  const { useState } = await import("react");
  return {
    useWindowVirtualizer: (options: { count: number; estimateSize: () => number }) => {
      const [[first, last], setRows] = useState(mocks.initialRows);
      const size = options.estimateSize();
      const rows: Array<{ index: number; start: number; end: number; size: number; key: number }> =
        [];
      for (let index = first; index <= Math.min(last, options.count - 1); index++) {
        rows.push({ index, start: index * size, end: (index + 1) * size, size, key: index });
      }
      return {
        getVirtualItems: () => rows,
        measure: () => undefined,
        range: { startIndex: first, endIndex: last },
        options: { scrollMargin: 0 },
        scrollToIndex: (row: number, opts: unknown) => {
          mocks.scrollToIndex(row, opts);
          setRows([Math.max(0, row - 1), row + 1]);
        },
      };
    },
  };
});

vi.mock("@/hooks/useGridLayout", async () => {
  const { useRef } = await import("react");
  return {
    useGridLayout: () => ({
      containerRef: useRef<HTMLDivElement | null>(null),
      layout: { columnCount: COLUMNS, rowHeight: ROW_HEIGHT },
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
vi.mock("./ItemCard", () => ({
  default: ({ item }: { item: BrowseItem }) => (
    <div>
      <a href={`/item/${item.content_id}`}>{item.title}</a>
      <button type="button" aria-label={`More for ${item.title}`}>
        ⋯
      </button>
    </div>
  ),
}));

const makeItems = (count: number) =>
  Array.from(
    { length: count },
    (_, i) => ({ content_id: `item-${i}`, title: `Title ${i}` }) as BrowseItem,
  );

const lastPage = Array.from(
  { length: 30 },
  (_, i) => ({ content_id: `item-${60 + i}`, title: `Title ${60 + i}` }) as BrowseItem,
);

const card = (index: number) => screen.getByRole("link", { name: `Title ${index}` });

function renderGrid(props: Parameters<typeof ItemGrid>[0], dir?: "rtl") {
  const ui = (gridProps: Parameters<typeof ItemGrid>[0]) => (
    <div dir={dir}>
      <button type="button">Before</button>
      <ItemGrid {...gridProps} />
      <button type="button">After</button>
    </div>
  );
  const result = render(ui(props));
  return {
    ...result,
    rerenderGrid: (next: Parameters<typeof ItemGrid>[0]) => result.rerender(ui(next)),
  };
}

describe("ItemGrid keyboard navigation", () => {
  beforeEach(() => {
    mocks.scrollToIndex.mockClear();
    mocks.initialRows = [0, 3];
  });
  afterEach(() => cleanup());

  it("gives each rendered card its position in the full list", () => {
    renderGrid({
      totalItems: 500,
      pages: new Map([[0, makeItems(12)]]),
      pageSize: 50,
      onVisibleRangeChange: () => {},
    });

    const cells = screen.getAllByRole("listitem");
    expect(cells[0]).toHaveAttribute("aria-posinset", "1");
    expect(cells[0]).toHaveAttribute("aria-setsize", "500");
    // Unloaded positions are skeletons that still report where they sit.
    expect(cells[11]).toHaveAttribute("aria-posinset", "12");
    expect(cells[11]).toHaveAttribute("aria-setsize", "500");
  });

  it("makes the grid one Tab stop", async () => {
    const user = userEvent.setup();
    renderGrid({ items: makeItems(9) });

    screen.getByRole("button", { name: "Before" }).focus();
    await user.tab();
    expect(card(0)).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: "More for Title 0" })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: "After" })).toHaveFocus();
  });

  it("moves by column with Left and Right and by row with Up and Down", async () => {
    const user = userEvent.setup();
    renderGrid({ items: makeItems(9) });

    await user.click(card(0));
    await user.keyboard("{ArrowRight}");
    expect(card(1)).toHaveFocus();
    await user.keyboard("{ArrowDown}");
    expect(card(4)).toHaveFocus();
    await user.keyboard("{ArrowDown}");
    expect(card(7)).toHaveFocus();
    await user.keyboard("{ArrowLeft}");
    expect(card(6)).toHaveFocus();
    await user.keyboard("{ArrowUp}");
    expect(card(3)).toHaveFocus();
    // Left from a row's first card wraps to the end of the row above.
    await user.keyboard("{ArrowLeft}");
    expect(card(2)).toHaveFocus();
  });

  it("goes to the row's ends with Home and End and the list's with Ctrl", async () => {
    const user = userEvent.setup();
    renderGrid({ items: makeItems(8) });

    await user.click(card(4));
    await user.keyboard("{End}");
    expect(card(5)).toHaveFocus();
    await user.keyboard("{Home}");
    expect(card(3)).toHaveFocus();
    await user.keyboard("{Control>}{End}{/Control}");
    expect(card(7)).toHaveFocus();
    // The last row is short; End stays on its last card.
    await user.keyboard("{Home}{End}");
    expect(card(7)).toHaveFocus();
    await user.keyboard("{Control>}{Home}{/Control}");
    expect(card(0)).toHaveFocus();
  });

  it("does not move past the last row", async () => {
    const user = userEvent.setup();
    renderGrid({ items: makeItems(8) });

    await user.click(card(5));
    await user.keyboard("{ArrowDown}");

    expect(card(5)).toHaveFocus();
  });

  it("flips Left and Right in a right-to-left layout", async () => {
    const user = userEvent.setup();
    renderGrid({ items: makeItems(9) }, "rtl");

    await user.click(card(1));
    await user.keyboard("{ArrowLeft}");
    expect(card(2)).toHaveFocus();
    await user.keyboard("{ArrowRight}");
    expect(card(1)).toHaveFocus();
  });

  it("remembers the last focused card", async () => {
    const user = userEvent.setup();
    renderGrid({ items: makeItems(9) });

    await user.click(card(0));
    await user.keyboard("{ArrowDown}{ArrowRight}");
    screen.getByRole("button", { name: "Before" }).focus();
    await user.tab();

    expect(card(4)).toHaveFocus();
  });

  it("renders a card outside the virtualized rows, waits for its page, then focuses it", async () => {
    const user = userEvent.setup();
    const pageSize = 30;
    const loaded = new Map([[0, makeItems(30)]]);
    const props = {
      totalItems: 90,
      pageSize,
      onVisibleRangeChange: () => {},
    };
    const { rerenderGrid } = renderGrid({ ...props, pages: loaded });

    await user.click(card(1));
    await user.keyboard("{Control>}{End}{/Control}");

    // The last row (29) was not rendered, so the grid scrolls it into the
    // virtualized range; its page has not loaded, so focus waits on the grid
    // rather than falling to the page when the old card unmounts.
    expect(mocks.scrollToIndex).toHaveBeenCalledWith(29, { align: "auto" });
    expect(screen.queryByRole("link", { name: "Title 89" })).not.toBeInTheDocument();
    expect(screen.getByRole("list")).toHaveFocus();

    act(() => rerenderGrid({ ...props, pages: new Map([...loaded, [2, lastPage]]) }));

    expect(card(89)).toHaveFocus();
  });

  it("drops a waiting move once focus goes elsewhere", async () => {
    const user = userEvent.setup();
    const props = { totalItems: 90, pageSize: 30, onVisibleRangeChange: () => {} };
    const loaded = new Map([[0, makeItems(30)]]);
    const { rerenderGrid } = renderGrid({ ...props, pages: loaded });

    await user.click(card(1));
    await user.keyboard("{Control>}{End}{/Control}");
    await user.click(screen.getByRole("button", { name: "Before" }));
    act(() => rerenderGrid({ ...props, pages: new Map([...loaded, [2, lastPage]]) }));

    expect(screen.getByRole("button", { name: "Before" })).toHaveFocus();
  });

  it("keeps Home and End from scrolling the page at a row's edge", async () => {
    const user = userEvent.setup();
    const prevented: boolean[] = [];
    const record = (event: KeyboardEvent) => prevented.push(event.defaultPrevented);
    window.addEventListener("keydown", record);
    renderGrid({ items: makeItems(9) });

    await user.click(card(3));
    await user.keyboard("{Home}");
    await user.click(card(5));
    await user.keyboard("{End}");
    window.removeEventListener("keydown", record);

    expect(card(5)).toHaveFocus();
    expect(prevented).toEqual([true, true]);
  });

  it("lets Up in the first row scroll the page", async () => {
    const user = userEvent.setup();
    const prevented: boolean[] = [];
    const record = (event: KeyboardEvent) => prevented.push(event.defaultPrevented);
    window.addEventListener("keydown", record);
    renderGrid({ items: makeItems(9) });

    await user.click(card(1));
    await user.keyboard("{ArrowUp}");
    window.removeEventListener("keydown", record);

    expect(card(1)).toHaveFocus();
    expect(prevented).toEqual([false]);
  });

  it("puts the Tab stop on the first visible card once the remembered one scrolls away", async () => {
    const user = userEvent.setup();
    mocks.initialRows = [5, 8];
    renderGrid({ items: makeItems(30) });

    screen.getByRole("button", { name: "Before" }).focus();
    await user.tab();

    expect(card(15)).toHaveFocus();
  });
});
