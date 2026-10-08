import {
  fetchCollectionEditSnapshot,
  fetchCollectionOrderSnapshot,
  type CollectionEditSnapshot,
} from "@/api/personalCollections";
import { toast } from "sonner";
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEventHandler,
  type ReactNode,
} from "react";
import { Link, useNavigate } from "react-router";
import { GripVertical, Plus, Users } from "lucide-react";
import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
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
  rectSortingStrategy,
  sortableKeyboardCoordinates,
  useSortable,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";

import type { Collection, LibraryTabCollection } from "@/api/types";
import { CalmPage } from "@/components/calm/CalmPage";
import { PillSwitcher } from "@/components/calm/PillSwitcher";
import { CollectionActionsMenu } from "@/components/collections/CollectionActionsMenu";
import { NewCollectionPicker } from "@/components/collections/NewCollectionPicker";
import { CollectionPosterCard } from "@/components/collections/CollectionPosterCard";
import {
  CollectionMetaLine,
  type SyncAttention,
} from "@/components/collections/editor/CollectionMetaLine";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useCollectionCapabilities,
  useCollections,
  useDeleteCollection,
  useReorderCollections,
  useServerCollections,
  useSetCollectionShared,
} from "@/hooks/queries/collections";
import { useProfiles } from "@/hooks/queries/profiles";
import { useSyncUserCollection } from "@/hooks/queries/userCollectionImports";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useDialogSearchParam } from "@/hooks/useDialogSearchParam";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { personalDeleteDescription, unshareConsequence } from "@/lib/collections/copy";
import { NEW_COLLECTION_DIALOG } from "@/lib/collections/dialogs";
import { partitionPersonalCollections } from "@/lib/collections/personalOwnership";
import { addToMyHomePath } from "@/lib/collections/rows";
import { PERSONAL_SCOPE } from "@/lib/collections/scope";
import { COLLECTION_KIND_LABEL, collectionKindOf } from "@/lib/collections/types";
import { cn } from "@/lib/utils";

/** Seven posters a row on desktop, five on medium widths, three on phones. */
const POSTER_GRID = "grid grid-cols-3 gap-x-3.5 gap-y-5 sm:grid-cols-5 lg:grid-cols-7";
/** One POSTER_GRID column, for cards laid out in a flex row inside an `@container`. */
const GRID_COLUMN_WIDTH =
  "w-[calc((100cqw-2*0.875rem)/3)] sm:w-[calc((100cqw-4*0.875rem)/5)] lg:w-[calc((100cqw-6*0.875rem)/7)]";
/** Under this width New collection moves to a bar docked at the bottom. */
const NARROW_QUERY = "(max-width: 1023px)";

