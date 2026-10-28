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
import { useWindowVirtualizer } from "@tanstack/react-virtual";
import type { BrowseItem } from "@/api/types";
import ItemCard from "./ItemCard";
import { Skeleton } from "@/components/ui/skeleton";
import { useGridLayout } from "@/hooks/useGridLayout";
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

  const virtualizer = useWindowVirtualizer({
    count: rowCount,
    estimateSize: () => rowHeight,
    overscan: 5,
    scrollMargin,
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
    // The saved offset was taken at the last mount's measured pitch, which
    // can be taller than this mount's; past this row it would be the next.
    const offset = row === savedRow ? Math.min(anchor.offset, rowHeight - 1) : 0;
    virtualizer.scrollToOffset(virtualizer.options.scrollMargin + row * rowHeight + offset, {
      behavior: "instant",
    });
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
  ]);

  useImperativeHandle(
    gridRef,
    () => ({
      scrollToItem: (index: number) => {
        const row = Math.floor(Math.max(0, index) / columnCount);
        virtualizer.scrollToOffset(
          Math.max(0, virtualizer.options.scrollMargin + row * rowHeight - stickyHeaderInset()),
          { behavior: "auto" },
        );
      },
    }),
    [columnCount, rowHeight, virtualizer],
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
    const row = Math.max(
      0,
      Math.round((scrollOffset + headerInsetRef.current - scrollMargin) / rowHeight),
    );
    onTopRowChange(row * columnCount, (row + 1) * columnCount - 1);
  }, [onTopRowChange, scrollOffset, scrollMargin, rowHeight, columnCount]);

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
    if (y >= scrollMargin + rowCount * rowHeight) return;
    const row = Math.max(0, Math.floor((y - scrollMargin) / rowHeight));
    recordGridAnchor(y, {
      index: row * columnCount,
      offset: y - scrollMargin - row * rowHeight,
    });
  }, [
    showsItems,
    anchorEl,
    scrollOffset,
    scrollMargin,
    rowCount,
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
    const start = firstRow * columnCount;
    const end = Math.min((lastRow + 1) * columnCount - 1, totalItems - 1);
    onVisibleRangeChange(start, Math.max(end, 0));
  }, [firstRow, lastRow, columnCount, totalItems, pageSize, onVisibleRangeChange, restoreVersion]);

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
            height: rowCount * rowHeight,
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
              const startIndex = virtualRow.index * columnCount;
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
