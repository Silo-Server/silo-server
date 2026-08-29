import type {
  CSSProperties,
  DragEvent,
  KeyboardEvent as ReactKeyboardEvent,
  PointerEvent as ReactPointerEvent,
} from "react";
import { useCallback, useRef, useState } from "react";
import { GripVertical, Plus, X } from "lucide-react";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { cn } from "@/lib/utils";
import { useDragAutoScroll } from "./dragAutoScroll";
import { getDashboardWidget } from "./registry";
import type { WidgetId } from "./types";
import type { DashboardLayout } from "./useDashboardLayout";
import { WidgetChromeProvider } from "./widgetChrome";

/** Fallback for the `gap` of `.admin-widget-grid` in app.css (0.875rem). */
const GRID_GAP_PX = 14;
/** Must match `--admin-row-h` on `.admin-widget-grid` in app.css (6.25rem). */
const GRID_ROW_HEIGHT_PX = 100;
const GRID_COLUMNS = 12;

/**
 * Drag payload type for adding a widget from the Add-widget sheet, distinct
 * from the `text/plain` payload a grid reorder drag carries so neither path
 * can mistake the other's drag for its own.
 */
export const WIDGET_ADD_DRAG_TYPE = "application/x-silo-widget-add";

/**
 * Row height in CSS pixels.
 *
 * Read back from `--admin-row-h` so a drag follows the stylesheet instead of a
 * second copy of the number; `GRID_ROW_HEIGHT_PX` is the fallback for anything
 * that cannot resolve the variable (jsdom, a grid that is not mounted yet).
 */
function readRowHeightPx(grid: HTMLElement | null): number {
  if (!grid) {
    return GRID_ROW_HEIGHT_PX;
  }
  const raw = window.getComputedStyle(grid).getPropertyValue("--admin-row-h").trim();
  const value = Number.parseFloat(raw);
  if (!Number.isFinite(value) || value <= 0) {
    return GRID_ROW_HEIGHT_PX;
  }
  if (raw.endsWith("rem")) {
    const root = Number.parseFloat(window.getComputedStyle(document.documentElement).fontSize);
    return Number.isFinite(root) && root > 0 ? value * root : GRID_ROW_HEIGHT_PX;
  }
  return value;
}

/**
 * Gutter width in CSS pixels.
 *
 * Read back from the resolved `column-gap` for the same reason as the row
 * height: a drag follows the stylesheet rather than a second copy of the
 * number. `GRID_GAP_PX` is the fallback for anything that cannot resolve it
 * (jsdom, a grid that is not mounted yet).
 */
