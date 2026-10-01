import { act, cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { BrowseItem } from "@/api/types";
import ItemGrid from "./ItemGrid";

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
    cardPresentation: { poster_size: "standard", caption: "title_metadata" },
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
    vi.restoreAllMocks();
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
});
