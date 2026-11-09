import { useAllowCollectionDialog } from "@/components/profiles/useAllowCollectionDialog";
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
import { Link, useNavigate, useSearchParams } from "react-router";
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

import type { Collection, LibraryTabCollection, ServerCollectionsLibrary } from "@/api/types";
import { PillSwitcher, type PillOption } from "@/components/calm/PillSwitcher";
import { PageHeader } from "@/components/page/PageHeader";
import { CollectionActionsMenu } from "@/components/collections/CollectionActionsMenu";
import { NewCollectionPicker } from "@/components/collections/NewCollectionPicker";
import { CollectionPosterCard } from "@/components/collections/CollectionPosterCard";
import {
  CollectionMetaLine,
  type SyncAttention,
} from "@/components/collections/editor/CollectionMetaLine";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import RefreshFailedNotice from "@/components/RefreshFailedNotice";
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
import { useServerCollectionCards } from "@/hooks/queries/serverCollectionCards";
import { useSyncUserCollection } from "@/hooks/queries/userCollectionImports";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useDialogSearchParam } from "@/hooks/useDialogSearchParam";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useIntersectionObserver } from "@/hooks/useIntersectionObserver";
import { useLoadMoreFocus } from "@/hooks/useLoadMoreFocus";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { useUICustomization } from "@/hooks/useUICustomization";
import { personalDeleteDescription, unshareConsequence } from "@/lib/collections/copy";
import { NEW_COLLECTION_DIALOG } from "@/lib/collections/dialogs";
import { partitionPersonalCollections } from "@/lib/collections/personalOwnership";
import { addToMyHomePath } from "@/lib/collections/rows";
import { PERSONAL_SCOPE } from "@/lib/collections/scope";
import { COLLECTION_KIND_LABEL, collectionKindOf } from "@/lib/collections/types";
import { cardGridClasses } from "@/lib/uiCustomization";
import { cn } from "@/lib/utils";

/** Under this width New collection moves to a bar docked at the bottom. */
const NARROW_QUERY = "(max-width: 1023px)";

/** The wall's poster grid, sized by the viewer's poster size setting. */
function usePosterGrid() {
  const { cardPresentation } = useUICustomization();
  return cardGridClasses(cardPresentation.poster_size);
}

/**
 * What the wall shows: everything, the profile's own collections, the ones
 * other profiles share, every server collection, or one library's. It lives
 * in the URL (`?show=mine`, `?library=2`) so a reload or a link keeps it.
 */
type Show = "all" | "mine" | "shared" | "server" | `library:${string}`;

const WALL_LABEL: Partial<Record<Show, string>> = {
  mine: "Your collections",
  shared: "Shared with me",
  server: "Server collections",
};

const SHOW_PARAM = "show";
const LIBRARY_PARAM = "library";

/**
 * The Collections page: one poster wall of the profile's own collections, the
 * ones other profiles share, and the server's, filtered by Show. The header
 * button (docked at the bottom on narrow screens) is the one way to start a
 * collection, and a view with nothing in it says so in one line.
 */
