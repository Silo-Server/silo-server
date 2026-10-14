import { useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type Active,
  type Announcements,
  type CollisionDetection,
  type DragEndEvent,
  type DragStartEvent,
  type Over,
  type UniqueIdentifier,
} from "@dnd-kit/core";
import { SortableContext, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { Ellipsis, GripVertical, Plus } from "lucide-react";

import {
  adminMutationMessage,
  fetchAdminBoardOrderSnapshot,
  fetchAdminGroupCollectionOrderSnapshot,
  fetchAdminGroupOrderSnapshot,
  fetchAdminGroupSnapshot,
} from "@/api/adminCollections";
import type { GroupSortMode, LibraryCollection } from "@/api/types";
import { Button } from "@/components/ui/button";
import {
  useAdminCollectionCapabilities,
  useSetAdminCollectionPin,
} from "@/hooks/queries/admin/collections";
import {
  useCreateCollectionGroup,
  useDeleteCollectionGroup,
  useReorderCollectionGroups,
  useReorderCollectionsInGroup,
  useUpdateCollectionGroup,
} from "@/hooks/queries/admin/collectionGroups";
import { invalidateAdminCollectionQueries } from "@/hooks/queries/collectionSurfaceRefresh";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import {
  ARRANGE_HINT,
  ARRANGE_SUBTITLE,
  MOVE_FAILED,
  ORDER_CHANGED,
  arrangeHeading,
} from "@/lib/collections/copy";
import {
  UNGROUPED,
  acceptsCollections,
  applyCollectionMove,
  applyPin,
  applyShelfMove,
  boardShelves,
  planCollectionMove,
  planShelfMove,
  shelfOf,
  shownCollections,
  sortedBy,
  type BoardGroup,
  type CollectionMove,
  type Shelf,
} from "@/lib/collections/shelves";
import { CollectionListItem } from "./CollectionListItem";
import { GroupCard, type ArrangeDragData, type SortableCardProps } from "./GroupCard";
import { MoveCollectionSheet } from "./MoveCollectionSheet";
import { DeleteShelfDialog, ShelfNameDialog } from "./ShelfDialogs";
import { ArrangeCardMenu, ShelfMenu } from "./ShelfMenus";
import { ViewerPreview } from "./ViewerPreview";

/** Under this width Arrange has no drag: ⋯ opens a sheet that moves the card. */
const NARROW_QUERY = "(max-width: 1023px)";

const SCREEN_READER_INSTRUCTIONS =
  "To move a shelf or a collection, focus its handle and press Space or Enter. Use the arrow keys to move it, Space or Enter to drop it, or Escape to cancel. Every collection's ⋯ also has Move to shelf.";

type OrderReads = Awaited<ReturnType<typeof fetchAdminBoardOrderSnapshot>>;
type OrderRead = Awaited<ReturnType<typeof fetchAdminGroupOrderSnapshot>>;

/** A fresh read found the order changed since the move was planned from it. */
class OrderChanged extends Error {}

function sameIds(actual: readonly string[], expected: readonly string[]) {
  return actual.length === expected.length && actual.every((id, index) => id === expected[index]);
}

/** The order reads describe these shelves exactly, so a move can be checked against them. */
function readsMatch(reads: OrderReads, shelves: readonly Shelf[]) {
  return (
    !reads.groupOrder.has_more &&
    sameIds(
      reads.groupOrder.ordered_ids,
      shelves.map((shelf) => shelf.id),
    ) &&
    shelves.every((shelf) => {
      const order = reads.collectionOrders.get(shelf.id);
      return (
        order !== undefined &&
        !order.has_more &&
        sameIds(
          order.ordered_ids,
          shelf.collections.map((entry) => entry.id),
        )
      );
    })
  );
}

/** The shelf a collection lands on, and the card it lands on (null for the shelf itself). */
function collectionDrop(target: ArrangeDragData): { shelfId: string; overId: string | null } {
  if (target.kind === "collection") return { shelfId: target.shelfId, overId: target.id };
  return { shelfId: target.kind === "shelf" ? target.id : target.shelfId, overId: null };
}

/**
 * A collection drag only meets cards and shelf bodies; a shelf drag only
 * meets shelves. Only a pinned collection meets the cards in a pinned band,
 * so nothing else is shown landing above it.
 */
const collision: CollisionDetection = (args) => {
  const active = args.active.data.current as ArrangeDragData | undefined;
  const shelfDrag = active?.kind === "shelf";
  const pinnedDrag = active?.kind === "collection" && active.pinned;
  return closestCenter({
    ...args,
    droppableContainers: args.droppableContainers.filter((container) => {
      if (String(container.id).startsWith("shelf:") !== shelfDrag) return false;
      const target = container.data.current as ArrangeDragData | undefined;
      return pinnedDrag || target?.kind !== "collection" || !target.banded;
    }),
  });
};

export interface GroupsBoardProps {
  libraryID: number;
  libraryName: string;
  groups: BoardGroup[];
  ungrouped: LibraryCollection[];
  ungroupedSortOrder: number;
  /** The Collections tab state, which may run ahead of the saved visibility. */
  isVisible: (collection: LibraryCollection) => boolean;
  onEditCollection: (collection: LibraryCollection) => void;
  /** The page asks first when rows show a collection being hidden. */
  onVisibleChange: (collection: LibraryCollection, visible: boolean) => void;
}

/**
 * Arrange: one library's shelves top to bottom, the way viewers see them,
 * beside a preview of its Collections tab. Every change saves right away and
 * shows at once; a move that fails goes back and offers Try again. Every
 * move is checked against the order it was planned from: a drag against the
 * order read when the board loaded, a menu move or Try again against a fresh
 * read, so a move never lands on an order someone else changed in the
 * meantime. Rename keeps the shelf's ETag from when its dialog opened. Phones
 * have no drag: a card's ⋯ opens a sheet that moves it.
 */
export function GroupsBoard({
  libraryID,
  libraryName,
  groups,
  ungrouped,
  ungroupedSortOrder,
  isVisible,
  onEditCollection,
  onVisibleChange,
}: GroupsBoardProps) {
  const queryClient = useQueryClient();
  const narrow = useMediaQuery(NARROW_QUERY);
  const { data: capabilities } = useAdminCollectionCapabilities();
  const canEdit = capabilities?.groups === true;

  const saved = useMemo(
    () => boardShelves(groups, ungrouped, ungroupedSortOrder),
    [groups, ungrouped, ungroupedSortOrder],
  );
  // A change shows at once: the board draws `shelves` until the change has
  // saved and the shelves were read again (or it failed and went back).
  const [pending, setPending] = useState<{ base: Shelf[]; shelves: Shelf[] } | null>(null);
  const shelves = pending?.base === saved ? pending.shelves : saved;
  const changing = pending?.base === saved;

  const orderReads = useQuery({
    queryKey: [
      "admin",
      "collections",
      "order-snapshots",
      libraryID,
      saved.map((shelf) => [shelf.id, shelf.collections.map((entry) => entry.id)]),
    ],
    enabled: canEdit,
    queryFn: () =>
      fetchAdminBoardOrderSnapshot(
        libraryID,
        groups.map((group) => group.id),
      ),
  });
  const hasMore =
    orderReads.data !== undefined &&
    (orderReads.data.groupOrder.has_more ||
      [...orderReads.data.collectionOrders.values()].some((order) => order.has_more));
  const dragDisabled =
    !canEdit || narrow || !orderReads.data || orderReads.isError || hasMore || changing;

  const createGroup = useCreateCollectionGroup(libraryID);
  const updateGroup = useUpdateCollectionGroup();
  const deleteGroup = useDeleteCollectionGroup();
  const reorderGroups = useReorderCollectionGroups(libraryID);
  const reorderCollections = useReorderCollectionsInGroup(libraryID);
  const setPin = useSetAdminCollectionPin();

  const [naming, setNaming] = useState<
    { mode: "create" } | { mode: "rename"; shelf: Shelf; name: string; etag: string } | null
  >(null);
  const [deleting, setDeleting] = useState<Shelf | null>(null);
  const [moving, setMoving] = useState<LibraryCollection | null>(null);

  // --- saving ----------------------------------------------------------------

  /** Shows `next` until `save` settles; a failure puts the board back. */
  async function change(next: Shelf[], save: () => Promise<unknown>) {
    setPending({ base: saved, shelves: next });
    try {
      await save();
    } finally {
      setPending(null);
    }
  }

  function moveFailed(error: unknown, retry: () => Promise<unknown>) {
    if (error instanceof OrderChanged) {
      toast.error(ORDER_CHANGED);
      void invalidateAdminCollectionQueries(queryClient);
      return;
    }
    toast.error(MOVE_FAILED, {
      action: {
        label: "Try again",
        onClick: () => void retry().catch((next: unknown) => moveFailed(next, retry)),
      },
    });
  }

  /**
   * Shows `next` and saves an order planned from `base`. A drag brings the
   * ETag its reads matched; otherwise (menus, Try again) a fresh read must
   * still show `base`, or the move stops and says someone else changed it.
   */
  function saveOrder(
    next: Shelf[],
    base: readonly string[],
    read: () => Promise<OrderRead>,
    put: (etag: string) => Promise<unknown>,
    etag?: string,
  ) {
    const send = async (tag?: string) => {
      if (tag === undefined) {
        const fresh = await read();
        if (fresh.has_more || !sameIds(fresh.ordered_ids, base)) throw new OrderChanged();
        tag = fresh.etag;
      }
      return put(tag);
    };
    void change(next, () => send(etag)).catch((error: unknown) => moveFailed(error, () => send()));
  }

  function saveCollectionMove(collectionId: string, plan: CollectionMove, etag?: string) {
    const target = saved.find((shelf) => shelf.id === plan.shelfId);
    saveOrder(
      applyCollectionMove(saved, collectionId, plan),
      target?.collections.map((entry) => entry.id) ?? [],
      () => fetchAdminGroupCollectionOrderSnapshot(plan.shelfId, libraryID),
      (tag) =>
        reorderCollections.mutateAsync({
          groupID: plan.shelfId,
          orderedIDs: plan.orderedIds,
          etag: tag,
          ...(plan.shelfId === UNGROUPED ? { libraryId: libraryID } : {}),
        }),
      etag,
    );
  }

  function saveShelfOrder(orderedIds: string[], etag?: string) {
    saveOrder(
      applyShelfMove(saved, orderedIds),
      saved.map((shelf) => shelf.id),
      () => fetchAdminGroupOrderSnapshot(libraryID),
      (tag) => reorderGroups.mutateAsync({ orderedIDs: orderedIds, etag: tag }),
      etag,
    );
  }

  /** A shelf as the server has it now, with its ETag; a failed read says so. */
  async function readShelf(id: string) {
    try {
      return await fetchAdminGroupSnapshot(id);
    } catch (error) {
      toast.error(adminMutationMessage(error, "Couldn't read the shelf"));
      throw error;
    }
  }

  async function patchShelf(
    id: string,
    patch: { name?: string; default_sort_mode?: GroupSortMode },
    etag?: string,
  ) {
    await updateGroup.mutateAsync({ id, etag: etag ?? (await readShelf(id)).etag, ...patch });
  }

  /** Rename saves against the shelf as the dialog first showed it. */
  function openRename(shelf: Shelf) {
    void readShelf(shelf.id).then(
      ({ group, etag }) => setNaming({ mode: "rename", shelf, name: group.name, etag }),
      () => undefined,
    );
  }

  function changeSort(shelf: Shelf, mode: GroupSortMode) {
    void change(
      shelves.map((entry) => (entry.id === shelf.id ? { ...entry, sortMode: mode } : entry)),
      () => patchShelf(shelf.id, { default_sort_mode: mode }),
    ).catch(() => undefined);
  }

  async function removeShelf(shelf: Shelf) {
    await deleteGroup.mutateAsync({ id: shelf.id, etag: (await readShelf(shelf.id)).etag });
  }

  /** Pin shows at once; the hook says when it fails, and the board goes back. */
  function changePin(collection: LibraryCollection, pinned: boolean) {
    void change(applyPin(shelves, collection.id, pinned), () =>
      setPin.mutateAsync({ id: collection.id, pinned }),
    ).catch(() => undefined);
  }

  function moveToShelf(collection: LibraryCollection, shelfId: string) {
    const plan = planCollectionMove(saved, collection.id, shelfId, null);
    if (plan) saveCollectionMove(collection.id, plan);
  }

  // --- dragging --------------------------------------------------------------

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor),
  );
  const [drag, setDrag] = useState<{ data: ArrangeDragData; reads: OrderReads } | null>(null);
  // dnd-kit reports the item as over itself right after pickup; only a change is announced.
  const lastOver = useRef<UniqueIdentifier | null>(null);

  function onDragStart(event: DragStartEvent) {
    const data = event.active.data.current as ArrangeDragData | undefined;
    const reads = orderReads.data;
    if (!data || !reads) return;
    if (!readsMatch(reads, saved)) {
      toast.error("Collection order is not ready or changed. Reload before reordering.");
      void invalidateAdminCollectionQueries(queryClient);
      void orderReads.refetch();
      return;
    }
    setDrag({ data, reads });
  }

  function onDragEnd({ active, over }: DragEndEvent) {
    const started = drag;
    setDrag(null);
    if (!started || !over || active.id === over.id) return;
    const target = over.data.current as ArrangeDragData | undefined;
    if (!target) return;
    const { data, reads } = started;
    if (data.kind === "shelf") {
      if (target.kind !== "shelf") return;
      const to = saved.findIndex((shelf) => shelf.id === target.id);
      const order = planShelfMove(saved, data.id, to);
      if (order) saveShelfOrder(order, reads.groupOrder.etag);
      return;
    }
    if (data.kind !== "collection") return;
    const { shelfId, overId } = collectionDrop(target);
    const plan = planCollectionMove(saved, data.id, shelfId, overId);
    if (plan) saveCollectionMove(data.id, plan, reads.collectionOrders.get(plan.shelfId)?.etag);
  }

  const announcements = useMemo<Announcements>(() => {
    // Drag ids are "col:<id>", "shelf:<id>" or "body:<id>".
    const bare = (id: UniqueIdentifier) => String(id).replace(/^(col|shelf|body):/, "");
    const shelfById = (id: UniqueIdentifier) => shelves.find((entry) => entry.id === bare(id));
    const collectionAt = (id: UniqueIdentifier) => {
      const shelf = shelfOf(shelves, bare(id));
      if (!shelf) return null;
      const shown = shownCollections(shelf);
      const index = shown.findIndex((entry) => entry.id === bare(id));
      return { title: shown[index]!.title, shelf, index, count: shown.length };
    };
    const name = (id: UniqueIdentifier): string => {
      if (String(id).startsWith("col:")) return collectionAt(id)?.title ?? "Collection";
      const shelf = shelfById(id);
      return shelf ? `shelf ${shelf.name}` : "Shelf";
    };
    /** Where a planned move puts the collection, as viewers will see it. */
    const landing = (id: string, plan: CollectionMove): string => {
      const shelf = applyCollectionMove(shelves, id, plan).find(
        (entry) => entry.id === plan.shelfId,
      );
      if (!shelf) return "";
      const shown = shownCollections(shelf);
      const index = shown.findIndex((entry) => entry.id === id);
      return `position ${index + 1} of ${shown.length} on ${shelf.name}`;
    };
    const place = (id: UniqueIdentifier): string => {
      const text = String(id);
      if (text.startsWith("col:")) {
        const at = collectionAt(id);
        return at ? `position ${at.index + 1} of ${at.count} on ${at.shelf.name}` : "";
      }
      if (text.startsWith("body:")) return `the end of ${shelfById(id)?.name ?? "the shelf"}`;
      const index = shelves.findIndex((entry) => entry.id === bare(id));
      return `position ${index + 1} of ${shelves.length}`;
    };
    const refused = (active: UniqueIdentifier, over: UniqueIdentifier) => {
      if (!String(active).startsWith("col:")) return false;
      const shelf = shelfById(over);
      return shelf !== undefined && !acceptsCollections(shelf);
    };
    /** What a drop did; a collection drop that plans no move says why. */
    const dropped = (active: Active, over: Over | null): string => {
      if (!over) return `${name(active.id)} dropped. Nothing moved.`;
      if (refused(active.id, over.id))
        return `${name(active.id)} can't go on My collections. Nothing moved.`;
      const target = over.data.current as ArrangeDragData | undefined;
      if (String(active.id).startsWith("col:") && target) {
        const { shelfId, overId } = collectionDrop(target);
        const plan = planCollectionMove(shelves, bare(active.id), shelfId, overId);
        if (!plan) {
          const shelf = shelfById(shelfId);
          const by = shelf && sortedBy(shelf.sortMode);
          if (by && shelfOf(shelves, bare(active.id)) === shelf)
            return `${shelf.name} sorts by ${by}, so the order didn't change.`;
          return `${name(active.id)} dropped. Nothing moved.`;
        }
        // The pinned band can put it somewhere other than the card it was dropped on.
        return `${name(active.id)} dropped at ${landing(bare(active.id), plan)}.`;
      }
      return `${name(active.id)} dropped at ${place(over.id)}.`;
    };
    return {
      onDragStart: ({ active }) => {
        lastOver.current = active.id;
        return `Picked up ${name(active.id)}, ${place(active.id)}.`;
      },
      onDragOver: ({ active, over }) => {
        if (!over || over.id === lastOver.current) return undefined;
        lastOver.current = over.id;
        if (refused(active.id, over.id))
          return `${name(active.id)} can't go on My collections. It holds viewers' own collections.`;
        return `${name(active.id)} is over ${place(over.id)}.`;
      },
      onDragEnd: ({ active, over }) => dropped(active, over),
      onDragCancel: ({ active }) => `Moving ${name(active.id)} was canceled.`,
    };
  }, [shelves]);

  // --- rendering -------------------------------------------------------------

  const takers = shelves.filter(acceptsCollections);
  const collectionDrag = drag?.data.kind === "collection";
  const shelfDrag = drag?.data.kind === "shelf";

  function card(collection: LibraryCollection, shelf: Shelf, sortable: SortableCardProps) {
    const visible = isVisible(collection);
    const menu = narrow ? (
      <Button
        type="button"
        variant="ghost"
        size="icon"
        aria-label={`More for ${collection.title}`}
        className="text-muted-foreground size-11 rounded-[10px]"
        onClick={() => setMoving(collection)}
      >
        <Ellipsis className="size-4" />
      </Button>
    ) : (
      <ArrangeCardMenu
        name={collection.title}
        shelves={takers}
        currentShelfId={shelf.id}
        canMove={canEdit && !changing}
        visible={visible}
        pinned={collection.featured}
        canPin={canEdit && !changing}
        inSeveralLibraries={collection.library_ids.length > 1}
        onEdit={() => onEditCollection(collection)}
        onMove={(shelfId) => moveToShelf(collection, shelfId)}
        onPinChange={(pinned) => changePin(collection, pinned)}
        onVisibleChange={(next) => onVisibleChange(collection, next)}
      />
    );
    return (
      <CollectionListItem
        variant="compact"
        collection={collection}
        visible={visible}
        pinned={collection.featured}
        handleProps={sortable.handleProps}
        ref={sortable.ref}
        style={sortable.style}
        dragging={sortable.dragging}
        menu={menu}
        onOpen={() => onEditCollection(collection)}
      />
    );
  }

  // The sheet follows the board, so a Pin made from it shows there at once.
  const movingNow = moving
    ? shelves.flatMap((shelf) => shelf.collections).find((entry) => entry.id === moving.id)
    : undefined;
  const movingShelf = movingNow ? shelfOf(shelves, movingNow.id) : undefined;

  return (
    <div className="grid gap-5 xl:grid-cols-[minmax(0,1fr)_280px] xl:items-start">
      <div className="grid min-w-0 gap-3">
        <div className="flex flex-wrap items-start gap-3">
          <div className="min-w-0 flex-1">
            <h2 className="m-0 text-[16px] font-semibold tracking-[-0.015em]">
              {arrangeHeading(libraryName)}
            </h2>
            <p className="text-muted-foreground m-0 mt-0.5 text-[13px]">{ARRANGE_SUBTITLE}</p>
          </div>
          {canEdit ? (
            <Button variant="outline" size="sm" onClick={() => setNaming({ mode: "create" })}>
              <Plus aria-hidden /> New shelf
            </Button>
          ) : null}
        </div>

        <DndContext
          sensors={sensors}
          collisionDetection={collision}
          accessibility={{
            announcements,
            screenReaderInstructions: { draggable: SCREEN_READER_INSTRUCTIONS },
          }}
          onDragStart={onDragStart}
          onDragEnd={onDragEnd}
          onDragCancel={() => setDrag(null)}
        >
          <SortableContext
            items={shelves.map((shelf) => `shelf:${shelf.id}`)}
            strategy={verticalListSortingStrategy}
          >
            <div className="grid gap-3">
              {shelves.map((shelf, index) => (
                <GroupCard
                  key={shelf.id}
                  shelf={shelf}
                  collapsed={shelfDrag}
                  dragDisabled={dragDisabled}
                  showGrips={!narrow}
                  noDrop={collectionDrag}
                  orderDisabled={!canEdit || changing}
                  onSortChange={(mode) => changeSort(shelf, mode)}
                  menu={
                    shelf.kind === "ungrouped" ? null : (
                      <ShelfMenu
                        shelf={shelf}
                        isFirst={index === 0}
                        isLast={index === shelves.length - 1}
                        disabled={!canEdit || changing}
                        onRename={() => openRename(shelf)}
                        onMove={(to) => {
                          const order = planShelfMove(saved, shelf.id, to === "top" ? 0 : Infinity);
                          if (order) saveShelfOrder(order);
                        }}
                        onDelete={() => setDeleting(shelf)}
                      />
                    )
                  }
                  renderCard={(collection, sortable) => card(collection, shelf, sortable)}
                />
              ))}
            </div>
          </SortableContext>
        </DndContext>

        {narrow ? null : (
          <p className="text-muted-foreground m-0 flex items-start gap-2 px-1 text-[13px] leading-normal">
            <GripVertical aria-hidden className="mt-0.5 size-4 shrink-0" />
            {ARRANGE_HINT}
          </p>
        )}
      </div>

      {narrow ? null : (
        <ViewerPreview
          libraryName={libraryName}
          shelves={shelves}
          isVisible={isVisible}
          className="xl:sticky xl:top-6"
        />
      )}

      {naming ? (
        <ShelfNameDialog
          mode={naming.mode}
          libraryName={libraryName}
          initialName={naming.mode === "rename" ? naming.name : ""}
          onSave={async (name) => {
            if (naming.mode === "create") await createGroup.mutateAsync({ name });
            else await patchShelf(naming.shelf.id, { name }, naming.etag);
          }}
          onClose={() => setNaming(null)}
        />
      ) : null}

      {deleting ? (
        <DeleteShelfDialog
          name={deleting.name}
          collectionCount={deleting.collections.length}
          libraryName={libraryName}
          onConfirm={() => void removeShelf(deleting).catch(() => undefined)}
          onClose={() => setDeleting(null)}
        />
      ) : null}

      {movingNow && movingShelf ? (
        <MoveCollectionSheet
          collection={movingNow}
          libraryName={libraryName}
          shelves={takers}
          currentShelf={movingShelf}
          canMove={canEdit && !changing}
          visible={isVisible(movingNow)}
          canPin={canEdit && !changing}
          onMove={(shelfId) => moveToShelf(movingNow, shelfId)}
          onEdit={() => onEditCollection(movingNow)}
          onPinChange={(pinned) => changePin(movingNow, pinned)}
          onVisibleChange={(next) => onVisibleChange(movingNow, next)}
          onClose={() => setMoving(null)}
        />
      ) : null}
    </div>
  );
}
