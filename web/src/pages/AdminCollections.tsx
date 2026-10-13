import { toast } from "sonner";
import {
  fetchAdminCollectionSnapshot,
  prepareAdminCollectionDeletes,
  adminMutationMessage,
} from "@/api/adminCollections";
import type { AdminCollectionDeleteSnapshot } from "@/api/adminCollections";
import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { AdminJob, LibraryCollection } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useAdminCollectionsBoard } from "@/hooks/queries/admin/collectionGroups";
import {
  useAdminCollectionRows,
  useDeleteCollectionRows,
} from "@/hooks/queries/admin/collectionRows";
import {
  patchAdminCollectionField,
  useAdminCollectionCapabilities,
  useAdminCollections,
  useDeleteAdminCollections,
  useSetAdminCollectionVisibility,
  useTemplateBundleApplyJobs,
} from "@/hooks/queries/admin/collections";
import { useScopeSync } from "@/hooks/queries/collectionScope";
import { invalidateAdminCollectionQueries } from "@/hooks/queries/collectionSurfaceRefresh";
import { sectionKeys } from "@/hooks/queries/keys";
import { useDialogSearchParam } from "@/hooks/useDialogSearchParam";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { useEventChannel } from "@/components/realtimeEventsContext";
import { CalmPage } from "@/components/calm/CalmPage";
import { PageMoreMenu, type PageMoreMenuItem } from "@/components/calm/PageMoreMenu";
import { PillSwitcher } from "@/components/calm/PillSwitcher";
import { SelectAllHeader, SelectModeBar } from "@/components/calm/SelectModeBar";
import { CollectionActionsMenu } from "@/components/collections/CollectionActionsMenu";
import { DeleteCollectionDialog } from "@/components/collections/DeleteCollectionDialog";
import { DeleteCollectionsDialog } from "@/components/collections/DeleteCollectionsDialog";
import { HideCollectionDialog } from "@/components/collections/HideCollectionDialog";
import {
  CollectionColumnHeader,
  CollectionListItem,
} from "@/components/collections/admin/CollectionListItem";
import { GroupsBoard } from "@/components/collections/admin/GroupsBoard";
import { MobileDockBar } from "@/components/homeRows/MobileDockBar";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  AlertCircle,
  CheckCircle2,
  Eye,
  EyeOff,
  Info,
  Layers,
  Layers3,
  LayoutGrid,
  Library as LibraryIcon,
  List,
  Loader2,
  Plus,
  RefreshCw,
  Search,
  SquareCheckBig,
  Trash2,
} from "lucide-react";
import { NewCollectionPicker } from "@/components/collections/NewCollectionPicker";
import { StarterPacksDialog } from "@/components/collections/StarterPacksDialog";
import {
  collectionLibraryIds,
  countByLibrary,
  filterAdminCollections,
  readAdminListState,
  writeAdminListState,
  type AdminListState,
  type AdminListView,
  type KindFilter,
} from "@/lib/collections/adminList";
import { MAX_SELECTED_COLLECTIONS, runBatch } from "@/lib/collections/batch";
import {
  CHECK_BEFORE_DELETE_FAILED,
  COLLECTION_IN_USE,
  COLLECTIONS_IN_USE,
  SHOW_IT_FIRST,
  STARTER_PACK_BLOCKS_DELETE,
  alreadyShown,
  batchResult,
  rowsLeftMessage,
  syncListsLabel,
  syncSkipNote,
  type BatchAction,
} from "@/lib/collections/copy";
import {
  NEW_COLLECTION_DIALOG,
  STARTER_PACKS_DIALOG,
  withoutDialog,
} from "@/lib/collections/dialogs";
import { listReturnState } from "@/lib/collections/listReturn";
import { serverCollectionPeek } from "@/lib/collections/peek";
import {
  addRowPath,
  rowPages,
  rowLabels,
  type CollectionRow,
  type RowsState,
} from "@/lib/collections/rows";
import { collectionsInAdminScope, SERVER_SCOPE } from "@/lib/collections/scope";
import {
  COLLECTION_KIND_LABEL,
  collectionKindOf,
  isListBackedCollectionType,
} from "@/lib/collections/types";
import {
  useCollectionTemplateBundles,
  type ApplyCollectionTemplateBundleResponse,
} from "@/lib/collectionTemplates";
import {
  packAdded,
  packResultHeading,
  packResultSummary,
  starterPacksOf,
} from "@/lib/collections/starterPacks";
import { updateCheckboxSelection } from "@/lib/checkboxSelection";
import { cn } from "@/lib/utils";
import { buildLibraryCollectionCatalogHref } from "./catalogSearchParams";

/** Under this width More and New collection move to a bar docked at the bottom. */
const NARROW_QUERY = "(max-width: 1023px)";
const ALL_LIBRARIES = "all";

const KIND_OPTIONS: ReadonlyArray<{ value: KindFilter; label: string }> = [
  { value: "all", label: "All" },
  { value: "manual", label: COLLECTION_KIND_LABEL.manual },
  { value: "smart", label: COLLECTION_KIND_LABEL.smart },
  { value: "synced", label: COLLECTION_KIND_LABEL.synced },
];

/** Several collections about to be deleted together, each read fresh for its ETag. */
interface BulkDelete {
  snapshots: AdminCollectionDeleteSnapshot[];
  /** Titles left alone because rows use them. */
  kept: string[];
  /** Delete all in this view, rather than the selection. */
  wholeView: boolean;
}

/** One line per collection a select-mode action couldn't change. */
function batchFailure(collection: LibraryCollection, error: unknown): string {
  return `${collection.title}: ${SERVER_SCOPE.errorMessage(error, "Something went wrong")}`;
}

function showBatchResult(
  action: BatchAction,
  done: number,
  total: number,
  failures: string[],
  warned: number,
) {
  const { tone, message } = batchResult(action, done, total, warned);
  if (failures.length === 0) toast[tone](message);
  else toast[tone](message, { description: failures.join(" ") });
}

/** A collection being deleted from the list or the board, read fresh for its ETag. */
interface PendingDelete {
  collection: LibraryCollection;
  etag: string;
  error: string | null;
  /** Rows show it (or the server said so): read them, and delete them first. */
  checkRows: boolean;
  /** The rows a delete is working through, so the dialog doesn't change under it. */
  rows: CollectionRow[] | null;
}

