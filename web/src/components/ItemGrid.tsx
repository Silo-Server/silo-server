import {
  useEffect,
  useCallback,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  useState,
  useSyncExternalStore,
  type ReactNode,
  type Ref,
} from "react";
import { useWindowVirtualizer, windowScroll } from "@tanstack/react-virtual";
import type { BrowseItem } from "@/api/types";
import ItemCard from "./ItemCard";
import { Skeleton } from "@/components/ui/skeleton";
import { useGridLayout } from "@/hooks/useGridLayout";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { useUICustomization } from "@/hooks/useUICustomization";
import { CARD_TEXT_AREA_HEIGHT, cardGridClasses } from "@/lib/uiCustomization";
import {
  awaitingGridRestore,
  pendingGridRestore,
  recordGridAnchor,
  restoreLanded,
  scrollRestorationVersion,
  subscribeScrollRestoration,
} from "@/lib/scrollRestoration";

interface SharedItemGridProps {
  loading?: boolean;
  sortField?: string;
  libraryId?: number;
  narrowPosterActions?: boolean;
  selectionMode?: boolean;
  selectedIds?: ReadonlySet<string>;
  onToggleSelect?: (item: BrowseItem) => void;
  /** Replaces the default "No items found." message when there are no items. */
  emptyState?: ReactNode;
  /** Imperative access for jumping to a position, such as a letter bar. */
  gridRef?: Ref<ItemGridHandle>;
  /** The first and last item index of the top row the viewer can see. */
  onTopRowChange?: (firstIndex: number, lastIndex: number) => void;
}

export interface ItemGridHandle {
  /** Scroll so the row holding `index` sits at the top of the viewport. */
  scrollToItem: (index: number) => void;
}

/**
 * How much of the top of the viewport sticky page headers cover once stuck:
 * the mobile top bar, or the library header on wide screens. Headers opt in
 * with `data-sticky-header`; one that is hidden or scrolls with the page at
 * the current width covers nothing.
 */
function stickyHeaderInset(): number {
  let inset = 0;
  for (const header of document.querySelectorAll<HTMLElement>("[data-sticky-header]")) {
    const style = getComputedStyle(header);
    const height = header.getBoundingClientRect().height;
    if ((style.position !== "sticky" && style.position !== "fixed") || height === 0) continue;
    const stuckTop =
      (Number.parseFloat(style.top) || 0) + (Number.parseFloat(style.marginTop) || 0);
    inset = Math.max(inset, stuckTop + height);
  }
  return inset;
}

/**
 * The tallest a grid may make the page. Past it the grid reserves a window of
 * rows instead of the whole list: a jump moves the window to the row it lands
 * on, and scrolling near either end of the window moves it on, keeping the
 * same row in place.
 *
 * Chrome stops drawing a page scrolled too far down; the screen goes blank,
 * header included. Desktop Chrome draws a page scrolled 8.30 million pixels
 * and nothing at 8.42 million, about 2^23. Chrome on an Android phone gave out
 * sooner: a 151,000-title library in three columns is 11.5 million pixels
 * tall, and on a phone every row past about 2.5 million went blank. So a grid
 * on a device with a touchscreen keeps to TOUCH_GRID_MAX_EXTENT_PX and any
 * other to GRID_MAX_EXTENT_PX; a list shorter than that keeps its whole
 * height, and its scrollbar spans it. Chrome also caps an element's height
 * at ELEMENT_MAX_DEVICE_PX device pixels, which at a high pixel ratio or
 * browser zoom comes before the desktop limit, so that limit shrinks with the
 * pixel ratio. The window is sized by the estimated row pitch, which the
 * measured one exceeds (by 12% at the default text size), so every limit
 * leaves room for a pitch half again the estimate.
 */
const TOUCH_GRID_MAX_EXTENT_PX = 1_200_000;
const GRID_MAX_EXTENT_PX = 5_000_000;
const ELEMENT_MAX_DEVICE_PX = 33_554_428;
const PITCH_HEADROOM = 1.5;
// How long the scroll position rests before the row window moves.
const WINDOW_SETTLE_MS = 600;

