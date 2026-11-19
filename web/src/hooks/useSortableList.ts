import { useMemo } from "react";

import {
  KeyboardSensor,
  MouseSensor,
  PointerSensor,
  TouchSensor,
  closestCenter,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import type { DragEndEvent } from "@dnd-kit/core";
import { arrayMove, sortableKeyboardCoordinates, useSortable } from "@dnd-kit/sortable";

import { useMotionScrollBehavior, usePrefersReducedMotion } from "@/hooks/usePrefersReducedMotion";

interface UseSortableListResult {
  sensors: ReturnType<typeof useSensors>;
  collisionDetection: typeof closestCenter;
  handleDragEnd: (event: DragEndEvent) => void;
}

/**
 * Wires the @dnd-kit sensors and a drag-end handler that translates an
 * `over`/`active` event into the new ordered id list, then hands it to
 * `onReorder`. Callers render with whatever data source they have (typically
 * the TanStack Query cache, optimistically updated by the mutation) — no
 * local sortable state lives in the component itself, which avoids races
 * with background refetches.
 */
export function useSortableList<T>(
  items: T[],
  getId: (item: T) => string,
  onReorder: (orderedIds: string[]) => void,
): UseSortableListResult {
  const scrollBehavior = useMotionScrollBehavior();
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
      scrollBehavior,
    }),
  );

  const handleDragEnd = useMemo(
    () =>
      function handleDragEnd(event: DragEndEvent) {
        const { active, over } = event;
        if (!over || active.id === over.id) return;
        const oldIndex = items.findIndex((item) => getId(item) === active.id);
        const newIndex = items.findIndex((item) => getId(item) === over.id);
        if (oldIndex === -1 || newIndex === -1) return;
        const next = arrayMove(items, oldIndex, newIndex);
        onReorder(next.map(getId));
      },
    [items, getId, onReorder],
  );

  return { sensors, collisionDetection: closestCenter, handleDragEnd };
}

/** How long a touch rests on a grip before it starts a drag, so a list can still scroll. */
const TOUCH_DRAG_DELAY_MS = 200;

/**
 * The sensors of a list dragged by its grips: a mouse after a short move, a
 * touch after a short rest (so the page still scrolls), and the keyboard.
 */
export function useGripDragSensors(): ReturnType<typeof useSensors> {
  const scrollBehavior = useMotionScrollBehavior();
  return useSensors(
    useSensor(MouseSensor, { activationConstraint: { distance: 5 } }),
    useSensor(TouchSensor, { activationConstraint: { delay: TOUCH_DRAG_DELAY_MS, tolerance: 5 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
      scrollBehavior,
    }),
  );
}

/**
 * `useSortable` that respects `prefers-reduced-motion`: under reduced motion
 * the neighbouring items jump to their new slots instead of sliding there,
 * and a dropped item lands without settling into place. Use it in place of
 * `useSortable` for every sortable item.
 */
export function useMotionSafeSortable(args: Parameters<typeof useSortable>[0]) {
  const reduceMotion = usePrefersReducedMotion();
  return useSortable(reduceMotion ? { ...args, transition: null } : args);
}

/**
 * Motion props for a `<DragOverlay>`: dnd-kit's defaults normally. Under
 * reduced motion, no glide back to the drop slot and no keyboard-move
 * transition.
 */
export function useMotionSafeOverlay(): { dropAnimation?: null; transition?: string } {
  return usePrefersReducedMotion() ? { dropAnimation: null, transition: "none" } : {};
}