export default function Collections() {
  useDocumentTitle("Collections");
  const { data, isLoading } = useCollections();
  const { data: capabilities } = useCollectionCapabilities();
  const { data: profiles = [] } = useProfiles();
  const { profile } = useCurrentProfile();
  const { own, shared } = useMemo(
    () => partitionPersonalCollections(data ?? [], profile?.id, profiles),
    [data, profile?.id, profiles],
  );
  const otherProfileNames = profiles
    .filter((entry) => entry.id !== profile?.id)
    .map((entry) => entry.name);
  // A one-profile account has nobody to share with: no sharing switch, no Shared with me.
  const multiProfile = otherProfileNames.length > 0;
  const narrow = useMediaQuery(NARROW_QUERY);
  const [pickerOpen, setPickerOpen] = useDialogSearchParam(NEW_COLLECTION_DIALOG);

  const newCollection = (
    <Button
      type="button"
      size="sm"
      className={cn(narrow && "h-11 w-full")}
      onClick={() => setPickerOpen(true)}
    >
      <Plus aria-hidden /> New collection
    </Button>
  );

  return (
    <div className="page-shell py-4 sm:py-6">
      <CalmPage
        heading="page"
        title="Collections"
        subtitle={
          multiProfile
            ? "Yours, the ones other profiles share with you, and the server's."
            : "Yours and the server's."
        }
        actions={narrow ? null : newCollection}
        padBottom={narrow}
      >
        {isLoading ? (
          <PosterGridSkeleton />
        ) : (
          <YourCollections
            collections={own}
            canReorder={capabilities?.item_reorder === true}
            canSync={capabilities?.imports === true}
            otherProfileNames={otherProfileNames}
          />
        )}
        {multiProfile && shared.length > 0 ? (
          <CollectionsSection
            id="shared-with-me"
            title="Shared with me"
            count={shared.reduce((total, group) => total + group.collections.length, 0)}
            note="Read-only. Other profiles on this account made these."
          >
            {/* Owners sit side by side, each card one grid column wide. */}
            <div className="@container">
              <div className="flex flex-wrap gap-x-3.5 gap-y-5">
                {shared.map((group) => (
                  <div key={group.owner.id} className="grid max-w-full min-w-0 content-start gap-3">
                    <div className="text-muted-foreground flex items-center gap-2 text-sm">
                      <OwnerInitial name={group.owner.name} />
                      <h3>
                        by <span className="text-foreground font-semibold">{group.owner.name}</span>
                      </h3>
                    </div>
                    <ul className="flex flex-wrap gap-x-3.5 gap-y-5">
                      {group.collections.map((collection) => (
                        <li key={collection.id} className={GRID_COLUMN_WIDTH}>
                          <CollectionPosterCard
                            collection={posterOf(collection)}
                            kind="user_collections"
                            meta={<CardMeta collection={collection} />}
                          />
                        </li>
                      ))}
                    </ul>
                  </div>
                ))}
              </div>
            </div>
          </CollectionsSection>
        ) : null}
        <ServerCollectionsSection />
      </CalmPage>
      {narrow ? (
        // Docked above whichever background playback bar shows, as SaveBar is.
        <div
          role="region"
          aria-label="Page actions"
          className="from-background/0 to-background fixed inset-x-0 bottom-(--playback-bar-clearance,0px) z-30 bg-gradient-to-b to-30% px-4 pt-[22px] pb-[max(1.25rem,env(safe-area-inset-bottom))]"
        >
          {newCollection}
        </div>
      ) : null}
      {pickerOpen ? (
        <NewCollectionPicker scope="personal" onClose={() => setPickerOpen(false)} />
      ) : null}
    </div>
  );
}

/** One section of the page: a heading with its count, a note at the right, and its cards. */
function CollectionsSection({
  id,
  title,
  count,
  note,
  action,
  children,
}: {
  id: string;
  title: string;
  count?: number;
  note?: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section aria-labelledby={id} className="grid gap-4">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <div className="flex items-baseline gap-2">
          <h2 id={id} className="text-xl font-semibold tracking-tight sm:text-2xl">
            {title}
          </h2>
          {count !== undefined ? (
            <span className="text-muted-foreground text-sm">{count}</span>
          ) : null}
        </div>
        {note ? <p className="text-muted-foreground text-[13px]">{note}</p> : null}
        {action}
      </div>
      {children}
    </section>
  );
}

function OwnerInitial({ name }: { name: string }) {
  return (
    <span
      aria-hidden
      className="bg-primary/20 text-primary inline-flex size-5 items-center justify-center rounded-full text-[11px] font-semibold"
    >
      {name.charAt(0).toUpperCase()}
    </span>
  );
}