function gridMaxExtent(touch: boolean): number {
  if (touch) return TOUCH_GRID_MAX_EXTENT_PX;
  const pixelRatio = (typeof window !== "undefined" && window.devicePixelRatio) || 1;
  return Math.min(GRID_MAX_EXTENT_PX, ELEMENT_MAX_DEVICE_PX / pixelRatio / PITCH_HEADROOM);
}

interface WindowedItemGridProps extends SharedItemGridProps {
  totalItems: number;
  /**
   * Items known to exist. Below `totalItems` while the total is an estimated
   * runway past the last loaded page; defaults to `totalItems`, an exact total.
   */
  knownItems?: number;
  /**
   * The pages are a previous query's, kept on screen while this one loads.
   * The grid shows them but restores no scroll position against them.
   */
  stale?: boolean;
  pages: Map<number, BrowseItem[]>;
  pageSize: number;
  onVisibleRangeChange: (startIndex: number, endIndex: number) => void;
  items?: never;
}

interface StaticItemGridProps extends SharedItemGridProps {
  items: BrowseItem[];
  totalItems?: never;
  knownItems?: never;
  stale?: never;
  pages?: never;
  pageSize?: never;
  onVisibleRangeChange?: never;
}

type ItemGridProps = WindowedItemGridProps | StaticItemGridProps;

function hasStaticItems(props: ItemGridProps): props is StaticItemGridProps {
  return Array.isArray((props as StaticItemGridProps).items);
}

