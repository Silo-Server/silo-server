import { useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { GripVertical, Pencil, RefreshCw, Trash2 } from "lucide-react";
import type { LibraryCollection } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { useSelection, type SelectionKind } from "./GroupsBoard";
import { BulkSelectionCheckbox } from "@/components/BulkSelectionCheckbox";

export interface CollectionRowProps {
  collection: LibraryCollection;
  dragDisabled?: boolean;
  parentGroupID: string;
  parentCollectionIDs: string[];
  isInUserCollectionsGroup?: boolean;
  onEdit: () => void;
  onDelete: () => void;
  onSync: () => void;
  isSyncing?: boolean;
}

export function CollectionRow({
  collection,
  dragDisabled,
  parentGroupID,
  parentCollectionIDs,
  isInUserCollectionsGroup = false,
  onEdit,
  onDelete,
  onSync,
  isSyncing = false,
}: CollectionRowProps) {
  const sortableId = `col:${collection.id}`;
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: sortableId,
    disabled: dragDisabled,
    data: { kind: "collection", id: collection.id },
  });
  const style = {
    transform: CSS.Translate.toString(transform),
    transition,
    opacity: isDragging ? 0.4 : 1,
  };

  const { isSelected, selectOnly, toggleOne, selectRange } = useSelection();
  const selected = isSelected(collection.id);
  const kind: SelectionKind = isInUserCollectionsGroup ? "user_collection" : "collection";
  const syncable = collection.collection_type !== "manual";

  function onRowClick(e: React.MouseEvent<HTMLDivElement>) {
    // Ignore clicks on interactive children (Edit link, drag handle button)
    if ((e.target as HTMLElement).closest("a, button")) return;

    if (e.shiftKey) {
      selectRange(collection.id, kind, parentGroupID, parentCollectionIDs);
    } else if (e.metaKey || e.ctrlKey) {
      toggleOne(collection.id, kind, parentGroupID);
    } else {
      selectOnly(collection.id, kind, parentGroupID);
    }
  }

  function onSelectionChange(checked: boolean, extendRange: boolean) {
    if (extendRange) {
      selectRange(collection.id, kind, parentGroupID, parentCollectionIDs, checked);
    } else {
      toggleOne(collection.id, kind, parentGroupID);
    }
  }

  return (
    <div
      ref={setNodeRef}
      style={style}
      onClick={onRowClick}
      className={cn(
        "bg-card flex cursor-pointer items-center gap-3 rounded border p-2 select-none",
        selected && "bg-primary/10 border-l-primary border-l-2",
      )}
    >
      <BulkSelectionCheckbox
        label={`Select ${collection.title}`}
        selected={selected}
        onSelectionChange={onSelectionChange}
      />
      <button
        type="button"
        {...attributes}
        {...listeners}
        onClick={(e) => e.stopPropagation()}
        className="text-muted-foreground hover:text-foreground cursor-grab disabled:cursor-not-allowed disabled:opacity-40"
        aria-label="Drag to reorder"
        disabled={dragDisabled}
      >
        <GripVertical className="h-4 w-4" />
      </button>
      {collection.poster_url && (
        <img src={collection.poster_url} alt="" className="h-10 w-7 rounded object-cover" />
      )}
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <div className="truncate font-medium">{collection.title}</div>
          {collection.featured ? <Badge variant="secondary">Featured</Badge> : null}
        </div>
        <div className="text-muted-foreground flex flex-wrap items-center gap-2 text-xs">
          <span>{collection.item_count} items</span>
          <Badge variant="outline" className="px-1.5 py-0 text-[10px]">
            {collection.collection_type}
          </Badge>
          {collection.last_sync_status && (
            <Badge variant="outline" className="px-1.5 py-0 text-[10px]">
              {collection.last_sync_status}
            </Badge>
          )}
        </div>
      </div>
      <div className="flex items-center gap-1">
        {syncable ? (
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            aria-label="Sync collection"
            disabled={isSyncing}
            onClick={(e) => {
              e.stopPropagation();
              onSync();
            }}
          >
            <RefreshCw className={cn("h-3.5 w-3.5", isSyncing && "animate-spin")} />
          </Button>
        ) : null}
        <Button
          variant="ghost"
          size="icon"
          className="h-8 w-8"
          aria-label="Edit collection"
          onClick={(e) => {
            e.stopPropagation();
            onEdit();
          }}
        >
          <Pencil className="h-3.5 w-3.5" />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          className="text-destructive hover:bg-destructive/10 hover:text-destructive h-8 w-8"
          aria-label="Delete collection"
          onClick={(e) => {
            e.stopPropagation();
            onDelete();
          }}
        >
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      </div>
    </div>
  );
}
