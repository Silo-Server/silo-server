import { useEffect, useCallback, useState } from "react";
import { useWindowVirtualizer } from "@tanstack/react-virtual";
import type { BrowseItem } from "@/api/types";
import ItemCard from "./ItemCard";
import { Skeleton } from "@/components/ui/skeleton";
import { useGridLayout } from "@/hooks/useGridLayout";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";

interface SharedItemGridProps {
  loading?: boolean;
  sortField?: string;
  libraryId?: number;
  selectionMode?: boolean;
  selectedIds?: ReadonlySet<string>;
  onToggleSelect?: (item: BrowseItem) => void;
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

const GRID_GAP = 12;
const TEXT_AREA_HEIGHT = 44;
const GRID_CLASSES =
  "grid grid-cols-3 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-7 xl:grid-cols-8 gap-3";

export default function ItemGrid(props: ItemGridProps) {
  const {
    loading,
    sortField,
    libraryId,
    selectionMode = false,
    selectedIds,
    onToggleSelect,
  } = props;
  const { prefs: overlayPrefs } = useOverlayPrefs();
  const totalItems = hasStaticItems(props) ? props.items.length : props.totalItems;
  const pages = hasStaticItems(props)
    ? new Map<number, BrowseItem[]>([[0, props.items]])
    : props.pages;
  const pageSize = hasStaticItems(props) ? Math.max(props.items.length, 1) : props.pageSize;
  const onVisibleRangeChange = hasStaticItems(props) ? () => undefined : props.onVisibleRangeChange;
  const { containerRef, layout } = useGridLayout({
    gap: GRID_GAP,
    textAreaHeight: TEXT_AREA_HEIGHT,
  });
  const [anchorEl, setAnchorEl] = useState<HTMLDivElement | null>(null);
  const { columnCount, rowHeight } = layout;
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

  const virtualRows = virtualizer.getVirtualItems();

  // Report visible item range to parent for page fetching
  const firstRow = virtualRows[0]?.index ?? 0;
  const lastRow = virtualRows[virtualRows.length - 1]?.index ?? 0;

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
        <div ref={containerRef} role="list" className={GRID_CLASSES}>
          {Array.from({ length: 24 }).map((_, i) => (
            <div key={i} role="listitem">
              <Skeleton className="aspect-[2/3] rounded-lg" />
              <Skeleton className="mt-2 h-4 w-3/4" />
            </div>
          ))}
        </div>
      ) : totalItems === 0 ? (
        <div className="text-muted-foreground py-12 text-center">No items found.</div>
      ) : (
        <div
          style={{
            height: virtualizer.getTotalSize(),
            position: "relative",
            overflow: "visible",
          }}
        >
          <div
            ref={containerRef}
            role="list"
            className={GRID_CLASSES}
            style={{
              position: "absolute",
              top: 0,
              left: 0,
              right: 0,
              overflow: "visible",
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
                    <div key={item.content_id} role="listitem">
                      <ItemCard
                        item={item}
                        libraryId={libraryId}
                        sortField={sortField}
                        overlayPrefs={overlayPrefs}
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