export default function ItemGrid(props: ItemGridProps) {
  const {
    loading,
    sortField,
    libraryId,
    narrowPosterActions = false,
    selectionMode = false,
    selectedIds,
    onToggleSelect,
    emptyState,
    gridRef,
    onTopRowChange,
  } = props;
  const { prefs: overlayPrefs, quickActionMode } = useOverlayPrefs();
  const { cardPresentation } = useUICustomization();
  const gridGap = cardPresentation.poster_size === "large" ? 16 : 12;
  const gridClasses = cardGridClasses(cardPresentation.poster_size);
  const totalItems = hasStaticItems(props) ? props.items.length : props.totalItems;
  const pages = hasStaticItems(props)
    ? new Map<number, BrowseItem[]>([[0, props.items]])
    : props.pages;
  const pageSize = hasStaticItems(props) ? Math.max(props.items.length, 1) : props.pageSize;
  const onVisibleRangeChange = hasStaticItems(props) ? () => undefined : props.onVisibleRangeChange;
  const { containerRef, layout } = useGridLayout({
    gap: gridGap,
    textAreaHeight: CARD_TEXT_AREA_HEIGHT,
    layoutKey: cardPresentation.poster_size,
  });
  const [anchorEl, setAnchorEl] = useState<HTMLDivElement | null>(null);
  const { columnCount, rowHeight: estimatedRowHeight } = layout;
  // The estimate adds a fixed caption height, but the rendered caption depends
  // on line heights, an episode line and the text scale, so it runs taller.
  // Rows reserved at the estimate then spilled past the page bottom (#1613).
  // The grid now pins every CSS row to the pitch the virtualizer reserves, and
  // that pitch grows to fit the tallest card rendered, so reserved and drawn
  // heights match exactly, row for row. A measurement only holds for the
  // layout it was taken under; a poster size, caption or column change
  // produces a new estimate and a fresh measurement.
  const [measured, setMeasured] = useState<{
    estimate: number;
    rowHeight: number;
    gap: number;
  } | null>(null);
  const rowHeight =
    measured?.estimate === estimatedRowHeight ? measured.rowHeight : estimatedRowHeight;
  const rowGap = measured?.estimate === estimatedRowHeight ? measured.gap : gridGap;
  const scrollAnchorRef = useRef<{ index: number; offset: number } | null>(null);
  const scrollMargin = anchorEl ? anchorEl.getBoundingClientRect().top + window.scrollY : 0;

  // Use the full totalItems for virtualizer height so the scrollbar reflects
  // the true list size from the first render. Unloaded positions render as
  // skeletons via getItem returning undefined — no incremental height growth
  // needed, which eliminates scroll snap-back on fast scrolling.
  const rowCount = Math.ceil(totalItems / columnCount);

  // A grid taller than its extent limit reserves only a window of rows,
  // starting at absolute row windowStart; the virtualizer's rows are that
  // window's. The budget follows the estimated pitch, so a measured, taller
  // pitch doesn't resize the window under a scroll. Any touchscreen counts:
  // Android with a mouse as its primary pointer draws no deeper.
  const touch = useMediaQuery("(any-pointer: coarse)");
  const windowBudget = Math.max(1, Math.floor(gridMaxExtent(touch) / estimatedRowHeight));
  const windowed = rowCount > windowBudget;
  const windowRows = windowed ? windowBudget : rowCount;
  const maxWindowStart = rowCount - windowRows;
  const [requestedWindowStart, setWindowStart] = useState(0);
  const windowStart = Math.min(requestedWindowStart, maxWindowStart);
  // The window that puts `row` in its middle.
  const windowStartFor = useCallback(
    (row: number) =>
      windowed ? Math.max(0, Math.min(row - Math.floor(windowRows / 2), maxWindowStart)) : 0,
    [windowed, windowRows, maxWindowStart],
  );
  // A scroll to make once the window requested at `start` has rendered:
  // absolute row `row` at `offset` pixels below the grid's top edge.
  const windowScrollRef = useRef<{ row: number; offset: number; start: number } | null>(null);
  // The item at the start of the viewport's top row, and how far into that
  // row, as of the last scroll the grid saw; a window that changes under the
  // viewer keeps them on it.
  const topItemRef = useRef<{ index: number; offset: number } | null>(null);
  const committedWindowRef = useRef<{
    requested: number;
    start: number;
    rows: number;
    columns: number;
    windowed: boolean;
  } | null>(null);

  const virtualizer = useWindowVirtualizer({
    count: windowRows,
    estimateSize: () => rowHeight,
    overscan: 5,
    scrollMargin,
    // The virtualizer otherwise learns where its own scroll went from the
    // next scroll event, a frame later; a moved row window has to draw the
    // rows it scrolled to in the same frame (see the window scroll below).
    scrollToFn: (offset, options, instance) => {
      windowScroll(offset, options, instance);
      instance.scrollOffset = window.scrollY;
    },
  });

  // The estimate function closes over rowHeight. Explicitly invalidate the
  // cached measurements when a poster/caption preset changes that height.
  useLayoutEffect(() => {
    virtualizer.measure();
    const anchor = scrollAnchorRef.current;
    if (anchor) {
      scrollAnchorRef.current = null;
      virtualizer.scrollToOffset(
        virtualizer.options.scrollMargin + anchor.index * rowHeight + anchor.offset,
        { behavior: "instant" },
      );
    }
  }, [rowHeight, virtualizer]);

  // A window that changes under the viewer without a move of the grid's own
  // (a new requested start), when the list's total, its columns or the
  // pointer change, keeps the item they were on: in the new window if it
  // holds that item's row, else in one centered on it. An empty list, as
  // while a new sort loads, has nothing to keep. Grids that weren't and
  // aren't windowed are left alone.
  useLayoutEffect(() => {
    const previous = committedWindowRef.current;
    committedWindowRef.current = {
      requested: requestedWindowStart,
      start: windowStart,
      rows: windowRows,
      columns: columnCount,
      windowed,
    };
    if (!previous || previous.requested !== requestedWindowStart) return;
    if (
      previous.start === windowStart &&
      previous.rows === windowRows &&
      previous.columns === columnCount
    ) {
      return;
    }
    if ((!windowed && !previous.windowed) || rowCount === 0) return;
    const top = topItemRef.current;
    if (!top || windowScrollRef.current || awaitingGridRestore()) return;
    const savedRow = Math.floor(top.index / columnCount);
    const row = Math.min(savedRow, rowCount - 1);
    const offset = row === savedRow ? Math.min(top.offset, rowHeight - 1) : 0;
    const start =
      row < windowStart || row >= windowStart + windowRows
        ? windowStartFor(row)
        : requestedWindowStart;
    windowScrollRef.current = { row, offset, start };
    // eslint-disable-next-line react-hooks/set-state-in-effect -- the viewer's item left the window; move to one that holds it
    if (start !== requestedWindowStart) setWindowStart(start);
  }, [
    requestedWindowStart,
    windowStart,
    windowRows,
    windowed,
    columnCount,
    rowCount,
    rowHeight,
    windowStartFor,
  ]);

  // Make the scroll a moved window was waiting for before the frame paints,
  // so the row the viewer was on (or jumped to) is where they see it, and
  // render the rows there now rather than on the next scroll event. A target
  // the window it waited for can't hold, because the list got shorter
  // meanwhile, is dropped. It runs after every commit: a target can be set by
  // a commit that changes nothing else this effect reads (a column change
  // that keeps the window), and one left pending stops the window moving.
  useLayoutEffect(() => {
    const target = windowScrollRef.current;
    if (!target) return;
    if (target.row < windowStart || target.row >= windowStart + windowRows) {
      if (requestedWindowStart === target.start) windowScrollRef.current = null;
      return;
    }
    windowScrollRef.current = null;
    virtualizer.scrollToOffset(
      Math.max(
        0,
        virtualizer.options.scrollMargin + (target.row - windowStart) * rowHeight + target.offset,
      ),
      { behavior: "instant" },
    );
    virtualizer.measure();
  });

  const virtualRows = virtualizer.getVirtualItems();
  const showsItems = !loading && totalItems > 0;
  const restoreVersion = useSyncExternalStore(subscribeScrollRestoration, scrollRestorationVersion);

  // A row past the current extent may still be on its way: an estimated
  // total only reaches it once the page holding it loads. Once the total is
  // final, or that page is in, the list ended sooner than when it was saved.
  const restoreAnchor = pendingGridRestore();
  const stale = props.stale === true;
  const extentFinal =
    hasStaticItems(props) || (props.knownItems ?? props.totalItems) >= props.totalItems;
  const restorePageLoaded =
    !restoreAnchor ||
    hasStaticItems(props) ||
    pages.has(Math.floor(restoreAnchor.index / pageSize));
  const restoreRowSettled = extentFinal || restorePageLoaded;

  // Back or Forward to this page: put the saved row back at the top, at the
  // same depth into it. The restore stays in force until the page settles,
  // so this re-runs as rows arrive.
  useLayoutEffect(() => {
    const anchor = pendingGridRestore();
    // Until the wrapper mounts, the scroll margin doesn't yet count the page
    // content above the grid.
    if (!anchor || !showsItems || !anchorEl || stale) return;
    const savedRow = Math.floor(anchor.index / columnCount);
    if (savedRow >= rowCount && !restoreRowSettled) return;
    const row = Math.min(savedRow, rowCount - 1);
    // A windowed grid first moves its window to the saved row; this runs again
    // once it has.
    if (row < windowStart || row >= windowStart + windowRows) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- the restore lands once the window holding the saved row has rendered
      setWindowStart(windowStartFor(row));
      return;
    }
    // The saved offset was taken at the last mount's measured pitch, which
    // can be taller than this mount's; past this row it would be the next.
    const offset = row === savedRow ? Math.min(anchor.offset, rowHeight - 1) : 0;
    virtualizer.scrollToOffset(
      virtualizer.options.scrollMargin + (row - windowStart) * rowHeight + offset,
      { behavior: "instant" },
    );
    // Until the row's cards load, the row height is an estimate, and the
    // browser may have clamped the offset to a document that is too short.
    // Land once they're in, so the restore re-applies at the measured height;
    // or now, when the list ends before the saved row.
    if (row < savedRow || restorePageLoaded) restoreLanded();
  }, [
    restoreVersion,
    restoreRowSettled,
    restorePageLoaded,
    stale,
    showsItems,
    anchorEl,
    columnCount,
    rowCount,
    rowHeight,
    virtualizer,
    windowStart,
    windowRows,
    windowStartFor,
  ]);

  useImperativeHandle(
    gridRef,
    () => ({
      scrollToItem: (index: number) => {
        const row = Math.max(0, Math.min(Math.floor(index / columnCount), rowCount - 1));
        const offset = -stickyHeaderInset();
        // A windowed grid moves its window to put the row in its middle, then
        // scrolls once the moved window has rendered.
        const start = windowStartFor(row);
        if (start !== windowStart) {
          windowScrollRef.current = { row, offset, start };
          setWindowStart(start);
          return;
        }
        virtualizer.scrollToOffset(
          Math.max(0, virtualizer.options.scrollMargin + (row - windowStart) * rowHeight + offset),
          { behavior: "auto" },
        );
      },
    }),
    [columnCount, rowCount, rowHeight, virtualizer, windowStart, windowStartFor],
  );

  // Sticky headers only change height at layout breakpoints, so measure them
  // on resize rather than on every scroll frame.
  const headerInsetRef = useRef(0);
  useEffect(() => {
    if (!onTopRowChange) return;
    const measure = () => {
      headerInsetRef.current = stickyHeaderInset();
    };
    measure();
    window.addEventListener("resize", measure);
    return () => window.removeEventListener("resize", measure);
  }, [onTopRowChange]);

  const scrollOffset = virtualizer.scrollOffset ?? 0;
  useEffect(() => {
    if (!onTopRowChange) return;
    // The top row is the first row at least half visible below the sticky
    // header. Rounding, not flooring, also absorbs the fraction of a pixel a
    // long list's scroll can settle short of a jump's offset, which would
    // otherwise count the row hidden under the header.
    const row =
      windowStart +
      Math.max(0, Math.round((scrollOffset + headerInsetRef.current - scrollMargin) / rowHeight));
    onTopRowChange(row * columnCount, (row + 1) * columnCount - 1);
  }, [onTopRowChange, scrollOffset, scrollMargin, rowHeight, columnCount, windowStart]);

  // Note the viewer's top item after every scroll and window move, for the
  // window to keep them on when it changes under them.
  useLayoutEffect(() => {
    if (!anchorEl || windowScrollRef.current) return;
    const top = window.scrollY - scrollMargin;
    const row = Math.max(0, Math.floor(top / rowHeight));
    topItemRef.current = {
      index: (windowStart + row) * columnCount,
      offset: top - row * rowHeight,
    };
  }, [anchorEl, scrollOffset, scrollMargin, rowHeight, windowStart, columnCount]);

  // Once the scroll position has rested for WINDOW_SETTLE_MS in the first or
  // last quarter of the row window, the window moves half its length that
  // way, keeping the row on screen where it is. Moving it scrolls, which
  // would cut short a smooth scroll (the scroll-to-top button) or a fling,
  // and a smooth scroll can pause between frames; until then the window
  // moves only for a viewport about to run out of rows below. Back at the
  // grid's top edge, a scroll to the top of the page, which scrolling by hand
  // only reaches through the window's first rows, puts the window back at the
  // start of the list; that needs no scroll.
  useLayoutEffect(() => {
    if (!windowed || !anchorEl || windowScrollRef.current || awaitingGridRestore()) return;
    const step = Math.floor(windowRows / 2);
    const edge = Math.floor(windowRows / 4);
    // Moves the window to `next`, keeping the row on screen now in place.
    const move = (next: number) => {
      const top = window.scrollY - scrollMargin;
      const row = Math.max(0, Math.floor(top / rowHeight));
      windowScrollRef.current = {
        row: windowStart + row,
        offset: top - row * rowHeight,
        start: next,
      };
      setWindowStart(next);
    };
    // The window a viewer resting at `top` pixels into it belongs in.
    const settledStart = (top: number) => {
      const row = Math.floor(top / rowHeight);
      if (row < edge && windowStart > 0) return Math.max(0, windowStart - step);
      if (row >= windowRows - edge && windowStart < maxWindowStart) {
        return Math.min(maxWindowStart, windowStart + step);
      }
      return windowStart;
    };
    const top = window.scrollY - scrollMargin;
    if (top <= 0) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- the viewer scrolled to the top of the page; the list's start belongs there
      if (windowStart > 0) setWindowStart(0);
      return;
    }
    const screenRows = Math.ceil(window.innerHeight / rowHeight);
    if (
      Math.floor(top / rowHeight) + 2 * screenRows >= windowRows &&
      windowStart < maxWindowStart
    ) {
      move(Math.min(maxWindowStart, windowStart + step));
      return;
    }
    if (settledStart(top) === windowStart) return;
    // Decide again when the timer fires, from where the viewer is then.
    const timer = window.setTimeout(() => {
      const next = settledStart(window.scrollY - scrollMargin);
      if (next !== windowStart) move(next);
    }, WINDOW_SETTLE_MS);
    return () => window.clearTimeout(timer);
  }, [
    scrollOffset,
    windowed,
    anchorEl,
    scrollMargin,
    rowHeight,
    windowRows,
    windowStart,
    maxWindowStart,
  ]);

  // Static grids rebuild `pages` each render; key on the data behind it.
  const content = hasStaticItems(props) ? props.items : props.pages;
  // Report visible item range to parent for page fetching
  const firstRow = virtualRows[0]?.index ?? 0;
  const lastRow = virtualRows[virtualRows.length - 1]?.index ?? 0;
  // Measure each rendered card's natural height (its content, not the grid
  // cell stretched to the pinned row) when the rendered range or content
  // changes, not after every commit. The pitch only grows within a layout, so
  // it can't be pulled back and forth by which cards are on screen.
  useLayoutEffect(() => {
    const grid = containerRef.current;
    if (!showsItems || !grid) return;
    let tallest = 0;
    for (const cell of Array.from(grid.children)) {
      const card = cell.firstElementChild;
      if (card instanceof HTMLElement) tallest = Math.max(tallest, card.offsetHeight);
    }
    if (tallest <= 0) return;
    // offsetHeight ignores transforms (cards scale on hover), which keeps a
    // hovered card from inflating the pitch, but it rounds to whole pixels;
    // one extra pixel covers a fractional card height.
    // Tailwind gaps use rem, so larger text also scales the rendered gap.
    const cssGap = Number.parseFloat(getComputedStyle(grid).rowGap);
    const renderedGap = Number.isFinite(cssGap) ? cssGap : gridGap;
    const pitch = tallest + 1 + renderedGap;
    if (pitch > rowHeight || renderedGap !== rowGap) {
      // Growing every row also moves the content above the viewport. Keep
      // the first visible row at its current offset instead of jumping back
      // to earlier items when a taller card or a loaded page enters overscan.
      const anchor = virtualizer
        .getVirtualItems()
        .find((row) => row.start <= window.scrollY && row.end > window.scrollY);
      scrollAnchorRef.current =
        pitch > rowHeight && anchor
          ? { index: anchor.index, offset: window.scrollY - anchor.start }
          : null;
      setMeasured({
        estimate: estimatedRowHeight,
        rowHeight: Math.max(rowHeight, pitch),
        gap: renderedGap,
      });
    }
  }, [
    showsItems,
    firstRow,
    lastRow,
    content,
    estimatedRowHeight,
    rowHeight,
    rowGap,
    gridGap,
    containerRef,
    virtualizer,
  ]);

  // Record the top row on every scroll, so leaving the page needs no
  // bookkeeping. Read the window rather than the virtualizer, which still
  // holds the offset from before a restore that just landed.
  useEffect(() => {
    if (!showsItems || !anchorEl) return;
    const y = window.scrollY;
    // Past the grid's last row the viewport shows whatever the page puts
    // after it; the plain scroll position is the one to keep there.
    if (y >= scrollMargin + windowRows * rowHeight) return;
    const row = Math.max(0, Math.floor((y - scrollMargin) / rowHeight));
    recordGridAnchor(y, {
      index: (windowStart + row) * columnCount,
      offset: y - scrollMargin - row * rowHeight,
    });
  }, [
    showsItems,
    anchorEl,
    scrollOffset,
    scrollMargin,
    windowRows,
    windowStart,
    rowHeight,
    columnCount,
    restoreVersion,
  ]);

  useEffect(() => {
    // Until a restore lands, the rows on screen are not where the page is
    // headed. Ask for the saved row's window instead, so that page is the one
    // fetched, whatever range the page last asked for.
    const anchor = awaitingGridRestore() ? pendingGridRestore() : undefined;
    if (anchor) {
      onVisibleRangeChange(anchor.index, anchor.index + pageSize - 1);
      return;
    }
    const start = (windowStart + firstRow) * columnCount;
    const end = Math.min((windowStart + lastRow + 1) * columnCount - 1, totalItems - 1);
    onVisibleRangeChange(start, Math.max(end, 0));
  }, [
    firstRow,
    lastRow,
    windowStart,
    columnCount,
    totalItems,
    pageSize,
    onVisibleRangeChange,
    restoreVersion,
  ]);

  const getItem = useCallback(
    (globalIndex: number): BrowseItem | undefined => {
      const pageIndex = Math.floor(globalIndex / pageSize);
      const itemIndex = globalIndex % pageSize;
      return pages.get(pageIndex)?.[itemIndex];
    },
    [pages, pageSize],
  );

  return (
    <div ref={setAnchorEl}>
      {loading ? (
        <div ref={containerRef} role="list" className={gridClasses}>
          {Array.from({ length: 24 }).map((_, i) => (
            <div key={i} role="listitem">
              <Skeleton className="aspect-[2/3] rounded-lg" />
              <Skeleton className="mt-2 h-4 w-3/4" />
            </div>
          ))}
        </div>
      ) : totalItems === 0 ? (
        (emptyState ?? (
          <div className="text-muted-foreground py-12 text-center">No items found.</div>
        ))
      ) : (
        <div
          style={{
            // Commit the new extent before restoring the scroll anchor, so
            // the browser can scroll to it even near the end of the list.
            height: windowRows * rowHeight,
            position: "relative",
            overflow: "visible",
            // The virtualizer owns anchoring; browser anchoring would apply
            // a second adjustment when the rendered range changes.
            overflowAnchor: "none",
          }}
        >
          <div
            ref={containerRef}
            role="list"
            className={gridClasses}
            style={{
              position: "absolute",
              top: 0,
              left: 0,
              right: 0,
              overflow: "visible",
              // Every row is exactly the pitch the virtualizer reserves.
              gridAutoRows: `${Math.max(rowHeight - rowGap, 0)}px`,
              transform: `translateY(${(virtualRows[0]?.start ?? 0) - scrollMargin}px)`,
            }}
          >
            {virtualRows.flatMap((virtualRow) => {
              const startIndex = (windowStart + virtualRow.index) * columnCount;
              const cellCount = Math.min(columnCount, totalItems - startIndex);
              const cells = [];

              for (let colIndex = 0; colIndex < cellCount; colIndex++) {
                const globalIndex = startIndex + colIndex;
                const item = getItem(globalIndex);

                if (!item) {
                  cells.push(
                    <div key={`skeleton-${globalIndex}`} role="listitem">
                      <Skeleton className="aspect-[2/3] rounded-lg" />
                      <Skeleton className="mt-2 h-4 w-3/4" />
                    </div>,
                  );
                } else {
                  cells.push(
                    <div key={`${item.content_id}-${globalIndex}`} role="listitem">
                      <ItemCard
                        item={item}
                        libraryId={libraryId}
                        sortField={sortField}
                        overlayPrefs={overlayPrefs}
                        quickActionMode={quickActionMode}
                        narrowPosterActions={narrowPosterActions}
                        selectionMode={selectionMode}
                        selected={selectedIds?.has(item.content_id) ?? false}
                        onToggleSelect={onToggleSelect}
                      />
                    </div>,
                  );
                }
              }

              return cells;
            })}
          </div>
        </div>
      )}
    </div>
  );
}
