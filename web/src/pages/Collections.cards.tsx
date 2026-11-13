import type { KeyboardEventHandler, ReactNode } from "react";
import { GripVertical, Users } from "lucide-react";
import { useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";

import type { Collection, LibraryTabCollection } from "@/api/types";
import { CollectionPosterCard } from "@/components/collections/CollectionPosterCard";
import {
  CollectionMetaLine,
  type SyncAttention,
} from "@/components/collections/editor/CollectionMetaLine";
import { collectionKindLabel, collectionKindOf } from "@/lib/collections/types";
import { useT, type Translate } from "@/i18n";

/** Other profiles' shared collections, read-only, each naming its owner. */
export function SharedCards({
  cards,
}: {
  cards: Array<{ collection: Collection; owner: string }>;
}) {
  return cards.map(({ collection, owner }) => (
    <li key={collection.id}>
      <CollectionPosterCard
        collection={posterOf(collection)}
        kind="user_collections"
        ownerName={owner}
        meta={<CardMeta collection={collection} />}
      />
    </li>
  ));
}

/** A personal collection in the shape the poster card draws. */
function posterOf(collection: Collection): LibraryTabCollection {
  return {
    id: collection.id,
    title: collection.name,
    poster_url: collection.poster_url ?? "",
    poster_thumbhash: collection.poster_thumbhash,
    item_count: collection.item_count ?? 0,
  };
}

/** Failed or syncing lists say so; a healthy sync stays quiet. */
function syncAttention(
  t: Translate,
  collection: Collection,
  syncing: boolean,
): SyncAttention | undefined {
  if (syncing || collection.last_sync_status === "running")
    return { label: t("web.collections.list.syncingNow"), tone: "syncing" };
  if (collection.last_sync_status === "failed")
    return {
      label: t("web.collections.list.syncFailed"),
      tone: "failed",
      message: collection.last_sync_message,
    };
  return undefined;
}

function CardMeta({ collection, syncing = false }: { collection: Collection; syncing?: boolean }) {
  const t = useT();
  return (
    <CollectionMetaLine
      className="text-xs"
      typeLabel={collectionKindLabel(collectionKindOf(collection.collection_type))}
      itemCount={collection.item_count ?? 0}
      attention={syncAttention(t, collection, syncing)}
    />
  );
}

// SortableCollectionCard is one of the profile's own collections, with its ⋯
// menu and, when the store supports it, a drag handle. The pointer can drag
// from anywhere on the card; the keyboard drags from the handle, so Enter on
// the card's link still opens it.
export function SortableCollectionCard({
  collection,
  canReorder,
  syncing,
  menu,
}: {
  collection: Collection;
  canReorder: boolean;
  syncing: boolean;
  menu: ReactNode;
}) {
  const t = useT();
  const {
    attributes,
    listeners,
    setNodeRef,
    setActivatorNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id: collection.id, disabled: !canReorder });
  return (
    <li
      ref={setNodeRef}
      data-collection-id={collection.id}
      onPointerDown={
        canReorder
          ? (event) => {
              // Only a press on the card itself starts a drag: not one in the
              // ⋯ menu, which renders in a portal but still bubbles here
              // through React, and not one on the ⋯ button. A finger or pen
              // drags only from the handle: elsewhere on the card it may be
              // the start of a scroll.
              const target = event.target as Element;
              if (!event.currentTarget.contains(target)) return;
              if (target.closest("button:not([data-drag-handle])")) return;
              if (event.pointerType !== "mouse" && !target.closest("[data-drag-handle]")) return;
              listeners?.onPointerDown?.(event);
            }
          : undefined
      }
      style={{
        transform: CSS.Transform.toString(transform),
        transition,
        opacity: isDragging ? 0.4 : 1,
      }}
    >
      <CollectionPosterCard
        collection={posterOf(collection)}
        kind="user_collections"
        meta={<CardMeta collection={collection} syncing={syncing} />}
        tag={
          collection.is_shared ? (
            <span className="inline-flex items-center gap-1 rounded-full border border-sky-500/40 bg-sky-500/15 px-2 py-0.5 text-[11px] font-semibold text-sky-300">
              <Users aria-hidden className="size-3" />
              {t("web.collections.tags.shared")}
            </span>
          ) : undefined
        }
        menu={menu}
        handle={
          canReorder ? (
            <button
              ref={setActivatorNodeRef}
              type="button"
              aria-label={t("web.collections.page.dragHandle", { name: collection.name })}
              data-drag-handle
              className="flex size-8 cursor-grab touch-none items-center justify-center rounded-[10px] bg-black/55 text-white opacity-0 backdrop-blur-sm transition group-focus-within/card:opacity-100 group-hover/card:opacity-100 focus-visible:opacity-100 [@media(pointer:coarse)]:opacity-100"
              {...attributes}
              onKeyDown={listeners?.onKeyDown as KeyboardEventHandler}
            >
              <GripVertical aria-hidden className="size-4" />
            </button>
          ) : undefined
        }
      />
    </li>
  );
}