const DELETE_CHANGED = "It changed since you opened this. Check it, then delete again.";

// A server collection's DELETE answers 409 (problem type "conflict") only
// while a row still shows it.
function isCollectionInUse(error: unknown) {
  return error instanceof V2ProblemError && error.status === 409;
}

function deleteErrorMessage(error: unknown): string {
  if (isCollectionInUse(error)) return COLLECTION_IN_USE;
  return SERVER_SCOPE.errorMessage(error, "Couldn't delete it");
}

/**
 * Server collections: the List of every collection (`?view=list`, the
 * default) and a library's shelves (`?view=arrange&libraryId=N`). The URL
 * holds the view, library, type, search and failed-sync filter, and editors
 * opened from here come back to it.
 */
export default function AdminCollections() {
  useDocumentTitle("Collections");
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const [searchParams, setSearchParams] = useSearchParams();
  const state = useMemo(() => readAdminListState(searchParams), [searchParams]);
  const narrow = useMediaQuery(NARROW_QUERY);
  const libraries = useAdminLibraries();
  const libraryList = useMemo(() => libraries.data ?? [], [libraries.data]);
  const libraryNames = useMemo(
    () => new Map(libraryList.map((library) => [library.id, library.name])),
    [libraryList],
  );
  const arrangeLibraryId =
    state.view === "arrange" ? (state.libraryId ?? libraryList[0]?.id ?? null) : null;
  const activeLibraryId = state.view === "arrange" ? arrangeLibraryId : state.libraryId;
  // Where an editor opened from here comes back to, without the dialog that was open.
  const listHref = withoutDialog(`${location.pathname}${location.search}`);

  const [starterPacksOpen, setStarterPacksOpen] = useDialogSearchParam(STARTER_PACKS_DIALOG);
  const [pickerOpen, setPickerOpen] = useDialogSearchParam(NEW_COLLECTION_DIALOG);
  const [pendingDelete, setPendingDelete] = useState<PendingDelete | null>(null);
  // The Delete button closes its dialog as it's pressed; keep it open for the answer.
  const holdDeleteOpen = useRef(false);
  const [bulkDelete, setBulkDelete] = useState<BulkDelete | null>(null);
  const [hiding, setHiding] = useState<LibraryCollection | null>(null);
  // Hide from tabs in select mode, waiting on the confirm because rows use some.
  const [bulkHiding, setBulkHiding] = useState<LibraryCollection[] | null>(null);
  const [selectMode, setSelectMode] = useState(false);
  const [selectedIds, setSelectedIds] = useState<ReadonlySet<string>>(new Set());
  const selectionAnchor = useRef<string | null>(null);
  const [batchRunning, setBatchRunning] = useState(false);
  const moreTrigger = useRef<HTMLButtonElement>(null);
  const selectAll = useRef<HTMLButtonElement>(null);
  // The switch runs ahead of the list while a change saves.
  const [visibilityOverrides, setVisibilityOverrides] = useState<ReadonlyMap<string, boolean>>(
    new Map(),
  );
  const [syncingIds, setSyncingIds] = useState<ReadonlySet<string>>(new Set());

  const { data: capabilities } = useAdminCollectionCapabilities();
  // Without imports a synced list's sync can only fail, so Sync isn't offered.
  const canImport = capabilities?.imports === true;
  const allCollections = useAdminCollections();
  const collections = useMemo(() => allCollections.data ?? [], [allCollections.data]);
  const libraryCounts = useMemo(() => countByLibrary(collections), [collections]);
  const listed = useMemo(() => filterAdminCollections(collections, state), [collections, state]);
  const inLibrary = useMemo(
    () => filterAdminCollections(collections, { ...state, kind: "all", q: "", failed: false }),
    [collections, state],
  );
  const failedCount = useMemo(
    () => filterAdminCollections(collections, { ...state, failed: true }).length,
    [collections, state],
  );
  // Select mode works on the List; switching to Arrange leaves it, and so
  // does a List with nothing left to pick (another library, or the last ones
  // deleted), which would show no Select all or Done.
  if (selectMode && state.view === "list" && allCollections.isSuccess && inLibrary.length === 0) {
    setSelectMode(false);
    setSelectedIds(new Set());
  }
  const selecting = selectMode && state.view === "list";
  const selected = useMemo(
    () => listed.filter((collection) => selectedIds.has(collection.id)),
    [listed, selectedIds],
  );
  const selectedLists = selected.filter((collection) =>
    isListBackedCollectionType(collection.collection_type),
  );
  const selectedSmart = selected.filter(
    (collection) => collectionKindOf(collection.collection_type) === "smart",
  ).length;

  // Entering select mode puts focus on Select all; leaving it, on More when
  // the Done button or checkbox that had focus is gone.
  const wasSelecting = useRef(selecting);
  useEffect(() => {
    if (wasSelecting.current === selecting) return;
    wasSelecting.current = selecting;
    if (selecting) selectAll.current?.focus();
    else if (document.activeElement === document.body || !document.activeElement)
      moreTrigger.current?.focus();
  }, [selecting]);

  const board = useAdminCollectionsBoard(arrangeLibraryId ?? undefined);
  const viewCollections = useMemo(
    () =>
      state.view === "arrange"
        ? collectionsInAdminScope(collections, board.data, arrangeLibraryId)
        : listed,
    [arrangeLibraryId, board.data, collections, listed, state.view],
  );
  const deleteCollections = useDeleteAdminCollections();
  const setVisibility = useSetAdminCollectionVisibility();
  const sync = useScopeSync(SERVER_SCOPE);
  const removeOne = useMutation({
    retry: false,
    mutationFn: (ref: { id: string; etag: string }) => SERVER_SCOPE.remove(ref),
  });
  // The rows that show a collection being deleted, when the server reports them.
  const rowsReported = capabilities?.section_references === true;
  const deleteRowsQuery = useAdminCollectionRows(
    pendingDelete?.collection.id,
    rowsReported && Boolean(pendingDelete?.checkRows),
  );
  const deleteRows = useDeleteCollectionRows();
  // Reading the collection's ETag before any row goes.
  const [checkingDelete, setCheckingDelete] = useState(false);
  let deleteRowsState: RowsState | null = null;
  if (pendingDelete?.checkRows && rowsReported) {
    if (pendingDelete.rows) deleteRowsState = { status: "ready", rows: pendingDelete.rows };
    else if (deleteRowsQuery.data)
      deleteRowsState = { status: "ready", rows: deleteRowsQuery.data };
    else if (deleteRowsQuery.isError)
      deleteRowsState = { status: "error", onRetry: () => void deleteRowsQuery.refetch() };
    else deleteRowsState = { status: "loading" };
  }
  const applyJobs = useTemplateBundleApplyJobs();
  useEventChannel("jobs");
  const latestApplyJob = applyJobs.data?.[0] ?? null;
  const activeApplyJob = latestApplyJob !== null && isActiveTemplateBundleApplyJob(latestApplyJob);
  const lastInvalidatedJobID = useRef<string | null>(null);

  useEffect(() => {
    if (!latestApplyJob || activeApplyJob || lastInvalidatedJobID.current === latestApplyJob.id) {
      return;
    }
    lastInvalidatedJobID.current = latestApplyJob.id;
    void invalidateAdminCollectionQueries(queryClient);
    void queryClient.invalidateQueries({ queryKey: sectionKeys.all });
  }, [activeApplyJob, latestApplyJob, queryClient]);

  function update(patch: Partial<AdminListState>, options: { replace?: boolean } = {}) {
    setSearchParams(
      (current) => writeAdminListState(current, { ...readAdminListState(current), ...patch }),
      options,
    );
  }

  function setView(view: AdminListView) {
    if (view === state.view) return;
    if (view === "arrange") exitSelectMode();
    // Arrange works on one library: from All libraries it opens the first.
    update(
      view === "arrange"
        ? { view, libraryId: state.libraryId ?? libraryList[0]?.id ?? null }
        : { view, libraryId: arrangeLibraryId },
    );
  }

  function openEditor(collection: LibraryCollection, libraryId = activeLibraryId) {
    navigate(SERVER_SCOPE.paths.edit(collection.id, { libraryId }), {
      state: listReturnState(listHref),
    });
  }

  /** The libraries a collection is in that this page knows by name. */
  function librariesOf(collection: LibraryCollection): Array<{ id: number; name: string }> {
    return collectionLibraryIds(collection).flatMap((id) => {
      const name = libraryNames.get(id);
      return name ? [{ id, name }] : [];
    });
  }

  function namesOf(collection: LibraryCollection): string[] {
    return librariesOf(collection).map((library) => library.name);
  }

  function isVisible(collection: LibraryCollection) {
    return visibilityOverrides.get(collection.id) ?? collection.visibility !== "hidden";
  }

  // Per-row cleanup chains on the promise: `mutate`'s own callbacks fire only
  // for the latest call, so a second row's change would strand the first.
  // The hooks report failures.
  function saveVisible(collection: LibraryCollection, visible: boolean) {
    setVisibilityOverrides((current) => new Map(current).set(collection.id, visible));
    void setVisibility
      .mutateAsync({ id: collection.id, visible })
      .catch(() => undefined)
      .finally(() =>
        setVisibilityOverrides((current) => {
          const next = new Map(current);
          next.delete(collection.id);
          return next;
        }),
      );
  }

  function changeVisible(collection: LibraryCollection, visible: boolean) {
    // Rows that show it would keep showing it with a See all that can't open.
    if (!visible && (collection.row_count ?? 0) > 0) setHiding(collection);
    else saveVisible(collection, visible);
  }

  function syncNow(collection: LibraryCollection) {
    setSyncingIds((current) => new Set(current).add(collection.id));
    void sync
      .mutateAsync(collection.id)
      .catch(() => undefined)
      .finally(() =>
        setSyncingIds((current) => {
          const next = new Set(current);
          next.delete(collection.id);
          return next;
        }),
      );
  }

  async function prepareDelete(collection: LibraryCollection) {
    // A single read carries no row counts and Arrange's board doesn't either;
    // the List's does. A 409 on delete lists the rows anyway.
    const rowCount =
      collections.find((entry) => entry.id === collection.id)?.row_count ??
      collection.row_count ??
      0;
    try {
      const snapshot = await fetchAdminCollectionSnapshot(collection.id);
      setPendingDelete({
        collection: snapshot.collection,
        etag: snapshot.etag,
        error: null,
        checkRows: rowsReported && rowCount > 0,
        rows: null,
      });
    } catch (error) {
      toast.error(adminMutationMessage(error, "Could not load collection"));
    }
  }

  async function confirmDelete() {
    if (!pendingDelete) return;
    const { id } = pendingDelete.collection;
    const rows = deleteRowsState?.status === "ready" ? [...deleteRowsState.rows] : [];
    if (rows.length > 0) {
      // A deleted row can't come back: check the collection's ETag first, so
      // a Delete that would answer 412 deletes nothing.
      setCheckingDelete(true);
      const fresh = await fetchAdminCollectionSnapshot(id).catch(() => null);
      setCheckingDelete(false);
      if (fresh?.etag !== pendingDelete.etag) {
        setPendingDelete((current) =>
          current?.collection.id === id
            ? {
                ...current,
                ...fresh,
                rows: null,
                error: fresh ? DELETE_CHANGED : CHECK_BEFORE_DELETE_FAILED,
              }
            : current,
        );
        return;
      }
      setPendingDelete((current) => current && { ...current, rows, error: null });
      const { remaining } = await deleteRows
        .mutateAsync({ collectionId: id, rows })
        .catch(() => ({ remaining: rows }));
      if (remaining.length > 0) {
        setPendingDelete((current) =>
          current?.collection.id === id
            ? {
                ...current,
                rows: remaining,
                error: rowsLeftMessage(rowLabels(remaining, libraryNames)),
              }
            : current,
        );
        return;
      }
    }
    holdDeleteOpen.current = true;
    removeOne.mutate(
      { id: pendingDelete.collection.id, etag: pendingDelete.etag },
      {
        onSuccess: () => {
          toast.success("Collection deleted");
          setPendingDelete(null);
        },
        onError: (error) => void showDeleteError(error),
        onSettled: () => {
          holdDeleteOpen.current = false;
          void SERVER_SCOPE.invalidate(queryClient);
        },
      },
    );
  }

  /**
   * A 412 means it changed under the dialog: read it again so the next Delete
   * sends its ETag. A 409 means rows show it after all: list them.
   */
  async function showDeleteError(error: unknown) {
    const id = pendingDelete?.collection.id;
    if (rowsReported && isCollectionInUse(error)) {
      setPendingDelete((current) =>
        current && current.collection.id === id
          ? { ...current, checkRows: true, rows: null, error: null }
          : current,
      );
      void deleteRowsQuery.refetch();
      return;
    }
    let fresh: Pick<PendingDelete, "collection" | "etag"> | null = null;
    if (id && error instanceof V2ProblemError && error.status === 412) {
      fresh = await fetchAdminCollectionSnapshot(id).catch(() => null);
    }
    setPendingDelete((current) =>
      current && current.collection.id === id
        ? {
            ...current,
            ...fresh,
            rows: null,
            error: fresh ? DELETE_CHANGED : deleteErrorMessage(error),
          }
        : current,
    );
  }

  function exitSelectMode() {
    setSelectMode(false);
    setSelectedIds(new Set());
    selectionAnchor.current = null;
  }

  function enterSelectMode() {
    setView("list");
    setSelectMode(true);
  }

  function changeSelection(id: string, checked: boolean, extendRange: boolean) {
    const ids = listed.map((collection) => collection.id);
    const anchor = extendRange && selectedIds.size > 0 ? selectionAnchor.current : null;
    setSelectedIds((current) =>
      updateCheckboxSelection(current, ids, anchor, id, checked, extendRange),
    );
    if (anchor === null || !ids.includes(anchor)) selectionAnchor.current = id;
  }

  // Escape leaves select mode, but only for keys pressed inside the list or
  // its bar: menus and dialogs render in portals outside them.
  function handleSelectKeyDown(event: KeyboardEvent<HTMLElement>) {
    if (
      event.key !== "Escape" ||
      event.defaultPrevented ||
      !selecting ||
      !event.currentTarget.contains(event.target as Node)
    )
      return;
    exitSelectMode();
  }

  /** Runs one select-mode action over `targets`, then reports and refreshes once. */
  async function runSelected(
    action: BatchAction,
    targets: LibraryCollection[],
    run: (collection: LibraryCollection) => Promise<unknown>,
    warnedCount: () => number = () => 0,
  ) {
    setBatchRunning(true);
    try {
      const { done, failures } = await runBatch(targets, run, batchFailure);
      showBatchResult(action, done, targets.length, failures, warnedCount());
    } finally {
      setBatchRunning(false);
      await SERVER_SCOPE.invalidate(queryClient);
    }
  }

  function syncSelected() {
    setSyncingIds((current) => new Set([...current, ...selectedLists.map((list) => list.id)]));
    let warned = 0;
    void runSelected(
      "sync",
      selectedLists,
      async (list) => {
        try {
          const result = await SERVER_SCOPE.sync(list.id);
          if (result.status === "failed") throw new Error(result.message || "Sync failed");
          if (result.status === "warning") warned++;
        } finally {
          setSyncingIds((current) => {
            const next = new Set(current);
            next.delete(list.id);
            return next;
          });
        }
      },
      () => warned,
    );
  }

  /**
   * `targets` with the row counts the List gives when read again: rows may
   * have been added or removed since it loaded. Only the List carries row
   * counts (Arrange's board doesn't). Throws when the read fails rather than
   * fall back on counts that may be stale.
   */
  async function withFreshRowCounts(targets: LibraryCollection[]) {
    const { data: fresh = collections, error } = await allCollections.refetch();
    if (error) throw error;
    const rowCounts = new Map(fresh.map((collection) => [collection.id, collection.row_count]));
    return targets.map((collection) => ({
      ...collection,
      row_count: rowCounts.get(collection.id) ?? collection.row_count,
    }));
  }

  async function setSelectedVisible(visible: boolean) {
    const changing = selected.filter((collection) => isVisible(collection) !== visible);
    if (changing.length === 0) {
      toast.success(alreadyShown(visible));
      return;
    }
    if (visible) {
      void saveSelectedVisible(changing, true);
      return;
    }
    // The bar stays busy while the List is read again.
    setBatchRunning(true);
    let hiding: LibraryCollection[];
    try {
      hiding = await withFreshRowCounts(changing);
    } catch (error) {
      toast.error(adminMutationMessage(error, "Could not check which rows use them"));
      return;
    } finally {
      setBatchRunning(false);
    }
    // Rows that show them would keep showing them with a See all that can't open.
    if (hiding.some((collection) => (collection.row_count ?? 0) > 0)) setBulkHiding(hiding);
    else void saveSelectedVisible(hiding, false);
  }

  async function saveSelectedVisible(targets: LibraryCollection[], visible: boolean) {
    const ids = targets.map((collection) => collection.id);
    setVisibilityOverrides((current) => {
      const next = new Map(current);
      for (const id of ids) next.set(id, visible);
      return next;
    });
    try {
      await runSelected(visible ? "show" : "hide", targets, (collection) =>
        patchAdminCollectionField(collection.id, { visibility: visible ? "visible" : "hidden" }),
      );
    } finally {
      setVisibilityOverrides((current) => {
        const next = new Map(current);
        for (const id of ids) next.delete(id);
        return next;
      });
    }
  }

  const [preparingDelete, setPreparingDelete] = useState(false);
  /**
   * Reads each collection that will go for its ETag and opens the confirm.
   * Collections rows use are kept: the server refuses to delete them.
   */
  async function prepareBulkDelete(targets: LibraryCollection[], wholeView: boolean) {
    setPreparingDelete(true);
    try {
      const counted = await withFreshRowCounts(targets);
      const used = (collection: LibraryCollection) => (collection.row_count ?? 0) > 0;
      const deletable = counted.filter((collection) => !used(collection));
      if (deletable.length === 0) {
        toast.error(COLLECTIONS_IN_USE);
        return;
      }
      const snapshots = await prepareAdminCollectionDeletes(deletable.map((entry) => entry.id));
      setBulkDelete({
        snapshots,
        kept: counted.filter(used).map((collection) => collection.title),
        wholeView,
      });
    } catch (error) {
      toast.error(adminMutationMessage(error, "Could not prepare deletion"));
    } finally {
      setPreparingDelete(false);
    }
  }

  const activeLibrary = libraryList.find((library) => library.id === activeLibraryId) ?? null;
  const deleteProgressLabel = `Deleting ${deleteCollections.progress?.completed ?? 0} of ${deleteCollections.progress?.total ?? viewCollections.length} collections`;
  const deleting = preparingDelete || deleteCollections.isPending;
  // A starter set being added would race a delete of the collections it makes,
  // and a select-mode change would move the ETags a delete has read.
  const bulkBusy = deleting || activeApplyJob || batchRunning;

  function confirmBulkDelete() {
    if (bulkDelete && !activeApplyJob) deleteCollections.mutate(bulkDelete.snapshots);
  }

  const bulkDeleteKinds = new Set(
    bulkDelete?.snapshots.map((entry) => collectionKindOf(entry.collection.collection_type)),
  );
  const bulkDeleteElsewhere =
    bulkDelete && activeLibraryId !== null
      ? bulkDelete.snapshots.flatMap(({ collection }) => {
          const others = librariesOf(collection).filter(
            (library) => library.id !== activeLibraryId,
          );
          return others.length > 0
            ? [{ title: collection.title, libraryNames: others.map((library) => library.name) }]
            : [];
        })
      : [];

  // What the List shows once Select collections opens it, Arrange's library included.
  const selectableCount =
    activeLibraryId === null ? collections.length : (libraryCounts.get(activeLibraryId) ?? 0);
  const moreItems: PageMoreMenuItem[] = [
    {
      key: "starter-packs",
      label: "Starter packs…",
      help: "Add a ready-made set of collections to a library.",
      icon: Layers3,
      disabled: !canImport,
      opensDialog: true,
      onSelect: () => setStarterPacksOpen(true),
    },
    {
      key: "select",
      label: "Select collections",
      help: "Sync, show, hide or delete several at once.",
      icon: SquareCheckBig,
      // Focus moves to Select all once select mode opens.
      returnFocus: false,
      disabled: selecting || selectableCount === 0 || deleting,
      onSelect: enterSelectMode,
    },
    {
      key: "delete-all",
      label: deleteCollections.isPending ? `${deleteProgressLabel}…` : "Delete all in this view…",
      help: "Every collection the current filters show.",
      icon: Trash2,
      group: true,
      disabled: viewCollections.length === 0 || bulkBusy,
      onSelect: () => void prepareBulkDelete(viewCollections, true),
    },
  ];
  const more = <PageMoreMenu items={moreItems} compact={narrow} triggerRef={moreTrigger} />;
  const newCollection = (
    <Button
      type="button"
      size={narrow ? "lg" : "sm"}
      className={cn(narrow && "h-12 rounded-[14px] text-[15px]")}
      onClick={() => setPickerOpen(true)}
    >
      <Plus aria-hidden /> New collection
    </Button>
  );

  const pendingNames = pendingDelete ? namesOf(pendingDelete.collection) : [];
  const hideTargets = hiding ? [hiding] : (bulkHiding ?? []);

  return (
    <div aria-busy={deleting} inert={deleting ? true : undefined}>
      <CalmPage
        heading="page"
        title="Collections"
        subtitle="Server collections everyone can browse on each library's Collections tab and use in Sections."
        actions={
          narrow ? null : (
            <>
              {more}
              {newCollection}
            </>
          )
        }
        padBottom={narrow || selecting}
      >
        <CollectionApplyJobBanner job={latestApplyJob} />

        <div className="flex flex-wrap items-center gap-3">
          <div className="min-w-0 flex-1">
            <PillSwitcher
              label="Library"
              options={[
                {
                  value: ALL_LIBRARIES,
                  label: "All libraries",
                  icon: LibraryIcon,
                  // Arrange works on one library's shelves.
                  disabled: state.view === "arrange",
                },
                ...libraryList.map((library, index) => ({
                  value: String(library.id),
                  label: library.name,
                  count: libraryCounts.get(library.id) ?? 0,
                  separated: index === 0,
                })),
              ]}
              value={activeLibraryId ? String(activeLibraryId) : ALL_LIBRARIES}
              onChange={(value) =>
                update({ libraryId: value === ALL_LIBRARIES ? null : Number(value) })
              }
            />
          </div>
          <Segmented
            label="View"
            value={state.view}
            options={[
              { value: "list", label: "List", icon: List },
              { value: "arrange", label: "Arrange", icon: LayoutGrid },
            ]}
            onChange={setView}
          />
        </div>

        {state.view === "list" ? (
          <div className="contents" onKeyDown={handleSelectKeyDown}>
            <section aria-label="Collections" className="surface-panel rounded-[26px] p-1.5">
              {allCollections.isError ? (
                <div role="alert" className="grid justify-items-center gap-3 px-4 py-10 text-sm">
                  <p>Couldn&apos;t load collections</p>
                  <Button variant="outline" size="sm" onClick={() => void allCollections.refetch()}>
                    Retry
                  </Button>
                </div>
              ) : allCollections.isLoading ? (
                <ListSkeleton />
              ) : inLibrary.length === 0 ? (
                <EmptyLibrary
                  libraryName={activeLibrary?.name ?? null}
                  canAddStarterPack={canImport}
                  onAddStarterPack={() => setStarterPacksOpen(true)}
                  newCollection={newCollection}
                />
              ) : (
                <>
                  {selecting ? (
                    <SelectAllHeader
                      ref={selectAll}
                      count={listed.length}
                      selectedCount={selected.length}
                      limit={MAX_SELECTED_COLLECTIONS}
                      noun="collections"
                      onSelectAll={(checked) => {
                        setSelectedIds(new Set(checked ? listed.map((entry) => entry.id) : []));
                        selectionAnchor.current = null;
                      }}
                      onDone={exitSelectMode}
                    />
                  ) : (
                    <ListFilters
                      state={state}
                      total={inLibrary.length}
                      failedCount={failedCount}
                      onChange={(patch) => update(patch, { replace: true })}
                    />
                  )}
                  {listed.length === 0 ? (
                    <div className="grid justify-items-center gap-2 px-4 py-10 text-sm">
                      <p className="text-muted-foreground">No collections match.</p>
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() =>
                          update({ kind: "all", q: "", failed: false }, { replace: true })
                        }
                      >
                        Clear filters
                      </Button>
                    </div>
                  ) : (
                    <>
                      {selecting ? null : <CollectionColumnHeader count={listed.length} />}
                      <ol className="grid">
                        {listed.map((collection) => {
                          const libraries = librariesOf(collection);
                          const visible = isVisible(collection);
                          const syncing = syncingIds.has(collection.id);
                          const peekLibraryId = state.libraryId ?? libraries[0]?.id;
                          const rowPageChoices = rowPages(libraries, libraryList);
                          return (
                            <CollectionListItem
                              key={collection.id}
                              collection={collection}
                              libraryNames={libraries.map((library) => library.name)}
                              showLibraries={state.libraryId === null}
                              peek={
                                peekLibraryId
                                  ? serverCollectionPeek(collection, peekLibraryId, !visible)
                                  : null
                              }
                              visible={visible}
                              syncing={syncing}
                              // A sync moves the ETag a visibility change would send.
                              switchDisabled={visibilityOverrides.has(collection.id) || syncing}
                              onVisibleChange={(next) => changeVisible(collection, next)}
                              onOpen={() => openEditor(collection)}
                              selection={
                                selecting
                                  ? {
                                      selected: selectedIds.has(collection.id),
                                      label: `Select ${collection.title}`,
                                      onChange: (checked, extend) =>
                                        changeSelection(collection.id, checked, extend),
                                    }
                                  : undefined
                              }
                              menu={
                                // In select mode the bar holds the actions.
                                selecting ? null : (
                                  <CollectionActionsMenu
                                    placement="row"
                                    name={collection.title}
                                    onEdit={() => openEditor(collection)}
                                    openIn={{
                                      libraries,
                                      onOpen: (libraryId) =>
                                        navigate(
                                          buildLibraryCollectionCatalogHref(
                                            collection.id,
                                            collection.title,
                                            libraryId,
                                          ),
                                        ),
                                      disabledReason: visible
                                        ? undefined
                                        : "Hidden from Collections tabs",
                                    }}
                                    sync={
                                      canImport &&
                                      isListBackedCollectionType(collection.collection_type)
                                        ? {
                                            syncing:
                                              syncing || collection.last_sync_status === "running",
                                            onSync: () => syncNow(collection),
                                          }
                                        : undefined
                                    }
                                    addRow={{
                                      libraries: [
                                        ...rowPageChoices.bound,
                                        ...rowPageChoices.others,
                                      ],
                                      onAdd: (page) => navigate(addRowPath(collection.id, page)),
                                      disabledReason: visible ? undefined : SHOW_IT_FIRST,
                                    }}
                                    onDelete={() => void prepareDelete(collection)}
                                  />
                                )
                              }
                            />
                          );
                        })}
                      </ol>
                    </>
                  )}
                  {selecting ? null : (
                    <p className="text-muted-foreground flex items-center gap-2 px-4 pt-2 pb-3 text-[13px]">
                      <Info aria-hidden className="size-4 shrink-0" />
                      <span>
                        Click a collection to edit it. Order and shelves live in{" "}
                        <button
                          type="button"
                          className="text-foreground font-semibold underline-offset-4 hover:underline"
                          onClick={() => setView("arrange")}
                        >
                          Arrange
                        </button>
                        .
                      </span>
                    </p>
                  )}
                </>
              )}
            </section>
            {selecting ? (
              <SelectModeBar
                count={selected.length}
                limit={MAX_SELECTED_COLLECTIONS}
                noun="collections"
                // A row's own switch save or sync would move the ETags a bar action reads.
                busy={
                  batchRunning ||
                  deleting ||
                  visibilityOverrides.size > 0 ||
                  selected.some((collection) => syncingIds.has(collection.id))
                }
                note={
                  canImport
                    ? syncSkipNote(
                        selectedSmart,
                        selected.length - selectedLists.length - selectedSmart,
                      )
                    : null
                }
                actions={[
                  ...(canImport
                    ? [
                        {
                          key: "sync",
                          label: syncListsLabel(selectedLists.length),
                          icon: RefreshCw,
                          disabled: selectedLists.length === 0,
                          explainedByNote: true,
                          onClick: syncSelected,
                        },
                      ]
                    : []),
                  {
                    key: "show",
                    label: "Show on tabs",
                    icon: Eye,
                    onClick: () => void setSelectedVisible(true),
                  },
                  {
                    key: "hide",
                    label: "Hide from tabs",
                    icon: EyeOff,
                    onClick: () => void setSelectedVisible(false),
                  },
                  {
                    key: "delete",
                    label: "Delete…",
                    icon: Trash2,
                    destructive: true,
                    separated: true,
                    disabled: activeApplyJob,
                    onClick: () => void prepareBulkDelete(selected, false),
                  },
                ]}
              />
            ) : null}
          </div>
        ) : (
          <ArrangeView
            libraryId={arrangeLibraryId}
            libraryName={activeLibrary?.name ?? null}
            board={board}
            canAddStarterPack={canImport}
            newCollection={newCollection}
            onAddStarterPack={() => setStarterPacksOpen(true)}
            isVisible={isVisible}
            onEditCollection={(collection) => openEditor(collection, arrangeLibraryId)}
            onVisibleChange={changeVisible}
          />
        )}
      </CalmPage>

      {narrow && !selecting ? <MobileDockBar more={more} addRow={newCollection} /> : null}

      {pickerOpen ? (
        <NewCollectionPicker
          scope="server"
          libraryId={activeLibraryId}
          listHref={listHref}
          onClose={() => setPickerOpen(false)}
        />
      ) : null}

      {starterPacksOpen ? (
        <StarterPacksDialog
          libraries={libraryList}
          initialLibraryId={activeLibraryId}
          onClose={() => setStarterPacksOpen(false)}
        />
      ) : null}

      <DeleteCollectionDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open && !holdDeleteOpen.current) setPendingDelete(null);
        }}
        title={`Delete ${pendingDelete?.collection.title ?? "collection"}?`}
        libraryNames={pendingNames}
        rows={deleteRowsState}
        rowLibraryNames={libraryNames}
        isPending={checkingDelete || removeOne.isPending || deleteRows.isPending}
        error={pendingDelete?.error}
        onConfirm={() => void confirmDelete()}
      />

      <HideCollectionDialog
        open={hideTargets.length > 0}
        onOpenChange={(open) => {
          if (open) return;
          setHiding(null);
          setBulkHiding(null);
        }}
        name={hideTargets[0]?.title ?? ""}
        libraryNames={hideTargets[0] ? namesOf(hideTargets[0]) : []}
        rowCount={hideTargets.reduce((sum, collection) => sum + (collection.row_count ?? 0), 0)}
        count={hideTargets.length}
        onConfirm={() => {
          if (hiding) saveVisible(hiding, false);
          else if (bulkHiding) void saveSelectedVisible(bulkHiding, false);
        }}
      />

      <DeleteCollectionsDialog
        open={bulkDelete !== null}
        onOpenChange={(open) => {
          if (!open) setBulkDelete(null);
        }}
        count={bulkDelete?.snapshots.length ?? 0}
        kind={bulkDeleteKinds.size === 1 ? [...bulkDeleteKinds][0]! : null}
        // A collection goes from every library, so only Delete all names where.
        where={bulkDelete?.wholeView ? (activeLibrary?.name ?? "this view") : null}
        elsewhere={bulkDeleteElsewhere}
        kept={bulkDelete?.kept ?? []}
        blocked={activeApplyJob ? STARTER_PACK_BLOCKS_DELETE : null}
        onConfirm={confirmBulkDelete}
      />
    </div>
  );
}

