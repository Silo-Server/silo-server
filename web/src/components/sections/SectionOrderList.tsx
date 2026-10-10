import { useState } from "react";
import {
  DndContext,
  DragOverlay,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
  type DragStartEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";

import type { SettingsSectionEntry } from "@/api/types";
import {
  SectionDragOverlay,
  SortableSectionCardRow,
  type EditableSectionViewModel,
} from "@/components/sections/EditableSectionRows";
import type { RecipeCatalogResponse } from "@/lib/recipes";

function toEditableSection(section: SettingsSectionEntry): EditableSectionViewModel {
  return {
    id: section.id,
    title: section.title,
    sectionType: section.section_type,
    itemLimit: section.item_limit,
    featured: section.featured,
    hidden: section.hidden,
    isCustom: section.is_custom,
    config: section.config,
  };
}

/**
 * A profile's page sections as a draggable list: drag to reorder, the eye to
 * show or hide. Edit and delete appear only when their handlers are given.
 * The list is controlled; `onMove` receives the reordered list and the moved
 * section's id.
 */
export function SectionOrderList({
  sections,
  catalog,
  disabled = false,
  onMove,
  onToggleHidden,
  onEdit,
  onDelete,
}: {
  sections: SettingsSectionEntry[];
  catalog?: RecipeCatalogResponse;
  disabled?: boolean;
  onMove: (next: SettingsSectionEntry[], movedId: string) => void;
  onToggleHidden: (section: SettingsSectionEntry) => void;
  onEdit?: (section: SettingsSectionEntry) => void;
  onDelete?: (section: SettingsSectionEntry) => void;
}) {
  const [activeId, setActiveId] = useState<string | null>(null);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  function handleDragStart(event: DragStartEvent) {
    if (disabled) return;
    setActiveId(String(event.active.id));
  }

  function handleDragEnd(event: DragEndEvent) {
    setActiveId(null);
    if (disabled) return;
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    const oldIndex = sections.findIndex((s) => s.id === active.id);
    const newIndex = sections.findIndex((s) => s.id === over.id);
    if (oldIndex === -1 || newIndex === -1) return;
    onMove(arrayMove(sections, oldIndex, newIndex), String(active.id));
  }

  const activeSection = activeId ? (sections.find((s) => s.id === activeId) ?? null) : null;

  return (
    <DndContext
      sensors={disabled ? [] : sensors}
      collisionDetection={closestCenter}
      onDragStart={handleDragStart}
      onDragEnd={handleDragEnd}
      onDragCancel={() => setActiveId(null)}
    >
      <SortableContext items={sections.map((s) => s.id)} strategy={verticalListSortingStrategy}>
        <div className="space-y-2">
          {sections.map((section) => (
            <SortableSectionCardRow
              key={section.id}
              section={toEditableSection(section)}
              catalog={catalog}
              onToggleHidden={() => onToggleHidden(section)}
              onEdit={onEdit ? () => onEdit(section) : undefined}
              onDelete={onDelete ? () => onDelete(section) : undefined}
              disabled={disabled}
            />
          ))}
          {sections.length === 0 && (
            <div className="surface-panel-subtle text-muted-foreground rounded-[1.2rem] py-8 text-center text-sm">
              No sections configured.
            </div>
          )}
        </div>
      </SortableContext>
      <DragOverlay>
        {activeSection ? (
          <SectionDragOverlay section={toEditableSection(activeSection)} catalog={catalog} />
        ) : null}
      </DragOverlay>
    </DndContext>
  );
}