function readGapPx(grid: HTMLElement | null): number {
  if (!grid) {
    return GRID_GAP_PX;
  }
  const value = Number.parseFloat(window.getComputedStyle(grid).columnGap);
  if (!Number.isFinite(value) || value < 0) {
    return GRID_GAP_PX;
  }
  return value;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

interface DropIndicator {
  id: WidgetId;
  edge: "before" | "after";
}

interface ResizePreview {
  id: WidgetId;
  span: number;
  rows: number;
}

export function DashboardGrid({
  layout,
  isAddPanelOpen,
  onAddPanelOpenChange,
}: {
  layout: DashboardLayout;
  isAddPanelOpen: boolean;
  onAddPanelOpenChange: (open: boolean) => void;
}) {
  const {
    entries,
    hiddenWidgets,
    isCustomizing,
    moveWidget,
    resizeWidget,
    setWidgetRange,
    removeWidget,
    addWidget,
  } = layout;

  const gridRef = useRef<HTMLDivElement | null>(null);
  const [draggedId, setDraggedId] = useState<WidgetId | null>(null);
  // A drag out of the Add-widget sheet. Tracked separately from `draggedId`
  // because dragover cannot read the payload, only its type — the id has to
  // come from state until the drop.
  const [addDragId, setAddDragId] = useState<WidgetId | null>(null);
  const [dropIndicator, setDropIndicator] = useState<DropIndicator | null>(null);
  const [resizePreview, setResizePreview] = useState<ResizePreview | null>(null);
  const [liveMessage, setLiveMessage] = useState("");
  // Widgets that have reported they have nothing to show. Only the ones whose
  // registry entry sets `collapsedRows` act on it; recording every report keeps
  // the grid out of the business of knowing which widget is which.
  const [collapsedIds, setCollapsedIds] = useState<readonly WidgetId[]>([]);
  const resizeSessionRef = useRef<{
    id: WidgetId;
    title: string;
    startX: number;
    startY: number;
    startSpan: number;
    startRows: number;
    columnUnit: number;
    rowUnit: number;
    minSpan: number;
    maxSpan: number;
    minRows: number;
    maxRows: number;
    latestSpan: number;
    latestRows: number;
    latestClientX: number;
    latestClientY: number;
    /** Auto-scroll during the session moves the grid under the stationary
        pointer; this accumulates that movement so the size keeps following
        where the pointer sits over the grid, not just where it last moved. */
    scrollOffsetY: number;
  } | null>(null);

  // Scrolls the page (or an inner scroll container) while a drag sits near the
  // top or bottom edge, so a widget can be dragged to an off-screen part of
  // the grid. Fed from dragover/pointermove; stopped on every drag ending.
  const autoScroll = useDragAutoScroll(gridRef);

  const setWidgetCollapsed = useCallback((id: WidgetId, collapsed: boolean) => {
    setCollapsedIds((prev) => {
      // Returning `prev` unchanged is what keeps a widget reporting the same
      // value every render from looping through the grid and back.
      if (prev.includes(id) === collapsed) return prev;
      return collapsed ? [...prev, id] : prev.filter((other) => other !== id);
    });
  }, []);

  const findWidgetIdFromEvent = useCallback((event: DragEvent<HTMLElement>): WidgetId | null => {
    const target = event.target as HTMLElement | null;
    const host = target?.closest<HTMLElement>("[data-widget-id]");
    return (host?.dataset.widgetId as WidgetId | undefined) ?? null;
  }, []);

  const handleDragStart = useCallback(
    (event: DragEvent<HTMLElement>) => {
      if (!isCustomizing) return;
      const id = findWidgetIdFromEvent(event);
      if (!id) return;
      event.dataTransfer.effectAllowed = "move";
      event.dataTransfer.setData("text/plain", id);
      setDraggedId(id);
    },
    [findWidgetIdFromEvent, isCustomizing],
  );

  const handleDragEnd = useCallback(() => {
    autoScroll.stop();
    setDraggedId(null);
    setDropIndicator(null);
  }, [autoScroll]);

  /** True when the drag carries the Add-widget sheet's payload type. */
  const isAddDrag = useCallback(
    (event: DragEvent<HTMLElement>) =>
      addDragId !== null && event.dataTransfer.types.includes(WIDGET_ADD_DRAG_TYPE),
    [addDragId],
  );

  /**
   * Which placed widget the pointer is over and which side of it, or null when
   * the drag is over the grid's trailing area rather than a widget.
   */
  const resolveDropEdge = useCallback(
    (event: DragEvent<HTMLElement>): DropIndicator | null => {
      const overId = findWidgetIdFromEvent(event);
      if (!overId) return null;
      const host = (event.target as HTMLElement).closest<HTMLElement>("[data-widget-id]");
      if (!host) return null;
      const rect = host.getBoundingClientRect();
      return { id: overId, edge: event.clientX < rect.left + rect.width / 2 ? "before" : "after" };
    },
    [findWidgetIdFromEvent],
  );

  /** The entry the payload should land in front of, or null to append. */
  const resolveBeforeId = useCallback(
    (event: DragEvent<HTMLElement>): WidgetId | null => {
      const target = resolveDropEdge(event);
      if (!target) return null;
      if (target.edge === "before") return target.id;
      const overIndex = entries.findIndex((entry) => entry.id === target.id);
      const nextEntry = overIndex === -1 ? undefined : entries[overIndex + 1];
      return nextEntry ? nextEntry.id : null;
    },
    [entries, resolveDropEdge],
  );

  const handleDragOver = useCallback(
    (event: DragEvent<HTMLElement>) => {
      if (isAddDrag(event)) {
        event.preventDefault();
        event.dataTransfer.dropEffect = "copy";
        const target = resolveDropEdge(event);
        // Over the trailing area the drop appends, shown as an "after" edge on
        // the last widget; over a widget the indicator matches a reorder's.
        const lastId = entries[entries.length - 1]?.id;
        const indicator: DropIndicator | null =
          target ?? (lastId ? { id: lastId, edge: "after" } : null);
        setDropIndicator((prev) =>
          prev?.id === indicator?.id && prev?.edge === indicator?.edge ? prev : indicator,
        );
        autoScroll.update(event.clientY);
        return;
      }
      if (!draggedId) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = "move";
      autoScroll.update(event.clientY);
      // Over the trailing area a reorder appends, exactly like a sheet add;
      // the indicator is the "after" edge of the last widget unless the
      // dragged widget already is the last one.
      const lastId = entries[entries.length - 1]?.id;
      const target =
        resolveDropEdge(event) ??
        (lastId && lastId !== draggedId ? { id: lastId, edge: "after" as const } : null);
      if (!target || target.id === draggedId) {
        setDropIndicator(null);
        return;
      }
      setDropIndicator((prev) =>
        prev?.id === target.id && prev.edge === target.edge ? prev : target,
      );
    },
    [autoScroll, draggedId, entries, isAddDrag, resolveDropEdge],
  );

  const handleDrop = useCallback(
    (event: DragEvent<HTMLElement>) => {
      autoScroll.stop();
      if (isAddDrag(event)) {
        event.preventDefault();
        // The payload is authoritative; the state id is the fallback for a
        // dataTransfer that cannot round-trip custom types.
        const payloadId = event.dataTransfer.getData(WIDGET_ADD_DRAG_TYPE) as WidgetId;
        const id = payloadId || addDragId;
        setAddDragId(null);
        setDropIndicator(null);
        if (!id) return;
        const beforeId = resolveBeforeId(event);
        addWidget(id, beforeId);
        const position =
          beforeId === null
            ? entries.length + 1
            : Math.max(1, entries.findIndex((entry) => entry.id === beforeId) + 1);
        setLiveMessage(
          `${getDashboardWidget(id).title} added at position ${position} of ${entries.length + 1}`,
        );
        onAddPanelOpenChange(false);
        return;
      }
      if (!draggedId) return;
      event.preventDefault();
      const overId = findWidgetIdFromEvent(event);
      if (overId && overId !== draggedId) {
        moveWidget(draggedId, resolveBeforeId(event));
      } else if (!overId) {
        // The trailing area appends for reorders too — the sheet-add path
        // already treats it that way, and a silent no-op strands the last
        // position for mouse users.
        moveWidget(draggedId, null);
      }
      setDraggedId(null);
      setDropIndicator(null);
    },
    [
      addDragId,
      addWidget,
      autoScroll,
      draggedId,
      entries,
      findWidgetIdFromEvent,
      isAddDrag,
      moveWidget,
      onAddPanelOpenChange,
      resolveBeforeId,
    ],
  );

  const handleResizePointerDown = useCallback(
    (
      event: ReactPointerEvent<HTMLButtonElement>,
      id: WidgetId,
      currentSpan: number,
      currentRows: number,
    ) => {
      if (!isCustomizing) return;
      // Only start on a primary-button press: a right-click opens the context
      // menu and never delivers the matching pointerup, which would leave the
      // resize session stuck.
      if (!event.isPrimary || event.button !== 0) return;
      const widget = getDashboardWidget(id);
      event.preventDefault();
      event.stopPropagation();
      event.currentTarget.setPointerCapture(event.pointerId);
      const gridWidth = gridRef.current?.getBoundingClientRect().width ?? 0;
      const gapPx = readGapPx(gridRef.current);
      const columnUnit = gridWidth > 0 ? (gridWidth + gapPx) / GRID_COLUMNS : 1;
      resizeSessionRef.current = {
        id,
        title: widget.title,
        startX: event.clientX,
        startY: event.clientY,
        startSpan: currentSpan,
        startRows: currentRows,
        columnUnit,
        rowUnit: readRowHeightPx(gridRef.current) + gapPx,
        minSpan: widget.minSpan,
        maxSpan: widget.maxSpan,
        minRows: widget.minRows,
        maxRows: widget.maxRows,
        latestSpan: currentSpan,
        latestRows: currentRows,
        latestClientX: event.clientX,
        latestClientY: event.clientY,
        scrollOffsetY: 0,
      };
      setResizePreview({ id, span: currentSpan, rows: currentRows });
    },
    [isCustomizing],
  );

  /**
   * Recomputes the previewed size from the session's latest pointer position.
   * `scrollOffsetY` folds auto-scroll into the vertical delta: scrolling moves
   * the widget up under a stationary pointer, which is the same gesture as
   * dragging the handle down by that much.
   */
  const applyResizePreview = useCallback(() => {
    const session = resizeSessionRef.current;
    if (!session) return;
    // Each axis is clamped to its own range, so a widget with a pinned width
    // still grows in height and never drifts sideways under the pointer.
    const nextSpan = clamp(
      Math.round(session.startSpan + (session.latestClientX - session.startX) / session.columnUnit),
      session.minSpan,
      session.maxSpan,
    );
    const nextRows = clamp(
      Math.round(
        session.startRows +
          (session.latestClientY + session.scrollOffsetY - session.startY) / session.rowUnit,
      ),
      session.minRows,
      session.maxRows,
    );
    if (nextSpan === session.latestSpan && nextRows === session.latestRows) {
      return;
    }
    session.latestSpan = nextSpan;
    session.latestRows = nextRows;
    setResizePreview({ id: session.id, span: nextSpan, rows: nextRows });
  }, []);

  /** Auto-scroll moved the grid mid-resize; fold it in and re-derive the size. */
  const handleResizeAutoScrolled = useCallback(
    (dy: number) => {
      const session = resizeSessionRef.current;
      if (!session) return;
      session.scrollOffsetY += dy;
      applyResizePreview();
    },
    [applyResizePreview],
  );

  const handleResizePointerMove = useCallback(
    (event: ReactPointerEvent<HTMLButtonElement>) => {
      const session = resizeSessionRef.current;
      if (!session) return;
      session.latestClientX = event.clientX;
      session.latestClientY = event.clientY;
      applyResizePreview();
      // A widget can be taller than the viewport, so growing it needs the same
      // edge auto-scroll a reorder drag gets.
      autoScroll.update(event.clientY, handleResizeAutoScrolled);
    },
    [applyResizePreview, autoScroll, handleResizeAutoScrolled],
  );

  const handleResizePointerEnd = useCallback(
    (event: ReactPointerEvent<HTMLButtonElement>) => {
      const session = resizeSessionRef.current;
      if (!session) return;
      autoScroll.stop();
      resizeSessionRef.current = null;
      setResizePreview(null);
      if (event.currentTarget.hasPointerCapture(event.pointerId)) {
        event.currentTarget.releasePointerCapture(event.pointerId);
      }
      resizeWidget(session.id, { span: session.latestSpan, rows: session.latestRows });
      setLiveMessage(
        `${session.title} resized to ${session.latestSpan} of ${GRID_COLUMNS} columns × ${session.latestRows} ${session.latestRows === 1 ? "row" : "rows"}`,
      );
    },
    [autoScroll, resizeWidget],
  );

  const handleMoveKeyDown = useCallback(
    (event: ReactKeyboardEvent<HTMLButtonElement>, id: WidgetId) => {
      const backward = event.key === "ArrowLeft" || event.key === "ArrowUp";
      const forward = event.key === "ArrowRight" || event.key === "ArrowDown";
      if (!backward && !forward) return;
      event.preventDefault();
      const index = entries.findIndex((entry) => entry.id === id);
      if (index === -1) return;
      const nextIndex = backward ? index - 1 : index + 1;
      const neighbor = entries[nextIndex];
      if (!neighbor) return;
      if (backward) {
        moveWidget(id, neighbor.id);
      } else {
        const after = entries[index + 2];
        moveWidget(id, after ? after.id : null);
      }
      setLiveMessage(
        `${getDashboardWidget(id).title} moved to position ${nextIndex + 1} of ${entries.length}`,
      );
    },
    [entries, moveWidget],
  );

  const handleResizeKeyDown = useCallback(
    (
      event: ReactKeyboardEvent<HTMLButtonElement>,
      id: WidgetId,
      currentSpan: number,
      currentRows: number,
    ) => {
      // The corner handle owns both axes: left/right walk columns, up/down walk
      // rows, matching the direction the same drag would take.
      const columnStep =
        event.key === "ArrowLeft" ? -1 : event.key === "ArrowRight" ? 1 : undefined;
      const rowStep = event.key === "ArrowUp" ? -1 : event.key === "ArrowDown" ? 1 : undefined;
      if (columnStep === undefined && rowStep === undefined) return;
      event.preventDefault();
      const widget = getDashboardWidget(id);
      const nextSpan = clamp(currentSpan + (columnStep ?? 0), widget.minSpan, widget.maxSpan);
      const nextRows = clamp(currentRows + (rowStep ?? 0), widget.minRows, widget.maxRows);
      if (nextSpan === currentSpan && nextRows === currentRows) return;
      resizeWidget(id, { span: nextSpan, rows: nextRows });
      setLiveMessage(
        `${widget.title} resized to ${nextSpan} of ${GRID_COLUMNS} columns × ${nextRows} ${nextRows === 1 ? "row" : "rows"}`,
      );
    },
    [resizeWidget],
  );

  const isResizing = resizePreview !== null;

  return (
    <>
      <span aria-live="polite" role="status" className="sr-only">
        {liveMessage}
      </span>
      <div
        ref={gridRef}
        className="admin-widget-grid"
        onDragStart={handleDragStart}
        onDragEnd={handleDragEnd}
        onDragOver={handleDragOver}
        onDrop={handleDrop}
      >
        {entries.map((entry) => {
          const widget = getDashboardWidget(entry.id);
          const isWidgetResizing = resizePreview?.id === entry.id;
          // Customize mode always renders full size: the admin is aiming at
          // drag and resize targets, and a widget that shrank under the pointer
          // because its data went quiet would be unresizable.
          const isCollapsed =
            !isCustomizing && widget.collapsedRows !== undefined && collapsedIds.includes(entry.id);
          const span = isWidgetResizing ? resizePreview.span : entry.span;
          const rows = isWidgetResizing
            ? resizePreview.rows
            : isCollapsed
              ? (widget.collapsedRows ?? entry.rows)
              : entry.rows;
          const canResize = widget.minSpan !== widget.maxSpan || widget.minRows !== widget.maxRows;
          const WidgetComponent = widget.Component;

          return (
            <div
              key={entry.id}
              data-widget-id={entry.id}
              data-collapsed={isCollapsed ? "true" : undefined}
              className={cn(
                "admin-widget",
                span >= 6 && "admin-widget-wide",
                isCustomizing && "rounded-2xl",
                draggedId === entry.id && "opacity-40",
              )}
              style={{ "--widget-span": span, "--widget-rows": rows } as CSSProperties}
              draggable={isCustomizing && !isResizing}
            >
              {/* The window is resolved here rather than in the widget: the
                  entry may predate the widget gaining ranges, in which case the
                  registry's default is what it has always been showing. */}
              <WidgetChromeProvider
                id={entry.id}
                range={entry.range ?? widget.ranges?.default}
                setRange={setWidgetRange}
                setCollapsed={setWidgetCollapsed}
              >
                <WidgetComponent />
              </WidgetChromeProvider>

              {isCustomizing && (
                <>
                  <div
                    aria-hidden="true"
                    className={cn(
                      "border-primary/40 pointer-events-none absolute -inset-1 z-10 rounded-2xl border-2 border-dashed",
                      isWidgetResizing && "border-primary/80 border-solid",
                    )}
                  />

                  {dropIndicator?.id === entry.id && (
                    <div
                      aria-hidden="true"
                      className={cn(
                        "bg-primary pointer-events-none absolute inset-y-0 z-30 w-1 rounded-full",
                        dropIndicator.edge === "before" ? "-left-2.5" : "-right-2.5",
                      )}
                    />
                  )}

                  <div className="border-border bg-background/95 absolute -top-3 right-3 z-20 flex items-center gap-0.5 rounded-full border px-1 py-0.5 shadow-md backdrop-blur">
                    <button
                      type="button"
                      aria-label={`Move ${widget.title} (drag, or arrow keys)`}
                      title="Drag or use arrow keys to move"
                      className="text-muted-foreground hover:text-foreground focus-visible:ring-ring flex h-6 w-6 cursor-grab items-center justify-center rounded-full focus-visible:ring-2 focus-visible:outline-none active:cursor-grabbing"
                      onKeyDown={(event) => handleMoveKeyDown(event, entry.id)}
                    >
                      <GripVertical className="h-3.5 w-3.5" />
                    </button>
                    <button
                      type="button"
                      aria-label={`Remove ${widget.title}`}
                      title="Remove"
                      className="text-muted-foreground hover:text-destructive focus-visible:ring-ring flex h-6 w-6 cursor-pointer items-center justify-center rounded-full focus-visible:ring-2 focus-visible:outline-none"
                      onClick={() => removeWidget(entry.id)}
                    >
                      <X className="h-3.5 w-3.5" />
                    </button>
                  </div>

                  {/* One handle for both axes, straddling the bottom-right
                      corner. Column spans (and row heights) only apply from lg
                      up, so the handle is hidden below that. */}
                  {canResize && (
                    <button
                      type="button"
                      aria-label={`Resize ${widget.title} (drag, or arrow keys)`}
                      title="Drag or use arrow keys to resize"
                      className={cn(
                        "border-border bg-background/95 hover:border-primary hover:bg-primary/30 focus-visible:ring-ring absolute -right-1.5 -bottom-1.5 z-20 hidden h-3.5 w-3.5 cursor-nwse-resize touch-none rounded-[4px] border shadow-md focus-visible:ring-2 focus-visible:outline-none lg:block",
                        isWidgetResizing && "border-primary bg-primary/30",
                      )}
                      onPointerDown={(event) =>
                        handleResizePointerDown(event, entry.id, entry.span, entry.rows)
                      }
                      onPointerMove={handleResizePointerMove}
                      onPointerUp={handleResizePointerEnd}
                      onPointerCancel={handleResizePointerEnd}
                      onLostPointerCapture={handleResizePointerEnd}
                      onKeyDown={(event) =>
                        handleResizeKeyDown(event, entry.id, entry.span, entry.rows)
                      }
                    />
                  )}

                  {isWidgetResizing && (
                    <span
                      aria-hidden="true"
                      className="bg-primary text-primary-foreground absolute -top-3 left-1/2 z-30 -translate-x-1/2 rounded-full px-2 py-0.5 text-[10px] font-bold whitespace-nowrap tabular-nums shadow-md"
                    >
                      {span} × {rows}
                    </span>
                  )}
                </>
              )}
            </div>
          );
        })}
      </div>

      <Sheet open={isAddPanelOpen} onOpenChange={onAddPanelOpenChange} modal={false}>
        {/* The sheet is non-modal, so Radix renders no overlay and the grid
            behind it stays a live drop target. During an add-drag the panel
            itself still covers the grid's right edge, so it goes transparent
            to the pointer and fades — but stays mounted: unmounting the drag
            source mid-drag cancels the drag in some engines. */}
        <SheetContent
          side="right"
          className={cn(
            "w-80 gap-2 sm:max-w-sm",
            addDragId !== null && "pointer-events-none opacity-30",
          )}
          onInteractOutside={(event) => event.preventDefault()}
        >
          <SheetHeader className="pb-0">
            <SheetTitle>Add widget</SheetTitle>
            <SheetDescription>
              Widgets you&apos;ve removed or haven&apos;t placed yet.
            </SheetDescription>
          </SheetHeader>
          <div className="overlay-scroll flex flex-col gap-2 overflow-y-auto p-4 pt-2">
            {hiddenWidgets.length === 0 ? (
              <p className="text-muted-foreground py-4 text-sm">
                Everything is on the dashboard already.
              </p>
            ) : (
              hiddenWidgets.map((widget) => (
                <button
                  key={widget.id}
                  type="button"
                  className="border-border hover:border-primary/50 hover:bg-accent/40 focus-visible:ring-ring flex cursor-pointer items-start gap-3 rounded-xl border p-3 text-left transition-colors focus-visible:ring-2 focus-visible:outline-none"
                  onClick={() => addWidget(widget.id)}
                  // Dragging is the pointer shortcut for placing the widget at
                  // a specific spot; clicking (the keyboard path) appends.
                  draggable
                  onDragStart={(event) => {
                    event.dataTransfer.setData(WIDGET_ADD_DRAG_TYPE, widget.id);
                    event.dataTransfer.effectAllowed = "copy";
                    setAddDragId(widget.id);
                  }}
                  onDragEnd={() => {
                    // Fires for cancelled and completed drags alike; the drop
                    // handler has already applied any placement by now.
                    autoScroll.stop();
                    setAddDragId(null);
                    setDropIndicator(null);
                  }}
                >
                  <span className="bg-primary/10 text-primary flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-md">
                    <Plus className="h-3.5 w-3.5" />
                  </span>
                  <span className="min-w-0">
                    <span className="block text-sm font-semibold">{widget.title}</span>
                    <span className="text-muted-foreground block text-xs">
                      {widget.description}
                    </span>
                  </span>
                </button>
              ))
            )}
          </div>
        </SheetContent>
      </Sheet>
    </>
  );
}