/** Toggle buttons for a small set of choices, one pressed. */
function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: T;
  options: ReadonlyArray<{ value: T; label: string; icon?: typeof List }>;
  onChange: (value: T) => void;
}) {
  return (
    <div
      role="group"
      aria-label={label}
      className="border-border bg-muted/20 flex shrink-0 items-center gap-0.5 rounded-xl border p-[3px]"
    >
      {options.map((option) => {
        const pressed = option.value === value;
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={pressed}
            onClick={() => onChange(option.value)}
            className={cn(
              "focus-visible:ring-ring/50 inline-flex h-8 items-center gap-1.5 rounded-[9px] px-3 text-[13px] font-medium whitespace-nowrap outline-none focus-visible:ring-[3px] max-lg:h-11",
              pressed
                ? "bg-accent text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {option.icon ? <option.icon aria-hidden className="size-4" /> : null}
            {option.label}
          </button>
        );
      })}
    </div>
  );
}

/** Search, type and the failed-sync chip above the List. */
function ListFilters({
  state,
  total,
  failedCount,
  onChange,
}: {
  state: AdminListState;
  total: number;
  failedCount: number;
  onChange: (patch: Partial<AdminListState>) => void;
}) {
  return (
    <div className="border-border/75 flex flex-wrap items-center gap-2.5 border-b px-3 pt-2.5 pb-3">
      <div className="relative min-w-[200px] flex-1 sm:max-w-[280px]">
        <Search
          aria-hidden
          className="text-muted-foreground pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2"
        />
        <Input
          type="search"
          aria-label="Search collections"
          placeholder={`Search ${total} collection${total === 1 ? "" : "s"}`}
          value={state.q}
          onChange={(event) => onChange({ q: event.target.value })}
          className="h-9 rounded-xl pl-9"
        />
      </div>
      <Segmented
        label="Type"
        value={state.kind}
        options={KIND_OPTIONS}
        onChange={(kind) => onChange({ kind })}
      />
      {failedCount > 0 || state.failed ? (
        <button
          type="button"
          aria-pressed={state.failed}
          onClick={() => onChange({ failed: !state.failed })}
          className={cn(
            "focus-visible:ring-ring/50 ml-auto inline-flex h-8 items-center gap-2 rounded-full border px-3 text-[13px] font-medium outline-none focus-visible:ring-[3px] max-lg:h-11",
            state.failed
              ? "border-destructive bg-destructive/15 text-destructive"
              : "border-destructive/40 text-destructive hover:bg-destructive/10",
          )}
        >
          <span aria-hidden className="bg-destructive size-2 rounded-full" />
          {failedCount} sync{failedCount === 1 ? "" : "s"} failed
        </button>
      ) : null}
    </div>
  );
}

function ListSkeleton() {
  return (
    <div role="status" aria-busy="true" className="grid gap-1 p-2">
      <span className="sr-only">Loading collections…</span>
      {Array.from({ length: 5 }, (_, index) => (
        <div key={index} className="flex items-center gap-3.5 px-2 py-2.5">
          <Skeleton className="h-[50px] w-[74px] rounded-xl" />
          <div className="grid flex-1 gap-2">
            <Skeleton className="h-4 w-48" />
            <Skeleton className="h-3 w-64" />
          </div>
          <Skeleton className="h-5 w-9 rounded-full" />
        </div>
      ))}
    </div>
  );
}

/** A library, or the server, with no collections yet: both ways to make some. */
function EmptyLibrary({
  libraryName,
  canAddStarterPack,
  onAddStarterPack,
  newCollection,
}: {
  libraryName: string | null;
  canAddStarterPack: boolean;
  onAddStarterPack: () => void;
  newCollection: ReactNode;
}) {
  return (
    <div className="grid justify-items-center gap-3 px-4 py-14 text-center">
      <span className="bg-accent/80 ring-border grid size-12 place-items-center rounded-xl ring-1 ring-inset">
        <Layers aria-hidden className="text-muted-foreground size-5" />
      </span>
      <p className="font-semibold">
        {libraryName ? `No collections in ${libraryName} yet` : "No collections yet"}
      </p>
      <p className="text-muted-foreground max-w-sm text-sm">
        Make one yourself, or start from ready-made lists.
      </p>
      <div className="flex flex-wrap justify-center gap-2">
        <Button
          variant="outline"
          size="sm"
          disabled={!canAddStarterPack}
          onClick={onAddStarterPack}
        >
          <Layers3 aria-hidden /> Add a starter pack
        </Button>
        {newCollection}
      </div>
    </div>
  );
}

/** Arrange: one library's shelves, or the ways to make collections when it has none. */
function ArrangeView({
  libraryId,
  libraryName,
  board,
  canAddStarterPack,
  newCollection,
  onAddStarterPack,
  isVisible,
  onEditCollection,
  onVisibleChange,
}: {
  libraryId: number | null;
  libraryName: string | null;
  board: ReturnType<typeof useAdminCollectionsBoard>;
  canAddStarterPack: boolean;
  newCollection: ReactNode;
  onAddStarterPack: () => void;
  isVisible: (collection: LibraryCollection) => boolean;
  onEditCollection: (collection: LibraryCollection) => void;
  onVisibleChange: (collection: LibraryCollection, visible: boolean) => void;
}) {
  if (libraryId === null) return null;
  if (board.isError)
    return (
      <div
        role="alert"
        className="surface-panel grid justify-items-center gap-3 rounded-[26px] px-4 py-10 text-sm"
      >
        <p>Couldn&apos;t load shelves</p>
        <Button variant="outline" size="sm" onClick={() => void board.refetch()}>
          Retry
        </Button>
      </div>
    );
  if (board.isLoading || !board.data)
    return (
      <div role="status" aria-busy="true" className="grid gap-3">
        <span className="sr-only">Loading shelves…</span>
        {Array.from({ length: 3 }, (_, index) => (
          <Skeleton key={index} className="h-32 w-full rounded-[22px]" />
        ))}
      </div>
    );
  const count =
    board.data.ungrouped.length +
    board.data.groups.reduce((sum, group) => sum + group.collections.length, 0);
  const hasShelves = board.data.groups.some((group) => group.kind === "regular");
  if (count === 0 && !hasShelves)
    return (
      <div className="surface-panel rounded-[26px] p-1.5">
        <EmptyLibrary
          libraryName={libraryName}
          canAddStarterPack={canAddStarterPack}
          onAddStarterPack={onAddStarterPack}
          newCollection={newCollection}
        />
      </div>
    );
  return (
    <GroupsBoard
      libraryID={libraryId}
      libraryName={libraryName ?? ""}
      groups={board.data.groups}
      ungrouped={board.data.ungrouped}
      ungroupedSortOrder={board.data.ungroupedSortOrder}
      isVisible={isVisible}
      onEditCollection={onEditCollection}
      onVisibleChange={onVisibleChange}
    />
  );
}

function CollectionApplyJobBanner({ job }: { job: AdminJob | null }) {
  const result = job?.status === "completed" ? templateResultOf(job) : null;
  // Read the packs only for a finished job, to name it and count what it shows.
  const bundles = useCollectionTemplateBundles(result !== null);
  const pack = result
    ? starterPacksOf(bundles.data?.bundles ?? []).find((entry) => entry.id === result.bundle_id)
    : undefined;

  if (!job || job.job_type !== "template_bundle_apply") {
    return null;
  }

  const active = isActiveTemplateBundleApplyJob(job);
  const recent = active || isRecentTemplateBundleApplyJob(job);
  if (!recent) {
    return null;
  }

  if (job.status === "failed") {
    return (
      <div className="border-destructive/30 bg-destructive/5 text-destructive rounded-lg border px-4 py-3">
        <div className="flex items-start gap-3">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
          <div className="min-w-0 space-y-1">
            <p className="text-sm font-medium">Couldn't add the starter pack</p>
            <p className="text-xs">{job.error_message || job.message || "The job failed."}</p>
          </div>
        </div>
      </div>
    );
  }

  if (job.status === "completed") {
    // Before the packs load, fall back to the raw result: it can't name the pack.
    let added = true;
    if (result && pack) {
      added = packAdded(result, pack);
    } else if (result) {
      added =
        result.created.length > 0 ||
        (result.failed.length === 0 && result.featured_failed.length === 0);
    }
    const Icon = added ? CheckCircle2 : AlertCircle;
    return (
      <div className="border-border bg-muted/30 rounded-lg border px-4 py-3">
        <div className="flex items-start gap-3">
          <Icon
            aria-hidden
            className={cn("mt-0.5 h-4 w-4 shrink-0", added ? "text-emerald-500" : "text-amber-500")}
          />
          <div className="min-w-0 space-y-1">
            <p className="text-sm font-medium">
              {packResultHeading(pack?.title ?? "Starter pack", true, added)}
            </p>
            {result && pack ? (
              <p className="text-muted-foreground text-xs">{packResultSummary(result, pack)}</p>
            ) : null}
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="border-border bg-muted/30 rounded-lg border px-4 py-3">
      <div className="flex items-start gap-3">
        <Loader2 className="text-muted-foreground mt-0.5 h-4 w-4 shrink-0 animate-spin" />
        <div className="min-w-0 flex-1 space-y-2">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-sm font-medium">Adding a starter pack</p>
            <p className="text-muted-foreground text-xs">{job.message || "Working..."}</p>
          </div>
          <div className="progress-bar">
            <div className="progress-fill animate-pulse" style={{ width: "40%" }} />
          </div>
        </div>
      </div>
    </div>
  );
}

function isActiveTemplateBundleApplyJob(job: AdminJob) {
  return job.status === "queued" || job.status === "running";
}

function isRecentTemplateBundleApplyJob(job: AdminJob) {
  const timestamp = job.completed_at ?? job.requested_at;
  const parsed = Date.parse(timestamp);
  if (Number.isNaN(parsed)) {
    return false;
  }
  return Date.now() - parsed < 10 * 60_000;
}

/** A finished starter pack job's result, when it carries one. */
function templateResultOf(job: AdminJob): ApplyCollectionTemplateBundleResponse | null {
  const payload = job.result_payload as Partial<ApplyCollectionTemplateBundleResponse> | undefined;
  const lists = [payload?.created, payload?.failed, payload?.featured, payload?.featured_failed];
  return lists.every(Array.isArray) ? (payload as ApplyCollectionTemplateBundleResponse) : null;
}