export default function Collections() {
  useDocumentTitle("Collections");
  const { data, isLoading, isSuccess: personalLoaded } = useCollections();
  const { data: capabilities } = useCollectionCapabilities();
  const {
    data: profiles = [],
    isLoading: profilesLoading,
    isSuccess: profilesLoaded,
  } = useProfiles();
  const { profile } = useCurrentProfile();
  const server = useServerCollections();
  const libraries = server.data ?? [];
  const { own, shared } = useMemo(
    () => partitionPersonalCollections(data ?? [], profile?.id, profiles),
    [data, profile?.id, profiles],
  );
  const otherProfileNames = profiles
    .filter((entry) => entry.id !== profile?.id)
    .map((entry) => entry.name);
  // A one-profile account has nobody to share with: no sharing switch, no Shared.
  const multiProfile = otherProfileNames.length > 0;
  const sharedCards = multiProfile
    ? shared.flatMap((group) =>
        group.collections.map((collection) => ({ collection, owner: group.owner.name })),
      )
    : [];
  // Totals are per library and a collection can be in several, so only a lone
  // library's total counts the server's collections.
  const serverCount = libraries.length > 1 ? undefined : (libraries[0]?.total_count ?? 0);
  const narrow = useMediaQuery(NARROW_QUERY);
  const [pickerOpen, setPickerOpen] = useDialogSearchParam(NEW_COLLECTION_DIALOG);
  const [searchParams, setSearchParams] = useSearchParams();

  // A filter the account has nothing for falls back to All. A filter in the
  // URL is kept while what it needs loads, so a reload doesn't flash All. A
  // lone library has no pill of its own and shows as Server.
  const libraryParam = searchParams.get(LIBRARY_PARAM);
  const showParam = searchParams.get(SHOW_PARAM);
  const libraryMatch = libraries.find((entry) => String(entry.library_id) === libraryParam);
  let show: Show = "all";
  if (libraryParam && server.isLoading) show = `library:${libraryParam}`;
  else if (libraryMatch) show = libraries.length > 1 ? `library:${libraryParam}` : "server";
  else if (showParam === "mine") show = "mine";
  else if (showParam === "shared" && (sharedCards.length > 0 || isLoading || profilesLoading))
    show = "shared";
  else if (showParam === "server" && (libraries.length > 0 || server.isLoading)) show = "server";
  const picked = show.startsWith("library:") ? libraryMatch : undefined;
  const showMine = show === "all" || show === "mine";
  const showShared = sharedCards.length > 0 && (show === "all" || show === "shared");
  const showServer = show !== "mine" && show !== "shared";
  const canReorder = capabilities?.item_reorder === true;
  const wallLabel =
    WALL_LABEL[show] ?? (picked ? `${picked.library_name} collections` : "All collections");
  const personalLoading = (showMine || show === "shared") && isLoading;
  const serverLoading = showServer && server.isLoading;
  // A view with nothing in it says so rather than leaving the wall blank, but
  // only once what it shows has loaded: a slow or failed request isn't "none".
  let emptyMessage: string | undefined;
  if (show === "mine" && personalLoaded && own.length === 0)
    emptyMessage = "You haven't made any collections yet.";
  else if (
    show === "all" &&
    personalLoaded &&
    profilesLoaded &&
    server.isSuccess &&
    own.length + sharedCards.length + libraries.length === 0
  )
    emptyMessage = "There are no collections yet.";

  function changeShow(next: string) {
    setSearchParams(
      (current) => {
        const params = new URLSearchParams(current);
        params.delete(SHOW_PARAM);
        params.delete(LIBRARY_PARAM);
        if (next.startsWith("library:")) params.set(LIBRARY_PARAM, next.slice("library:".length));
        else if (next !== "all") params.set(SHOW_PARAM, next);
        return params;
      },
      { replace: true },
    );
  }

  const filterOptions: PillOption[] = [
    {
      value: "all",
      label: "All",
      count: serverCount === undefined ? undefined : own.length + sharedCards.length + serverCount,
    },
    { value: "mine", label: "Mine", count: own.length },
  ];
  if (sharedCards.length > 0)
    filterOptions.push({ value: "shared", label: "Shared", count: sharedCards.length });
  if (libraries.length > 0)
    filterOptions.push({ value: "server", label: "Server", count: serverCount });
  if (libraries.length > 1)
    libraries.forEach((library, index) =>
      filterOptions.push({
        value: `library:${library.library_id}`,
        label: library.library_name,
        count: library.total_count,
        separated: index === 0,
      }),
    );

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
    <div className={cn("py-4 sm:py-6", narrow && "pb-24")}>
      <PageHeader
        className="mb-5"
        title="Collections"
        subtitle={
          multiProfile
            ? "Yours, the ones other profiles share with you, and the server's."
            : "Yours and the server's."
        }
        actions={narrow ? null : newCollection}
      />
      <div className="grid grid-cols-[minmax(0,1fr)] gap-4">
        {sharedCards.length > 0 || libraries.length > 0 ? (
          <PillSwitcher label="Show" options={filterOptions} value={show} onChange={changeShow} />
        ) : null}
        {emptyMessage ? <p className="text-muted-foreground text-[13px]">{emptyMessage}</p> : null}
        {/* The personal cards' place while they load; the server's don't wait for them. */}
        {personalLoading ? <PosterGridSkeleton /> : null}
        <Wall
          label={wallLabel}
          before={
            personalLoading ? null : (
              <>
                {showMine ? (
                  <YourCollections
                    collections={own}
                    canReorder={canReorder}
                    canSync={capabilities?.imports === true}
                    otherProfileNames={otherProfileNames}
                  />
                ) : null}
                {showShared ? <SharedCards cards={sharedCards} /> : null}
              </>
            )
          }
          libraries={showServer ? (picked ? [picked] : libraries) : []}
          loadOnScroll={picked === undefined}
        />
        {serverLoading && !personalLoading ? <PosterGridSkeleton /> : null}
        {show === "mine" && canReorder && own.length > 1 ? (
          <p className="text-muted-foreground flex items-center gap-2 text-[13px]">
            <GripVertical aria-hidden className="size-3.5" />
            Drag a poster to change the order, or focus its handle and press Space, then the arrow
            keys.
          </p>
        ) : null}
      </div>
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

/** Other profiles' shared collections, read-only, each naming its owner. */
function SharedCards({ cards }: { cards: Array<{ collection: Collection; owner: string }> }) {
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

function PosterGridSkeleton() {
  const grid = usePosterGrid();
  return (
    <div className={grid} aria-hidden>
      {Array.from({ length: 8 }, (_, index) => (
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
  const allowFor = useAllowCollectionDialog(otherProfileNames.length > 0);
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
    <>
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
      {allowFor.dialog}
      {collections.length === 0 ? null : (
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
                    onAllowForProfiles={allowFor.openFor && (() => allowFor.openFor?.(collection))}
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
      )}
    </>
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

// SortableCollectionGrid makes the profile's own collections drag-sortable
// inside the wall's list, which may go on with other cards after them. It
// reports the new full order of ids.
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
      // In the body, not inline: inline it would put divs inside the wall's list.
      accessibility={{ announcements: moveAnnouncements(collections), container: document.body }}
      onDragStart={handleDragStart}
      onDragEnd={handleDragEnd}
      onDragCancel={blockClicks.settle}
    >
      <SortableContext items={ids} strategy={rectSortingStrategy} disabled={disabled}>
        {children}
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

/**
 * The wall: one poster grid named for what the filter shows. Personal cards
 * (`before`) come first, then every server collection of `libraries`, so all
 * the cards flow on in the same rows. The aggregated
 * answer carries only each library's first 20, so the rest come from the
 * libraries' Collections tabs, which list the same collections in the same
 * order. A picked library loads its rest straight away. The page's default
 * view (All, or a lone library) loads one library's rest at a time, each time
 * the end of the grid nears the viewport or the viewer presses Load more, so
 * opening the page reads only the capped answer unless the grid is short.
 * Every library's first 20 come before any library's rest. If a list fails,
 * that library's first 20 stay with a link to its Collections tab; if a list
 * already on screen fails to refresh, it stays with a notice offering Try
 * again. Pressing Load more moves focus to the first card it added, or to
 * the library's link when its list failed and nothing is left to load.
 */
function Wall({
  label,
  before,
  libraries,
  loadOnScroll,
}: {
  label: string;
  before: ReactNode;
  libraries: ServerCollectionsLibrary[];
  loadOnScroll: boolean;
}) {
  const { cards, loading, next, failed, refreshFailed, loadNext } = useServerCollectionCards(
    libraries,
    loadOnScroll,
  );
  const sentinelRef = useIntersectionObserver({
    onIntersect: loadNext,
    enabled: loadOnScroll && !loading && next !== undefined,
  });
  const grid = usePosterGrid();
  const { listRef, buttonRef, fallbackRef, press } = useLoadMoreFocus<HTMLUListElement>(loading);
  const pressLoadMore = () => {
    if (loading || !next) return;
    press();
    loadNext();
  };
  return (
    <section aria-label={label} className="grid gap-4">
      {refreshFailed.map(({ library, error, retry, retrying }) => (
        <RefreshFailedNotice
          key={library.library_id}
          message={`Couldn't refresh the collections in ${library.library_name}.`}
          error={error}
          onRetry={retry}
          retrying={retrying}
        />
      ))}
      <ul ref={listRef} className={grid}>
        {before}
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
      {loading ? <PosterGridSkeleton /> : null}
      {next || (loadOnScroll && loading) ? (
        // Scrolling near it presses it; it stays for keyboard and screen
        // reader users, who may read the grid without scrolling. It stays
        // mounted while a library's rest loads so a press keeps its focus,
        // and is aria-disabled rather than disabled, which would drop focus.
        <div ref={sentinelRef} className="flex justify-center">
          <Button
            ref={buttonRef}
            variant="outline"
            size="sm"
            className="aria-disabled:opacity-50"
            aria-disabled={loading || undefined}
            onClick={pressLoadMore}
          >
            Load more
          </Button>
        </div>
      ) : null}
      {failed.length > 0 ? (
        <div ref={fallbackRef} className="flex flex-wrap gap-x-4 gap-y-1">
          {failed.map((entry) => (
            <Link
              key={entry.library_id}
              to={`/library/${entry.library_id}?tab=collections`}
              className="text-sm font-medium underline-offset-4 hover:underline"
            >
              See all {entry.total_count} in {entry.library_name}
            </Link>
          ))}
        </div>
      ) : null}
    </section>
  );
}
