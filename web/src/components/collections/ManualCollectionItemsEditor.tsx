import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { GripVertical, Loader2, Plus, Search, Trash2 } from "lucide-react";

import { DndContext } from "@dnd-kit/core";
import {
  SortableContext,
  verticalListSortingStrategy,
  useSortable,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";

import type { BrowseItem, CollectionItem } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useAddItemToCollection,
  useCollectionItems,
  useRemoveCollectionItem,
  useReorderCollectionItems,
} from "@/hooks/queries/collections";
import { createCatalogSearchState, fetchCatalogPage } from "@/hooks/queries/catalog";
import { catalogKeys } from "@/hooks/queries/keys";
import { useSortableList } from "@/hooks/useSortableList";
import { useDebounce } from "@/hooks/useDebounce";

interface ManualCollectionItemsEditorProps {
  collectionId: string;
  readOnly?: boolean;
  /**
   * Whether this collection lives under the admin /admin/collections/* route
   * (library collection) or the user /collections/* route (personal). The
   * add-item mutation needs to know to hit the right endpoint.
   */
  source?: "user" | "library";
}

const SEARCH_LIMIT = 12;
const DEBOUNCE_MS = 250;

function AddItemPanel({
  collectionId,
  source,
  existingIds,
}: {
  collectionId: string;
  source: "user" | "library";
  existingIds: Set<string>;
}) {
  const [query, setQuery] = useState("");
  const debounced = useDebounce(query.trim(), DEBOUNCE_MS);
  const addItem = useAddItemToCollection();

  const searchState = useMemo(
    () => createCatalogSearchState("query", { q: debounced || undefined }),
    [debounced],
  );

  const results = useQuery({
    queryKey: [
      "manualCollectionPicker",
      collectionId,
      catalogKeys.list({
        source: searchState.source,
        q: searchState.q,
        limit: SEARCH_LIMIT,
        offset: 0,
      }),
    ],
    queryFn: ({ signal }) => fetchCatalogPage(searchState, SEARCH_LIMIT, 0, { signal }),
    enabled: debounced.length > 0,
    staleTime: 30 * 1000,
  });

  const items: BrowseItem[] = results.data?.items ?? [];

  return (
    <div className="space-y-2">
      <div className="relative">
        <Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search the catalog to add titles…"
          className="pl-9"
          autoComplete="off"
        />
      </div>
      {debounced.length === 0 ? null : results.isLoading ? (
        <div className="text-muted-foreground flex items-center gap-2 px-3 py-3 text-sm">
          <Loader2 className="h-4 w-4 animate-spin" />
          Searching…
        </div>
      ) : items.length === 0 ? (
        <div className="text-muted-foreground border-border/60 rounded-md border border-dashed px-3 py-3 text-sm">
          No matches.
        </div>
      ) : (
        <ul className="border-border/60 divide-border/60 max-h-72 divide-y overflow-y-auto rounded-md border">
          {items.map((item) => {
            const already = existingIds.has(item.content_id);
            return (
              <li key={item.content_id} className="flex items-center gap-3 px-3 py-2">
                {item.poster_url ? (
                  <img
                    src={item.poster_url}
                    alt=""
                    className="bg-muted h-10 w-10 shrink-0 rounded object-cover"
                  />
                ) : (
                  <div className="bg-muted h-10 w-10 shrink-0 rounded" />
                )}
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium">{item.title}</div>
                  <div className="text-muted-foreground text-xs">
                    {item.year ? `${item.year} · ` : ""}
                    {item.type}
                  </div>
                </div>
                <Button
                  size="sm"
                  variant={already ? "ghost" : "outline"}
                  disabled={already || addItem.isPending}
                  onClick={() =>
                    addItem.mutate({
                      collectionId,
                      mediaItemId: item.content_id,
                      source,
                    })
                  }
                  className="gap-1.5"
                >
                  <Plus className="h-3.5 w-3.5" />
                  {already ? "Added" : "Add"}
                </Button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

export function ManualCollectionItemsEditor({
  collectionId,
  readOnly = false,
  source = "user",
}: ManualCollectionItemsEditorProps) {
  const { data, isLoading } = useCollectionItems(collectionId);
  const items = useMemo(() => data ?? [], [data]);
  const reorderMutation = useReorderCollectionItems(collectionId);
  const removeMutation = useRemoveCollectionItem(collectionId);
  const existingIds = useMemo(() => new Set(items.map((i) => i.media_item_id)), [items]);

  const { sensors, collisionDetection, handleDragEnd } = useSortableList(
    items,
    (item) => item.media_item_id,
    (orderedIds) => reorderMutation.mutate(orderedIds),
  );

  if (isLoading) {
    return (
      <div className="space-y-2">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-12 rounded-lg" />
        ))}
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {!readOnly && (
        <AddItemPanel collectionId={collectionId} source={source} existingIds={existingIds} />
      )}
      {items.length === 0 ? (
        <div className="text-muted-foreground rounded-lg border border-dashed px-4 py-5 text-sm">
          No items yet. Search above to add titles.
        </div>
      ) : (
        <DndContext
          sensors={sensors}
          collisionDetection={collisionDetection}
          onDragEnd={readOnly ? undefined : handleDragEnd}
        >
          <SortableContext
            items={items.map((item) => item.media_item_id)}
            strategy={verticalListSortingStrategy}
          >
            <div className="space-y-2">
              {items.map((item, index) => (
                <SortableItemRow
                  key={item.media_item_id}
                  item={item}
                  index={index}
                  readOnly={readOnly}
                  onRemove={() => removeMutation.mutate(item.media_item_id)}
                />
              ))}
            </div>
          </SortableContext>
        </DndContext>
      )}
    </div>
  );
}

function SortableItemRow({
  item,
  index,
  readOnly,
  onRemove,
}: {
  item: CollectionItem;
  index: number;
  readOnly: boolean;
  onRemove: () => void;
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: item.media_item_id,
    disabled: readOnly,
  });
  const style: React.CSSProperties = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.4 : 1,
  };

  return (
    <div
      ref={setNodeRef}
      style={style}
      className="surface-panel-subtle flex items-center gap-3 rounded-xl px-3 py-2"
    >
      {!readOnly ? (
        <button
          type="button"
          aria-label={`Drag item ${item.media_item_id}`}
          className="hover:bg-surface-hover cursor-grab touch-none rounded-md p-1 transition-colors"
          {...attributes}
          {...listeners}
        >
          <GripVertical className="text-muted-foreground h-4 w-4" />
        </button>
      ) : null}
      <span className="text-muted-foreground w-8 shrink-0 text-right text-xs tabular-nums">
        {index + 1}
      </span>
      <code className="text-muted-foreground min-w-0 flex-1 truncate text-xs">
        {item.media_item_id}
      </code>
      {!readOnly ? (
        <Button
          variant="ghost"
          size="icon"
          className="text-destructive hover:bg-destructive/10 hover:text-destructive h-7 w-7"
          aria-label={`Remove item ${item.media_item_id}`}
          onClick={onRemove}
        >
          <Trash2 className="h-3 w-3" />
        </Button>
      ) : null}
    </div>
  );
}