function PosterGridSkeleton() {
  return (
    <div className={POSTER_GRID} aria-hidden>
      {Array.from({ length: 7 }, (_, index) => (
        <div key={index}>
          <Skeleton className="aspect-[2/3] rounded-xl" />
          <Skeleton className="mt-2.5 h-4 w-3/4" />
        </div>
      ))}
    </div>
  );
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
function syncAttention(collection: Collection, syncing: boolean): SyncAttention | undefined {
  if (syncing || collection.last_sync_status === "running")
    return { label: "Syncing now", tone: "syncing" };
  if (collection.last_sync_status === "failed")
    return { label: "Sync failed", tone: "failed", message: collection.last_sync_message };
  return undefined;
}

function CardMeta({ collection, syncing = false }: { collection: Collection; syncing?: boolean }) {
  return (
    <CollectionMetaLine
      className="text-xs"
      typeLabel={COLLECTION_KIND_LABEL[collectionKindOf(collection.collection_type)]}
      itemCount={collection.item_count ?? 0}
      attention={syncAttention(collection, syncing)}
    />
  );
}

/**
 * The profile's own collections, in its order: drag a poster, or focus its
 * handle and press Space, then the arrow keys. Each card has a ⋯ menu.
 */
function YourCollections({
  collections,
  canReorder,
  canSync,
  otherProfileNames,
}: {
  collections: Collection[];
  canReorder: boolean;
  canSync: boolean;
  otherProfileNames: string[];
}) {
  const navigate = useNavigate();
  const remove = useDeleteCollection();
  const sync = useSyncUserCollection();
  const share = useSetCollectionShared();
  const reorderMutation = useReorderCollections();
  // A closing dialog stays on screen while it animates out, so it keeps the
  // collection it was about until the next one opens; only `open` resets.
  const [confirmDelete, setConfirmDelete] = useState<{
    open: boolean;
    snapshot?: CollectionEditSnapshot;
  }>({ open: false });
  const [confirmUnshare, setConfirmUnshare] = useState<{ open: boolean; collection?: Collection }>({
    open: false,
  });
  const dragSnapshot = useRef<Promise<string>>(undefined);
  const ids = collections.map((collection) => collection.id);
  // The confirm dialogs open from a menu item that is gone once they close,
  // so focus goes back to the ⋯ of the collection they were about.
  const menuTriggers = useRef(new Map<string, HTMLButtonElement>());
  const confirmFor = useRef<string>(undefined);
  function focusMenuTrigger(event: Event) {
    const trigger = menuTriggers.current.get(confirmFor.current ?? "");
    if (!trigger?.isConnected) return;
    event.preventDefault();
    trigger.focus();
  }

  // A drag reads the server's order validator as it starts, and refuses to
  // reorder when the server's own-collection order differs from the page.
  function beginDrag() {
    dragSnapshot.current = fetchCollectionOrderSnapshot().then((order) => {
      const same =
        order.ordered_ids.length === ids.length &&
        order.ordered_ids.every((id, index) => id === ids[index]);
      if (!same) throw new Error("Collection order changed. Reload before moving collections.");
      return order.etag;
    });
    // A cancelled drag may never consume its snapshot.
    void dragSnapshot.current.catch(() => undefined);
  }
  function reorder(orderedIds: string[]) {
    if (!dragSnapshot.current) return;
    void dragSnapshot.current
      .then((etag) => reorderMutation.mutate({ orderedIds, etag }))
      .catch((error) => toast.error(error.message));
  }
  function setShared(collection: Collection, shared: boolean) {
    // Turning sharing off takes it away from other profiles: ask first.
    if (shared) share.mutate({ id: collection.id, shared });
    else {
      confirmFor.current = collection.id;
      setConfirmUnshare({ open: true, collection });
    }
  }

  return (
    <CollectionsSection
      id="your-collections"
      title="Your collections"
      count={collections.length}
      note={collections.length > 0 ? "Only you can change these" : undefined}
    >
      <ConfirmDialog
        open={confirmDelete.open}
        onOpenChange={(open) => {
          if (!open) setConfirmDelete((current) => ({ ...current, open: false }));
        }}
        title={`Delete "${confirmDelete.snapshot?.collection.name ?? ""}"?`}
        description={personalDeleteDescription(
          confirmDelete.snapshot?.collection.is_shared ?? false,
        )}
        confirmLabel="Delete"
        variant="destructive"
        onCloseAutoFocus={focusMenuTrigger}
        onConfirm={() => {
          const { snapshot } = confirmDelete;
          if (snapshot) remove.mutate({ id: snapshot.collection.id, etag: snapshot.etag });
          setConfirmDelete({ open: false, snapshot });
        }}
      />
      <ConfirmDialog
        open={confirmUnshare.open}
        onOpenChange={(open) => {
          if (!open) setConfirmUnshare((current) => ({ ...current, open: false }));
        }}
        title={`Stop sharing ${confirmUnshare.collection?.name ?? ""}?`}
        description={unshareConsequence(otherProfileNames)}
        confirmLabel="Stop sharing"
        onCloseAutoFocus={focusMenuTrigger}
        onConfirm={() => {
          const { collection } = confirmUnshare;
          if (collection) share.mutate({ id: collection.id, shared: false });
          setConfirmUnshare({ open: false, collection });
        }}
      />
      {collections.length === 0 ? (
        <ul className={POSTER_GRID}>
          <li>
            <NewCollectionCard />
          </li>
        </ul>
      ) : (
        <>
          <SortableCollectionGrid
            collections={collections}
            disabled={!canReorder}
            onBeginDrag={beginDrag}
            onReorder={reorder}
          >
            {collections.map((collection) => {
              const syncing = sync.isPending && sync.variables === collection.id;
              const synced = collectionKindOf(collection.collection_type) === "synced";
              return (
                <SortableCollectionCard
                  key={collection.id}
                  collection={collection}
                  canReorder={canReorder}
                  syncing={syncing}
                  menu={
                    <CollectionActionsMenu
                      name={collection.name}
                      triggerRef={(node) => {
                        if (node) menuTriggers.current.set(collection.id, node);
                        else menuTriggers.current.delete(collection.id);
                      }}
                      onEdit={() => navigate(PERSONAL_SCOPE.paths.edit(collection.id))}
                      sync={
                        canSync && synced
                          ? { syncing, onSync: () => sync.mutate(collection.id) }
                          : undefined
                      }
                      addRow={{
                        mine: true,
                        libraries: [],
                        onAdd: (page) =>
                          navigate(addToMyHomePath({ source: "user", id: collection.id }, page)),
                      }}
                      share={
                        otherProfileNames.length > 0
                          ? {
                              shared: collection.is_shared,
                              disabled: share.isPending,
                              onChange: (shared) => setShared(collection, shared),
                            }
                          : undefined
                      }
                      onDelete={() => {
                        confirmFor.current = collection.id;
                        void fetchCollectionEditSnapshot(collection.id)
                          .then((snapshot) => setConfirmDelete({ open: true, snapshot }))
                          .catch((error) => toast.error(error.message));
                      }}
                    />
                  }
                />
              );
            })}
          </SortableCollectionGrid>
          {canReorder && collections.length > 1 ? (
            <p className="text-muted-foreground flex items-center gap-2 text-[13px]">
              <GripVertical aria-hidden className="size-3.5" />
              Drag a poster to change the order, or focus its handle and press Space, then the arrow
              keys.
            </p>
          ) : null}
        </>
      )}
    </CollectionsSection>
  );
}

/** The empty Your collections: one dashed card, because nobody sees anything here yet. */
function NewCollectionCard() {
  const [, setPickerOpen] = useDialogSearchParam(NEW_COLLECTION_DIALOG);
  return (
    <button
      type="button"
      onClick={() => setPickerOpen(true)}
      className="border-border text-muted-foreground hover:text-foreground hover:border-foreground/40 focus-visible:ring-ring/50 flex aspect-[2/3] flex-col items-center justify-center gap-2 rounded-xl border border-dashed text-center text-[13px] font-medium transition-colors outline-none focus-visible:ring-[3px]"
    >
      <Plus aria-hidden className="size-5" />
      New collection
    </button>
  );
}

/** Live-region words for a keyboard or pointer move, by name and position. */
function moveAnnouncements(collections: Collection[]): Announcements {
  const name = (id: UniqueIdentifier) =>
    collections.find((collection) => collection.id === id)?.name ?? "The collection";
  const position = (id: UniqueIdentifier) =>
    `position ${collections.findIndex((collection) => collection.id === id) + 1} of ${collections.length}`;
  return {
    onDragStart: ({ active }) => `Picked up ${name(active.id)}, at ${position(active.id)}.`,
    onDragOver: ({ active, over }) =>
      over
        ? `${name(active.id)} is over ${position(over.id)}.`
        : `${name(active.id)} is not over a position.`,
    onDragEnd: ({ active, over }) =>
      over
        ? `Moved ${name(active.id)} to ${position(over.id)}.`
        : `${name(active.id)} stayed at ${position(active.id)}.`,
    onDragCancel: ({ active }) => `Cancelled. ${name(active.id)} stayed at ${position(active.id)}.`,
  };
}

// SortableCollectionGrid is the profile's own collections as one flat,
// drag-sortable grid. It reports the new full order of ids.
function SortableCollectionGrid({
  collections,
  disabled,
  onBeginDrag,
  onReorder,
  children,
}: {
  collections: Collection[];
  disabled: boolean;
  onBeginDrag: () => void;
  onReorder: (orderedIds: string[]) => void;
  children: ReactNode;
}) {
  const ids = collections.map((collection) => collection.id);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  const blockClicks = useClickBlockAfterPointerDrag();
  function handleDragStart(event: DragStartEvent) {
    blockClicks.start(event);
    onBeginDrag();
  }
  function handleDragEnd({ active, over }: DragEndEvent) {
    blockClicks.settle();
    if (!over || active.id === over.id) return;
    const from = ids.indexOf(String(active.id));
    const to = ids.indexOf(String(over.id));
    if (from < 0 || to < 0) return;
    onReorder(arrayMove(ids, from, to));
  }
  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCenter}
      accessibility={{ announcements: moveAnnouncements(collections) }}
      onDragStart={handleDragStart}
      onDragEnd={handleDragEnd}
      onDragCancel={blockClicks.settle}
    >
      <SortableContext items={ids} strategy={rectSortingStrategy} disabled={disabled}>
        <ul className={POSTER_GRID}>{children}</ul>
      </SortableContext>
    </DndContext>
  );
}

