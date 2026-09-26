import { useDroppable } from "@dnd-kit/core";
import { useSortable, SortableContext, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import type { LibraryCollection } from "@/api/types";
import { CollectionRow } from "./CollectionRow";

export interface UngroupedSectionProps {
  collections: LibraryCollection[];
  onEditCollection: (collection: LibraryCollection) => void;
  onDeleteCollection: (collection: LibraryCollection) => void;
  onSyncCollection: (collection: LibraryCollection) => void;
  syncingCollectionID?: string | null;
  collapsed?: boolean;
  dragDisabled?: boolean;
}

export function UngroupedSection({
  collections,
  onEditCollection,
  onDeleteCollection,
  onSyncCollection,
  syncingCollectionID = null,
  collapsed = false,
  dragDisabled = false,
}: UngroupedSectionProps) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: "ungrouped",
    disabled: dragDisabled,
    data: { kind: "group", id: "ungrouped" },
  });
  const style = {
    transform: CSS.Translate.toString(transform),
    transition,
    opacity: isDragging ? 0.5 : 1,
  };

  const { setNodeRef: setDroppableRef, isOver } = useDroppable({
    id: `group-body:ungrouped`,
    data: { kind: "group-body", groupID: "ungrouped" },
  });

  const sortableIds = collections.map((c) => `col:${c.id}`);

  return (
    <div ref={setNodeRef} style={style} className="bg-background rounded-lg border border-dashed">
      <div className="flex items-center gap-2 border-b border-dashed p-3">
        <button
          {...attributes}
          {...listeners}
          className="text-muted-foreground hover:text-foreground cursor-grab"
          disabled={dragDisabled}
          aria-label="Drag ungrouped section"
          type="button"
        >
          ⋮⋮
        </button>
        <h4 className="text-muted-foreground flex-1 text-sm font-medium">Ungrouped</h4>
      </div>
      {!collapsed && (
        <div ref={setDroppableRef} className={`p-3 ${isOver ? "bg-muted/40" : ""}`}>
          {collections.length === 0 ? (
            <div className="text-muted-foreground rounded border border-dashed p-4 text-center text-sm">
              Drop collections here to remove them from any group. They'll appear on the library tab
              at this section's position.
            </div>
          ) : (
            <SortableContext items={sortableIds} strategy={verticalListSortingStrategy}>
              <div className="space-y-2">
                {collections.map((c) => (
                  <CollectionRow
                    dragDisabled={dragDisabled}
                    key={c.id}
                    collection={c}
                    parentGroupID="ungrouped"
                    parentCollectionIDs={collections.map((x) => x.id)}
                    isInUserCollectionsGroup={false}
                    onEdit={() => onEditCollection(c)}
                    onDelete={() => onDeleteCollection(c)}
                    onSync={() => onSyncCollection(c)}
                    isSyncing={syncingCollectionID === c.id}
                  />
                ))}
              </div>
            </SortableContext>
          )}
        </div>
      )}
    </div>
  );
}
