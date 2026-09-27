import { useState } from "react";
import { useDroppable } from "@dnd-kit/core";
import { useSortable, SortableContext, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { Users } from "lucide-react";
import type { GroupSortMode, LibraryCollection, LibraryCollectionGroup } from "@/api/types";
import { CollectionRow } from "./CollectionRow";

export interface GroupCardProps {
  group: LibraryCollectionGroup;
  collections: LibraryCollection[];
  onEdit: (id: string) => void;
  onEditCollection: (collection: LibraryCollection) => void;
  onDeleteCollection: (collection: LibraryCollection) => void;
  onSyncCollection: (collection: LibraryCollection) => void;
  syncingCollectionID?: string | null;
  collapsed?: boolean;
  dragDisabled?: boolean;
}

export function GroupCard({
  group,
  collections,
  onEdit,
  onEditCollection,
  onDeleteCollection,
  onSyncCollection,
  syncingCollectionID = null,
  collapsed = false,
  dragDisabled: boardDragDisabled = false,
}: GroupCardProps) {
  const [viewMode, setViewMode] = useState<GroupSortMode>(group.default_sort_mode);
  const dragDisabled = boardDragDisabled || viewMode !== "manual";
  const isUserCollections = group.kind === "user_collections";

  const sortableId = `group:${group.id}`;
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: sortableId,
    disabled: boardDragDisabled,
    data: { kind: "group", id: group.id },
  });
  const style = {
    transform: CSS.Translate.toString(transform),
    transition,
    opacity: isDragging ? 0.5 : 1,
  };

  const { setNodeRef: setDroppableRef, isOver } = useDroppable({
    id: `group-body:${group.id}`,
    data: { kind: "group-body", groupID: group.id },
  });

  const sorted = applySort(collections, viewMode);
  const sortableIds = sorted.map((c) => `col:${c.id}`);

  return (
    <div ref={setNodeRef} style={style} className="bg-background rounded-lg border">
      <div className="flex items-center gap-2 border-b p-3">
        <button
          {...attributes}
          {...listeners}
          className="text-muted-foreground hover:text-foreground cursor-grab"
          disabled={boardDragDisabled}
          aria-label="Drag group"
          type="button"
        >
          ⋮⋮
        </button>
        <h3 className="flex-1 font-semibold">
          {group.name}
          {isUserCollections && (
            <span className="ml-2 inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-normal">
              <Users className="mr-1 h-3 w-3" />
              User Collections
            </span>
          )}
        </h3>
        <label className="text-muted-foreground text-xs">View:</label>
        <select
          value={viewMode}
          onChange={(e) => setViewMode(e.target.value as GroupSortMode)}
          className="border-input bg-background text-foreground rounded-md border px-2 py-1 text-sm"
          aria-label="View sort"
        >
          <option value="manual">Manual</option>
          <option value="name_asc">Name A–Z</option>
          <option value="name_desc">Name Z–A</option>
          <option value="recent">Recently Updated</option>
          <option value="most_items">Most Items</option>
        </select>
        <button
          onClick={() => onEdit(group.id)}
          className="hover:bg-muted rounded p-1"
          aria-label="Group settings"
          type="button"
        >
          ⋯
        </button>
      </div>

      {!collapsed && (
        <div ref={setDroppableRef} className={`p-3 ${isOver ? "bg-muted/40" : ""}`}>
          {sorted.length === 0 ? (
            <div className="text-muted-foreground rounded border border-dashed p-4 text-center text-sm">
              {isUserCollections
                ? "Reserved slot for user-published collections. Drag this group to set where they'd appear on the library tab."
                : "Drag a collection here, or add one with + New collection."}
            </div>
          ) : (
            <SortableContext items={sortableIds} strategy={verticalListSortingStrategy}>
              <div className="space-y-2">
                {sorted.map((c) => (
                  <CollectionRow
                    key={c.id}
                    collection={c}
                    dragDisabled={dragDisabled}
                    parentGroupID={group.id}
                    parentCollectionIDs={sorted.map((x) => x.id)}
                    isInUserCollectionsGroup={isUserCollections}
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

function applySort(cs: LibraryCollection[], mode: GroupSortMode): LibraryCollection[] {
  const cp = [...cs];
  switch (mode) {
    case "name_asc":
      cp.sort((a, b) => a.title.localeCompare(b.title));
      break;
    case "name_desc":
      cp.sort((a, b) => b.title.localeCompare(a.title));
      break;
    case "recent":
      cp.sort((a, b) => +new Date(b.updated_at) - +new Date(a.updated_at));
      break;
    case "most_items":
      cp.sort((a, b) => (b.item_count ?? 0) - (a.item_count ?? 0));
      break;
    case "manual":
    default:
      // preserve incoming order (already sort_order asc from server)
      break;
  }
  return cp;
}