// useClickBlockAfterPointerDrag keeps the click that ends a pointer drag from
// opening the card under the pointer. dnd-kit stops that click's propagation
// at the document, so React Router never sees it and the browser would follow
// the link's href; a window listener runs first and cancels it. It stays for
// 50ms after the drop, as dnd-kit's own guard does.
function useClickBlockAfterPointerDrag() {
  const release = useRef<() => void>(undefined);
  useEffect(() => () => release.current?.(), []);
  return {
    start({ activatorEvent }: DragStartEvent) {
      if (release.current || !activatorEvent?.type.startsWith("pointer")) return;
      const block = (event: MouseEvent) => event.preventDefault();
      window.addEventListener("click", block, true);
      release.current = () => window.removeEventListener("click", block, true);
    },
    settle() {
      const done = release.current;
      release.current = undefined;
      if (done) setTimeout(done, 50);
    },
  };
}

// SortableCollectionCard is one of the profile's own collections, with its ⋯
// menu and, when the store supports it, a drag handle. The pointer can drag
// from anywhere on the card; the keyboard drags from the handle, so Enter on
// the card's link still opens it.
function SortableCollectionCard({
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
              Shared
            </span>
          ) : undefined
        }
        menu={menu}
        handle={
          canReorder ? (
            <button
              ref={setActivatorNodeRef}
              type="button"
              aria-label={`Drag ${collection.name}`}
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

const ALL_LIBRARIES = "all";

/**
 * Server collections: library pills, one row of cards, and See all for the
 * chosen library. Hidden only when no library has any, so one-profile accounts see it too.
 */
function ServerCollectionsSection() {
  const { data, isLoading } = useServerCollections();
  const [selected, setSelected] = useState(ALL_LIBRARIES);
  const libraries = data ?? [];

  if (isLoading)
    return (
      <CollectionsSection id="server-collections" title="Server collections">
        <PosterGridSkeleton />
      </CollectionsSection>
    );
  if (libraries.length === 0) return null;

  const library =
    libraries.length === 1
      ? libraries[0]
      : libraries.find((entry) => String(entry.library_id) === selected);
  // All libraries: every library's cards in library order, each collection
  // once. A collection in several libraries opens across all of them, so its
  // card carries no library (and so no sidebar pin, which needs one).
  const libraryCount = new Map<string, number>();
  for (const entry of libraries) {
    for (const collection of entry.collections) {
      libraryCount.set(collection.id, (libraryCount.get(collection.id) ?? 0) + 1);
    }
  }
  const seen = new Set<string>();
  const cards = (library ? [library] : libraries).flatMap((entry) =>
    entry.collections.flatMap((collection) => {
      if (seen.has(collection.id)) return [];
      seen.add(collection.id);
      const inSeveral = !library && (libraryCount.get(collection.id) ?? 0) > 1;
      return [{ collection, libraryId: inSeveral ? undefined : entry.library_id }];
    }),
  );
  return (
    <CollectionsSection
      id="server-collections"
      title="Server collections"
      action={
        library ? (
          <Link
            to={`/library/${library.library_id}?tab=collections`}
            aria-label={`See all ${library.total_count} ${library.library_name} collections`}
            className="text-sm font-medium underline-offset-4 hover:underline"
          >
            See all {library.total_count}
          </Link>
        ) : null
      }
    >
      {libraries.length > 1 ? (
        <PillSwitcher
          label="Library"
          options={[
            { value: ALL_LIBRARIES, label: "All" },
            ...libraries.map((entry) => ({
              value: String(entry.library_id),
              label: entry.library_name,
            })),
          ]}
          value={library ? String(library.library_id) : ALL_LIBRARIES}
          onChange={setSelected}
        />
      ) : null}
      {/* One row: as many cards as the grid has columns. */}
      <ul
        className={cn(
          POSTER_GRID,
          "max-sm:[&>li:nth-child(n+4)]:hidden max-lg:[&>li:nth-child(n+6)]:hidden [&>li:nth-child(n+8)]:hidden",
        )}
      >
        {cards.map(({ collection, libraryId }) => (
          <li key={collection.id}>
            <CollectionPosterCard
              collection={collection}
              kind="regular"
              libraryId={libraryId}
              meta={<CollectionMetaLine className="text-xs" itemCount={collection.item_count} />}
            />
          </li>
        ))}
      </ul>
    </CollectionsSection>
  );
}
