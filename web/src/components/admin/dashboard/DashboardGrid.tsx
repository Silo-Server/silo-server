import type {
  CSSProperties,
  DragEvent,
  KeyboardEvent as ReactKeyboardEvent,
  PointerEvent as ReactPointerEvent,
} from "react";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { GripVertical, Plus, X } from "lucide-react";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";
import { type AutoScrollGesture, useDragAutoScroll } from "./dragAutoScroll";
import {
  CUSTOMIZE_GRID_CLASS,
  GRID_COLUMNS,
  readColumnGapPx,
  readRowGapPx,
  readRowHeightPx,
} from "./gridMetrics";
import { getDashboardWidget } from "./registry";
import type { WidgetId } from "./types";
import type { DashboardLayout } from "./useDashboardLayout";
import { WidgetChromeProvider } from "./widgetChrome";
import { isRightToLeft, readingArrowKey } from "@/lib/direction";
import { Direction } from "radix-ui";

/**
 * Drag payload type for adding a widget from the Add-widget sheet, distinct
 * from the `text/plain` payload a grid reorder drag carries so neither path
 * can mistake the other's drag for its own.
 */
const WIDGET_ADD_DRAG_TYPE = "application/x-silo-widget-add";

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

/**
 * The widget order and sizes, which together place every widget on the grid.
 * A range or view change leaves it alone.
 */
