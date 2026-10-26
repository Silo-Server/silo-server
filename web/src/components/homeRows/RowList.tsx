import { useMemo, useRef, useState, type ReactNode } from "react";
import {
  DndContext,
  KeyboardSensor,
  MouseSensor,
  TouchSensor,
  closestCenter,
  useSensor,
  useSensors,
  type Announcements,
  type DragEndEvent,
  type DragStartEvent,
  type UniqueIdentifier,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import type { HomeRow } from "@/lib/homeRows/types";
import type { ListRowProps } from "@/components/calm/ListRow";

/** What the list hands each row so it can be dragged by its grip only. */
export type SortableRowProps = Pick<ListRowProps, "handleProps" | "ref" | "style" | "dragging">;

const TOUCH_DRAG_DELAY_MS = 200;

const SCREEN_READER_INSTRUCTIONS =
  "To move a row, focus its handle and press Space or Enter. Use the up and down arrow keys to move it, Space or Enter to drop it, or Escape to cancel.";

function SortableRow({
  id,
  disabled,
  children,
}: {
  id: string;
  disabled: boolean;
  children: (props: SortableRowProps) => ReactNode;
}) {
  const {
    attributes,
    listeners,
    setNodeRef,
    setActivatorNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id, disabled });
  return children({
    ref: setNodeRef,
    style: { transform: CSS.Transform.toString(transform), transition },
    dragging: isDragging,
    handleProps: { ...attributes, ...listeners, ref: setActivatorNodeRef },
  });
}

/**
 * The sortable list. A drag works on the order and version captured when it
 * started, so a refetch while the row is in the air cannot reshuffle it or
 * change what the reorder is checked against.
 */
export function RowList({
  rows,
  canReorder,
  orderToken,
  label,
  onReorder,
  onDragActiveChange,
  children,
}: {
  rows: HomeRow[];
  canReorder: boolean;
  orderToken: unknown;
  label: string;
  onReorder: (orderedIds: string[], orderToken: unknown, movedId: string) => void;
  onDragActiveChange?: (active: boolean) => void;
  children: (row: HomeRow, sortable: SortableRowProps) => ReactNode;
}) {
  const [drag, setDrag] = useState<{ rows: HomeRow[]; token: unknown } | null>(null);
  // dnd-kit reports the row as over itself right after pickup; announcing that
  // would talk over "Picked up". Only a change of position is announced.
  const lastOver = useRef<UniqueIdentifier | null>(null);
  const shownRows = drag?.rows ?? rows;
  const sensors = useSensors(
    useSensor(MouseSensor, { activationConstraint: { distance: 5 } }),
    useSensor(TouchSensor, { activationConstraint: { delay: TOUCH_DRAG_DELAY_MS, tolerance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  const announcements = useMemo<Announcements>(() => {
    const titleOf = (id: UniqueIdentifier) =>
      shownRows.find((row) => row.id === id)?.title ?? "Row";
    const positionOf = (id: UniqueIdentifier) =>
      `position ${shownRows.findIndex((row) => row.id === id) + 1} of ${shownRows.length}`;
    return {
      onDragStart: ({ active }) => {
        lastOver.current = active.id;
        return `Picked up ${titleOf(active.id)}, ${positionOf(active.id)}.`;
      },
      onDragOver: ({ active, over }) => {
        if (!over || over.id === lastOver.current) return undefined;
        lastOver.current = over.id;
        return `${titleOf(active.id)} is now at ${positionOf(over.id)}.`;
      },
      onDragEnd: ({ active, over }) =>
        over
          ? `${titleOf(active.id)} dropped at ${positionOf(over.id)}.`
          : `${titleOf(active.id)} dropped.`,
      onDragCancel: ({ active }) => `Moving ${titleOf(active.id)} was canceled.`,
    };
  }, [shownRows]);

  function setDragState(next: typeof drag) {
    setDrag(next);
    onDragActiveChange?.(next !== null);
  }

  function handleDragStart(_event: DragStartEvent) {
    if (!canReorder) return;
    setDragState({ rows, token: orderToken });
  }

  function handleDragEnd({ active, over }: DragEndEvent) {
    const snapshot = drag;
    setDragState(null);
    if (!snapshot || !over || active.id === over.id) return;
    const ids = snapshot.rows.map((row) => row.id);
    const from = ids.indexOf(String(active.id));
    const to = ids.indexOf(String(over.id));
    if (from === -1 || to === -1) return;
    onReorder(arrayMove(ids, from, to), snapshot.token, String(active.id));
  }

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCenter}
      accessibility={{
        announcements,
        screenReaderInstructions: { draggable: SCREEN_READER_INSTRUCTIONS },
      }}
      onDragStart={handleDragStart}
      onDragEnd={handleDragEnd}
      onDragCancel={() => setDragState(null)}
    >
      <SortableContext
        items={shownRows.map((row) => row.id)}
        strategy={verticalListSortingStrategy}
      >
        <ol aria-label={label} className="m-0 list-none p-0">
          {shownRows.map((row) => (
            <SortableRow key={row.id} id={row.id} disabled={!canReorder && drag === null}>
              {(sortable) => children(row, sortable)}
            </SortableRow>
          ))}
        </ol>
      </SortableContext>
    </DndContext>
  );
}
