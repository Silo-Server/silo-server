import { useEffect, useCallback, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { useWindowVirtualizer } from "@tanstack/react-virtual";
import type { BrowseItem } from "@/api/types";
import ItemCard from "./ItemCard";
import { Skeleton } from "@/components/ui/skeleton";
import { useGridLayout } from "@/hooks/useGridLayout";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { useUICustomization } from "@/hooks/useUICustomization";
import { cardGridClasses, cardTextAreaHeight } from "@/lib/uiCustomization";

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
}

interface WindowedItemGridProps extends SharedItemGridProps {
  totalItems: number;
  pages: Map<number, BrowseItem[]>;
  pageSize: number;
  onVisibleRangeChange: (startIndex: number, endIndex: number) => void;
  items?: never;
}

interface StaticItemGridProps extends SharedItemGridProps {
  items: BrowseItem[];
  totalItems?: never;
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
    textAreaHeight: cardTextAreaHeight(cardPresentation.caption),
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

  useEffect(() => {
    const start = firstRow * columnCount;
    const end = Math.min((lastRow + 1) * columnCount - 1, totalItems - 1);
    onVisibleRangeChange(start, Math.max(end, 0));
  }, [firstRow, lastRow, columnCount, totalItems, onVisibleRangeChange]);

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