function layoutGeometry(entries: DashboardLayout["entries"]): string {
  return entries.map((entry) => `${entry.id}:${entry.span}:${entry.rows}`).join(",");
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
    setWidgetView,
    removeWidget,
    addWidget,
  } = layout;
  const t = useT();
  const dir = Direction.useDirection();

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
    /** The resize handle the session started from. */
    handle: Element;
    /** The pointer that pressed it; other pointers' moves and releases are ignored. */
    pointerId: number;
    /** `layoutGeometry` of the layout the start size and position were measured on. */
    geometry: string;
    title: string;
    startX: number;
    /** 1, or -1 right to left, where the handle grows the widget leftward. */
    inlineSign: number;
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

  // The node the current drag started from: the dragged widget, a link, image,
  // or selected Text node inside it, or the Add widget sheet item.
  const dragSourceRef = useRef<Node | null>(null);

  const endDrag = useCallback(() => {
    dragSourceRef.current = null;
    setDraggedId(null);
    setAddDragId(null);
    setDropIndicator(null);
  }, []);

  const endResize = useCallback(() => {
    resizeSessionRef.current = null;
    setResizePreview(null);
  }, []);

  const isResizing = resizePreview !== null;
  const isDragging = draggedId !== null || addDragId !== null;
  let autoScrollGesture: AutoScrollGesture | null = null;
  if (isDragging) autoScrollGesture = "drag";
  else if (isResizing) autoScrollGesture = "pointer";
  const gestureActive = autoScrollGesture !== null;

  // Any render can take a gesture's source out of the document mid-gesture,
  // this grid's or a widget's own: the server layout arriving while the page
  // loads, a widget's rows refreshing under a dragged link, or Customize
  // ending under a resize. Its dragend or pointerup then never reaches React,
  // so the gesture ends here. A browser drag itself goes on, but the grid no
  // longer accepts it. The check runs after each batch of DOM changes, so a
  // node React moves (removed and inserted again) doesn't count.
  useEffect(() => {
    if (!gestureActive) return;
    const observer = new MutationObserver(() => {
      if (dragSourceRef.current?.isConnected === false) endDrag();
      if (resizeSessionRef.current?.handle.isConnected === false) endResize();
    });
    observer.observe(document.body, { childList: true, subtree: true });
    return () => observer.disconnect();
  }, [endDrag, endResize, gestureActive]);

  // Scrolls the page (or an inner scroll container) while a drag or resize
  // sits near the top or bottom edge, so a widget can be dragged to an
  // off-screen part of the grid. It runs for as long as one of them is active.
  const { update: updateAutoScroll } = useDragAutoScroll(gridRef, autoScrollGesture);

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
      // One gesture at a time: a link or image inside a widget stays draggable
      // while another pointer resizes.
      if (resizeSessionRef.current) {
        event.preventDefault();
        return;
      }
      event.dataTransfer.effectAllowed = "move";
      event.dataTransfer.setData("text/plain", id);
      // The native target: a selection drag starts from a Text node, which
      // React reports as its parent element.
      dragSourceRef.current = event.nativeEvent.target as Node;
      setDraggedId(id);
    },
    [findWidgetIdFromEvent, isCustomizing],
  );

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
      // Above the widget (its pill, or the gap over it in Customize mode) is
      // before it. Otherwise "before" is the start side: the left half, or the
      // right half right to left.
      if (event.clientY < rect.top) return { id: overId, edge: "before" };
      const leftHalf = event.clientX < rect.left + rect.width / 2;
      return { id: overId, edge: leftHalf !== (dir === "rtl") ? "before" : "after" };
    },
    [dir, findWidgetIdFromEvent],
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
        return;
      }
      if (!draggedId) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = "move";
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
    [draggedId, entries, isAddDrag, resolveDropEdge],
  );

  const handleDrop = useCallback(
    (event: DragEvent<HTMLElement>) => {
      if (isAddDrag(event)) {
        event.preventDefault();
        // The payload is authoritative; the state id is the fallback for a
        // dataTransfer that cannot round-trip custom types.
        const payloadId = event.dataTransfer.getData(WIDGET_ADD_DRAG_TYPE) as WidgetId;
        const id = payloadId || addDragId;
        endDrag();
        if (!id) return;
        const beforeId = resolveBeforeId(event);
        addWidget(id, beforeId);
        const position =
          beforeId === null
            ? entries.length + 1
            : Math.max(1, entries.findIndex((entry) => entry.id === beforeId) + 1);
        setLiveMessage(
          t("web.admin.dashboard.grid.added", {
            title: t(getDashboardWidget(id).titleKey),
            position: position,
            total: entries.length + 1,
          }),
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
      endDrag();
    },
    [
      addDragId,
      addWidget,
      draggedId,
      endDrag,
      entries,
      findWidgetIdFromEvent,
      isAddDrag,
      moveWidget,
      onAddPanelOpenChange,
      resolveBeforeId,
      t,
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
      // One gesture at a time: a mouse press is primary even while a touch
      // resizes or drags.
      if (resizeSessionRef.current || dragSourceRef.current) return;
      const widget = getDashboardWidget(id);
      event.preventDefault();
      event.stopPropagation();
      event.currentTarget.setPointerCapture(event.pointerId);
      const gridWidth = gridRef.current?.getBoundingClientRect().width ?? 0;
      const gapPx = readColumnGapPx(gridRef.current);
      const columnUnit = gridWidth > 0 ? (gridWidth + gapPx) / GRID_COLUMNS : 1;
      resizeSessionRef.current = {
        id,
        handle: event.currentTarget,
        pointerId: event.pointerId,
        geometry: layoutGeometry(entries),
        title: t(widget.titleKey),
        startX: event.clientX,
        inlineSign: isRightToLeft(event.currentTarget) ? -1 : 1,
        startY: event.clientY,
        startSpan: currentSpan,
        startRows: currentRows,
        columnUnit,
        rowUnit: readRowHeightPx(gridRef.current) + readRowGapPx(gridRef.current),
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
    [entries, isCustomizing, t],
  );

  // Another control can change the layout under a pointer resize: Reset or a
  // Remove activated from the keyboard while the mouse holds a handle, or a
  // different server layout arriving. The session's start size and position
  // belong to the old layout, so the resize ends without saving rather than
  // writing a size from before the change. A keyboard change commits before
  // the browser can deliver the release. A server layout commits in a later
  // task, so a release in the few milliseconds before then still saves.
  useLayoutEffect(() => {
    const session = resizeSessionRef.current;
    if (session && session.geometry !== layoutGeometry(entries)) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- the session lives in a ref, which a render cannot clear; ending it before paint keeps the release from saving
      endResize();
      // Released after the session ends, so the lost-capture event it fires
      // finds no session and saves nothing.
      if (session.handle.hasPointerCapture(session.pointerId)) {
        session.handle.releasePointerCapture(session.pointerId);
      }
    }
  }, [endResize, entries]);

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
      Math.round(
        session.startSpan +
          ((session.latestClientX - session.startX) * session.inlineSign) / session.columnUnit,
      ),
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
      if (session?.pointerId !== event.pointerId) return;
      session.latestClientX = event.clientX;
      session.latestClientY = event.clientY;
      applyResizePreview();
      // A widget can be taller than the viewport, so growing it needs the same
      // edge auto-scroll a reorder drag gets.
      updateAutoScroll(event.clientY, handleResizeAutoScrolled);
    },
    [applyResizePreview, handleResizeAutoScrolled, updateAutoScroll],
  );

  const handleResizePointerEnd = useCallback(
    (event: ReactPointerEvent<HTMLButtonElement>) => {
      const session = resizeSessionRef.current;
      if (session?.pointerId !== event.pointerId) return;
      endResize();
      if (event.currentTarget.hasPointerCapture(event.pointerId)) {
        event.currentTarget.releasePointerCapture(event.pointerId);
      }
      resizeWidget(session.id, { span: session.latestSpan, rows: session.latestRows });
      setLiveMessage(
        t("web.admin.dashboard.grid.resized", {
          title: session.title,
          span: session.latestSpan,
          columns: GRID_COLUMNS,
          rows: session.latestRows,
        }),
      );
    },
    [endResize, resizeWidget, t],
  );

  const handleMoveKeyDown = useCallback(
    (event: ReactKeyboardEvent<HTMLButtonElement>, id: WidgetId) => {
      const key = readingArrowKey(event);
      const backward = key === "ArrowLeft" || key === "ArrowUp";
      const forward = key === "ArrowRight" || key === "ArrowDown";
      if (!backward && !forward) return;
      event.preventDefault();
      // One gesture at a time: a pointer press on a resize handle leaves focus
      // on this button, and reordering would cut that resize short.
      if (resizeSessionRef.current) return;
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
        t("web.admin.dashboard.grid.moved", {
          title: t(getDashboardWidget(id).titleKey),
          position: nextIndex + 1,
          total: entries.length,
        }),
      );
    },
    [entries, moveWidget, t],
  );

  const handleResizeKeyDown = useCallback(
    (
      event: ReactKeyboardEvent<HTMLButtonElement>,
      id: WidgetId,
      currentSpan: number,
      currentRows: number,
    ) => {
      // The corner handle owns both axes: left/right walk columns, up/down walk
      // rows, matching the direction the same drag would take. The handle sits
      // on the end corner, so right to left the keys mirror too.
      const key = readingArrowKey(event);
      const columnStep = key === "ArrowLeft" ? -1 : key === "ArrowRight" ? 1 : undefined;
      const rowStep = event.key === "ArrowUp" ? -1 : event.key === "ArrowDown" ? 1 : undefined;
      if (columnStep === undefined && rowStep === undefined) return;
      event.preventDefault();
      // One gesture at a time: a pointer holding a handle owns the size, and
      // its release would overwrite the step anyway.
      if (resizeSessionRef.current) return;
      const widget = getDashboardWidget(id);
      const nextSpan = clamp(currentSpan + (columnStep ?? 0), widget.minSpan, widget.maxSpan);
      const nextRows = clamp(currentRows + (rowStep ?? 0), widget.minRows, widget.maxRows);
      if (nextSpan === currentSpan && nextRows === currentRows) return;
      resizeWidget(id, { span: nextSpan, rows: nextRows });
      setLiveMessage(
        t("web.admin.dashboard.grid.resized", {
          title: t(widget.titleKey),
          span: nextSpan,
          columns: GRID_COLUMNS,
          rows: nextRows,
        }),
      );
    },
    [resizeWidget, t],
  );

  return (
    <>
      <span aria-live="polite" role="status" className="sr-only">
        {liveMessage}
      </span>
      <div
        ref={gridRef}
        className={cn("admin-widget-grid", isCustomizing && CUSTOMIZE_GRID_CLASS)}
        onDragStart={handleDragStart}
        onDragEnd={endDrag}
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
          const title = t(widget.titleKey);

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
                view={entry.view ?? widget.views?.default}
                setView={setWidgetView}
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
                        dropIndicator.edge === "before" ? "-start-2.5" : "-end-2.5",
                      )}
                    />
                  )}

                  {/* The gap above the widget belongs to it while something is
                      dragged, so a drop there lands before it instead of at
                      the end of the dashboard, where drops on the grid's own
                      space go. It is the row gap's height and reaches half the
                      column gap (0.875rem in app.css) to each side, so the
                      areas of neighbors meet where the gaps cross. */}
                  {isDragging && (
                    <div
                      aria-hidden="true"
                      className="absolute -inset-x-1.75 bottom-full h-(--customize-row-gap)"
                    />
                  )}

                  {/* Above the widget, in the gap CUSTOMIZE_GRID_CLASS makes
                      room for, so it never covers the widget's label, icon,
                      or links. */}
                  <div className="border-border bg-background/95 absolute end-3 bottom-full z-20 flex items-center gap-0.5 rounded-full border px-1 py-0.5 shadow-md backdrop-blur">
                    <button
                      type="button"
                      aria-label={t("web.admin.dashboard.grid.moveLabel", { title })}
                      title={t("web.admin.dashboard.grid.moveHint")}
                      className="text-muted-foreground hover:text-foreground focus-visible:ring-ring flex h-6 w-6 cursor-grab items-center justify-center rounded-full focus-visible:ring-2 focus-visible:outline-none active:cursor-grabbing"
                      onKeyDown={(event) => handleMoveKeyDown(event, entry.id)}
                    >
                      <GripVertical className="h-3.5 w-3.5" />
                    </button>
                    <button
                      type="button"
                      aria-label={t("web.admin.dashboard.grid.removeLabel", { title })}
                      title={t("web.admin.dashboard.grid.removeHint")}
                      className="text-muted-foreground hover:text-destructive focus-visible:ring-ring flex h-6 w-6 cursor-pointer items-center justify-center rounded-full focus-visible:ring-2 focus-visible:outline-none"
                      onClick={() => removeWidget(entry.id)}
                    >
                      <X className="h-3.5 w-3.5" />
                    </button>
                  </div>

                  {/* One handle for both axes, straddling the bottom-right
                      corner. Column spans (and row heights) only apply in the
                      12-column layout, from lg and 1024px up (see
                      .admin-widget-grid in app.css), so the handle is hidden
                      below that. */}
                  {canResize && (
                    <button
                      type="button"
                      aria-label={t("web.admin.dashboard.grid.resizeLabel", { title })}
                      title={t("web.admin.dashboard.grid.resizeHint")}
                      className={cn(
                        "border-border bg-background/95 hover:border-primary hover:bg-primary/30 focus-visible:ring-ring absolute -end-1.5 -bottom-1.5 z-20 hidden h-3.5 w-3.5 cursor-nwse-resize touch-none rounded-[4px] border shadow-md focus-visible:ring-2 focus-visible:outline-none lg:min-[1024px]:block rtl:cursor-nesw-resize",
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
                      className="bg-primary text-primary-foreground absolute -top-3 left-1/2 z-30 -translate-x-1/2 rounded-full px-2 py-0.5 text-[0.625rem] font-bold whitespace-nowrap tabular-nums shadow-md"
                    >
                      {t("web.admin.dashboard.grid.sizeBadge", {
                        span: span,
                        rows: rows,
                      })}
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
            <SheetTitle>{t("web.admin.dashboard.addSheet.title")}</SheetTitle>
            <SheetDescription>{t("web.admin.dashboard.addSheet.description")}</SheetDescription>
          </SheetHeader>
          <div className="overlay-scroll flex flex-col gap-2 overflow-y-auto p-4 pt-2">
            {hiddenWidgets.length === 0 ? (
              <p className="text-muted-foreground py-4 text-sm">
                {t("web.admin.dashboard.addSheet.empty")}
              </p>
            ) : (
              hiddenWidgets.map((widget) => (
                <button
                  key={widget.id}
                  type="button"
                  className="border-border hover:border-primary/50 hover:bg-accent/40 focus-visible:ring-ring flex cursor-pointer items-start gap-3 rounded-xl border p-3 text-start transition-colors focus-visible:ring-2 focus-visible:outline-none"
                  onClick={() => addWidget(widget.id)}
                  // Dragging is the pointer shortcut for placing the widget at
                  // a specific spot; clicking (the keyboard path) appends.
                  draggable
                  onDragStart={(event) => {
                    if (resizeSessionRef.current) {
                      event.preventDefault();
                      return;
                    }
                    event.dataTransfer.setData(WIDGET_ADD_DRAG_TYPE, widget.id);
                    event.dataTransfer.effectAllowed = "copy";
                    dragSourceRef.current = event.currentTarget;
                    setAddDragId(widget.id);
                  }}
                  // Fires for cancelled and completed drags alike; the drop
                  // handler has already applied any placement by now.
                  onDragEnd={endDrag}
                >
                  <span className="bg-primary/10 text-primary flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-md">
                    <Plus className="h-3.5 w-3.5" />
                  </span>
                  <span className="min-w-0">
                    <span className="block text-sm font-semibold">{t(widget.titleKey)}</span>
                    <span className="text-muted-foreground block text-xs">
                      {t(widget.descriptionKey)}
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
