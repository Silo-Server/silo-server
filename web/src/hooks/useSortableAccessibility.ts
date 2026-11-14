import type { DndContextProps, UniqueIdentifier } from "@dnd-kit/core";

import { useT } from "@/i18n";

/** One entry of a reorderable list, as a screen reader names it. */
export interface SortableAnnouncementItem {
  id: UniqueIdentifier;
  /** What the list shows for the entry (a name, not an ID). */
  name: string;
}

/**
 * dnd-kit's screen-reader instructions and live announcements for a plain
 * reorderable list, in the interface language. dnd-kit's own are English and
 * name an item by its ID. Pass `items` in their current order; spread the
 * result into `<DndContext accessibility={…}>`. A list with its own wording
 * (shelves, rows) builds its own announcements instead.
 */
export function useSortableAccessibility(
  items: readonly SortableAnnouncementItem[],
): NonNullable<DndContextProps["accessibility"]> {
  const t = useT();
  const nameOf = (id: UniqueIdentifier) =>
    items.find((item) => item.id === id)?.name ?? t("web.common.sortable.fallbackName");
  const at = (id: UniqueIdentifier) => ({
    position: items.findIndex((item) => item.id === id) + 1,
    total: items.length,
  });
  return {
    screenReaderInstructions: { draggable: t("web.common.sortable.instructions") },
    announcements: {
      onDragStart: ({ active }) =>
        t("web.common.sortable.pickedUp", { name: nameOf(active.id), ...at(active.id) }),
      onDragOver: ({ active, over }) =>
        over
          ? t("web.common.sortable.over", { name: nameOf(active.id), ...at(over.id) })
          : undefined,
      onDragEnd: ({ active, over }) =>
        over
          ? t("web.common.sortable.droppedAt", { name: nameOf(active.id), ...at(over.id) })
          : t("web.common.sortable.dropped", { name: nameOf(active.id) }),
      onDragCancel: ({ active }) => t("web.common.sortable.canceled", { name: nameOf(active.id) }),
    },
  };
}
