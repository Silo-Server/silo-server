import { useEffect, useState, type ReactNode } from "react";
import { useLocation, useNavigate } from "react-router";
import { useQueries, useQuery } from "@tanstack/react-query";
import { AlertTriangle } from "lucide-react";
import { toast } from "sonner";

import { ConfirmDialog } from "@/components/ConfirmDialog";
import { SaveBar } from "@/components/SaveBar";
import { UnsavedChangesGuard } from "@/components/UnsavedChangesGuard";
import {
  useAdminCollectionRows,
  useDeleteCollectionRows,
} from "@/hooks/queries/admin/collectionRows";
import { useAdminCollectionCapabilities } from "@/hooks/queries/admin/collections";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { createCatalogSearchState, fetchCatalogItems } from "@/hooks/queries/catalog";
import {
  useCollectionDraft,
  useListedPoster,
  useScopeDelete,
  useScopePreview,
  useScopeSync,
  type ScopePreview,
} from "@/hooks/queries/collectionScope";
import { useCollectionCapabilities } from "@/hooks/queries/collections";
import { catalogKeys } from "@/hooks/queries/keys";
import { useUserLibraries } from "@/hooks/queries/libraries";
import { useProfiles } from "@/hooks/queries/profiles";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useHasUnsavedChanges, useReportUnsavedChanges } from "@/hooks/useUnsavedChanges";
import {
  CHANGED_BEFORE_DELETE,
  CHECK_BEFORE_DELETE_FAILED,
  DRAFT_FIELD_LABEL,
  NAME_FILLED_HELP,
  NAME_IT_THEN_CREATE,
  NOT_CREATED_YET,
  PICK_A_LIBRARY,
  PICK_A_LIST_FIRST,
  PICK_LIBRARIES_FIRST,
  PREVIEW_EVERY_LIBRARY,
  PREVIEW_SHOWS_UNSAVED,
  SAVE_AFTER_CONFLICTS,
  SAVE_FAILED,
  SAVE_NOT_READY,
  SHOW_IT_FIRST,
  DISCARD_KEEPS_IT_HIDDEN,
  SMART_UPDATES_ITSELF,
  SYNCED_CREATE_SUBTITLE,
  SYNCS_ON_CREATE,
  TITLES_ALREADY_SAVED,
  firstSyncMessage,
  joinNames,
  keptMessage,
  notSavedMessage,
  personalDeleteDescription,
  rowsLeftMessage,
  saveFirstDescription,
  smartCreateHint,
  titlesReadyToAdd,
} from "@/lib/collections/copy";
import { changedFields, draftRules, type DraftField } from "@/lib/collections/draft";
import { listReturnState, useListReturnPath } from "@/lib/collections/listReturn";
import {
  addRowPath,
  addToMyHomePath,
  onRowsLine,
  rowPages,
  rowLabels,
  rowPlaceInSentence,
  rowPlaces,
  type CollectionRow,
  type RowsState,
} from "@/lib/collections/rows";
import type {
  CollectionDraft,
  CollectionScope,
  CreateKind,
  EditorSnapshot,
  WireCollection,
} from "@/lib/collections/scope";
import { savedListProblem, type SyncedDraft, type SyncedTab } from "@/lib/collections/synced";
import { SYNCED_SOURCE_LABEL } from "@/lib/collections/types";
import type { AddedRowState } from "@/lib/homeRows/rowLinks";
import type { PageRef } from "@/lib/homeRows/types";
import { buildLibraryCollectionCatalogHref } from "@/pages/catalogSearchParams";

import { DeleteCollectionDialog } from "../DeleteCollectionDialog";
import { LibrariesLine } from "../fields/LibrariesLine";
import { focusLibrariesLine } from "../fields/librariesLineFocus";
import { CollectionEditorShell } from "./CollectionEditorShell";
import { CollectionMetaLine } from "./CollectionMetaLine";
import { ConflictBanner } from "./ConflictBanner";
import { DetailsPanel } from "./DetailsPanel";
import { EditorHeader, type OpenTarget } from "./EditorHeader";
import { CollectionPreviewPane } from "./CollectionPreviewPane";
import { LookPanel } from "./LookPanel";
import { ManualContentsPanel } from "./ManualContentsPanel";
import { PersonalRowsThatShowIt } from "./PersonalRowsThatShowIt";
import { RowsOnceCreated, RowsThatShowIt } from "./RowsThatShowIt";
import { SaveFirstDialog } from "./SaveFirstDialog";
import { SmartRulesPanel } from "./SmartRulesPanel";
import { SyncedListPanel, type SyncedListPanelProps } from "./SyncedListPanel";
import { useAddedRowHighlight } from "./useAddedRowHighlight";
import { WhereItShowsPanel, type HideConfirm } from "./WhereItShowsPanel";

const NO_TITLES: readonly string[] = [];
/** The fields the live preview already reflects before they are saved. */
const PREVIEWED: ReadonlySet<DraftField> = new Set(["rules", "libraryIds", "rawSortConfig"]);
/** The fields a sync reads; Sync now runs the saved copy, so it waits while they're unsaved. */
const SYNC_INPUTS: ReadonlySet<DraftField> = new Set(["list", "libraryIds", "limit"]);
const touchesSync = (fields: readonly DraftField[]) =>
  fields.some((field) => SYNC_INPUTS.has(field));

/** Every title a server collection shows in one library, a page at a time. */
async function collectionTitlesIn(collectionId: string, libraryId: number, signal: AbortSignal) {
  const state = createCatalogSearchState("library_collection", {
    collection_id: collectionId,
    library_id: libraryId,
    uses_source_order: true,
  });
  return fetchCatalogItems(state, { signal });
}

/**
 * "3 titles are only in Kids: …" when unticking libraries would drop titles a
 * server collection holds. Reads each saved library's titles only while an
 * untick is pending, all of them, since a title past the first page of a kept
 * library still shows.
 */
function useUntickWarning(
  collectionId: string | undefined,
  saved: readonly number[],
  chosen: readonly number[],
  libraries: ReadonlyArray<{ id: number; name: string }>,
) {
  const removed = saved.filter((id) => !chosen.includes(id));
  const pending = Boolean(collectionId) && removed.length > 0;
  const titlesIn = useQueries({
    queries: saved.map((libraryId) => ({
      queryKey: [...catalogKeys.all, "collectionTitlesIn", collectionId, libraryId],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        collectionTitlesIn(collectionId!, libraryId, signal),
      enabled: pending,
      staleTime: 60 * 1000,
    })),
  });
  // A kept library not read yet would make every title read as only in the others.
  if (!pending || titlesIn.some((titles) => !titles.data)) return null;
  const kept = new Set(
    saved
      .flatMap((libraryId, index) => (chosen.includes(libraryId) ? [titlesIn[index]] : []))
      .flatMap((titles) => titles?.data?.map((item) => item.content_id) ?? []),
  );
  const onlyRemoved = new Map<string, string>();
  saved.forEach((libraryId, index) => {
    if (chosen.includes(libraryId)) return;
    for (const item of titlesIn[index]?.data ?? []) {
      if (!kept.has(item.content_id)) onlyRemoved.set(item.content_id, item.title);
    }
  });
  if (onlyRemoved.size === 0) return null;
  const names = removed.map((id) => libraries.find((library) => library.id === id)?.name ?? "");
  const titles = [...onlyRemoved.values()];
  const shown = titles.slice(0, 3).join(", ") + (titles.length > 3 ? "…" : "");
  const count = `${titles.length} title${titles.length === 1 ? " is" : "s are"}`;
  return `${count} only in ${joinNames(names.filter(Boolean))}: ${shown}. They'll stop showing when you save.`;
}

/** A Smart collection's rules and, under them, their live preview. */
function SmartContents<Raw extends WireCollection>({
  scope,
  draft,
  onChange,
  libraries,
  preview,
}: {
  scope: CollectionScope<Raw>;
  draft: CollectionDraft;
  onChange: (update: (draft: CollectionDraft) => CollectionDraft) => void;
  libraries: Array<{ id: number; name: string }>;
  preview: ScopePreview;
}) {
  // A server collection with no library yet still previews, across every library, so
  // the rules can be tried before picking where it shows.
  const needsLibraries = scope.requireLibraries && draft.libraryIds.length === 0;
  return (
    <>
      <SmartRulesPanel
        scopeKind={scope.kind}
        draft={draft}
        onChange={onChange}
        libraries={libraries}
      />
      <CollectionPreviewPane
        preview={preview}
        note={needsLibraries ? PREVIEW_EVERY_LIBRARY : undefined}
      />
    </>
  );
}

/** A personal Smart collection picks from the libraries the profile can see. */
function PersonalSmartContents<Raw extends WireCollection>(
  props: Omit<Parameters<typeof SmartContents<Raw>>[0], "libraries">,
) {
  const { data = [] } = useUserLibraries();
  return <SmartContents {...props} libraries={data} />;
}

type WithoutLibraries<Props> = Props extends unknown ? Omit<Props, "libraries"> : never;

/** A personal Synced list matches into the libraries the profile can see. */
function PersonalSyncedContents(props: WithoutLibraries<SyncedListPanelProps>) {
  const { data = [] } = useUserLibraries();
  return <SyncedListPanel {...({ ...props, libraries: data } as SyncedListPanelProps)} />;
}

/** Under Name on a new Synced list: whether the pick filled it, or kept what was typed. */
function syncedNameNote(draft: CollectionDraft): string | undefined {
  const synced = draft.synced;
  if (!synced?.list) return undefined;
  const kept = keptMessage(synced.kept);
  if (kept) return kept;
  return draft.name !== "" && draft.name === synced.filled.name ? NAME_FILLED_HELP : undefined;
}

/** Whether anything in a new synced list's step was picked or typed. */
function syncedTouched(synced: SyncedDraft | undefined) {
  return Boolean(
    synced &&
    (synced.list ||
      synced.mdblistLink ||
      synced.tmdbListLink ||
      synced.limit !== undefined ||
      synced.schedule),
  );
}

/**
 * The editor page for every collection type, both scopes, create and edit.
 * A Manual or Smart collection's instance carries on from `/new` to
 * `/:id/edit` after Create; a new Synced list moves to its own edit page,
 * which opens it fresh with its first sync's state.
 */
export function CollectionEditor<Raw extends WireCollection>({
  scope,
  kind: createKind = "manual",
  snapshot,
  libraryId,
  syncedTab,
  onCreated,
}: {
  scope: CollectionScope<Raw>;
  /** Create mode: what to create. A saved collection keeps its own kind. */
  kind?: CreateKind;
  snapshot?: EditorSnapshot<Raw>;
  /** Create mode: the library the editor was opened from. */
  libraryId?: number | null;
  /** Create mode, Synced list: the tab the list step opens on. */
  syncedTab?: SyncedTab;
  /** Create mode, Manual and Smart: told the new id before the page moves to its edit URL. */
  onCreated?: (id: string) => void;
}) {
  const navigate = useNavigate();
  const kind = snapshot ? snapshot.view.kind : createKind;
  const smart = kind === "smart";
  const synced = kind === "synced";
  // A new Synced list leaves for its edit page once it exists.
  const newList = synced && !snapshot;
  const editor = useCollectionDraft(scope, { snapshot, kind, libraryId });
  const { draft, view } = editor;
  // Smart: the live preview under the rules, whose count the save bar repeats before Create.
  const preview = useScopePreview(scope, draftRules(draft), { enabled: smart });
  const created = Boolean(editor.id) && !newList;
  const isServer = scope.kind === "server";
  useDocumentTitle(created ? `Edit ${view?.name ?? draft.name}` : "New collection");
  const { data: adminLibraries = [] } = useAdminLibraries({ enabled: isServer });
  const { profile } = useCurrentProfile();
  const { data: profiles = [] } = useProfiles({ enabled: !isServer });
  const personalCapabilities = useCollectionCapabilities();
  const adminCapabilities = useAdminCollectionCapabilities(isServer);
  const capabilities = isServer ? adminCapabilities.data : personalCapabilities.data;
  const hasUnsaved = useHasUnsavedChanges();
  const [leaving, setLeaving] = useState<string | null>(null);
  // A trip to Home rows (Add as a row), from which the editor expects to be handed back.
  const [detour, setDetour] = useState<string | null>(null);
  // Save and continue saved: the trip to take once that save has settled.
  const [afterSave, setAfterSave] = useState<string | null>(null);
  const [openEdit, setOpenEdit] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const listPath = useListReturnPath(
    scope.paths.list({ libraryId: isServer ? (draft.libraryIds[0] ?? null) : null }),
  );
  const remove = useScopeDelete(scope, {
    onDeleted: () => setLeaving(listPath),
    // The editor keeps its own copy: read it so the next Delete sends the new token.
    onStale: reread,
  });
  const syncList = useScopeSync(scope);
  // How many titles the last sync run here skipped, and the draft it ran
  // with; the collection doesn't carry the count.
  const [lastRun, setLastRun] = useState<{ skipped: number; draft: CollectionDraft }>();
  // A count from other settings than the ones on screen would read as theirs.
  const skipped =
    lastRun && !touchesSync(changedFields(lastRun.draft, draft)) ? lastRun.skipped : undefined;
  // Reading the collection again after Sync now, for its status and token.
  const [rereading, setRereading] = useState(false);
  // The server records a sync's status only when the run ends, and Sync now
  // answers once it has: the request is the only sync this page can see. It
  // counts as running until the read after it brings the new token.
  const syncing = syncList.isPending || rereading;
  // Spec §3.1: only server lists offer Sync now here; a profile syncs its
  // lists from their cards on the Collections page. Sync answers 501 when the
  // server can't import, so it needs the capability.
  const canSync = created && isServer && Boolean(view?.source) && capabilities?.imports === true;
  const saveFirst = touchesSync(editor.changed);
  // Discards put the list's source card back.
  const [discards, setDiscards] = useState(0);

  /** Reads the collection again; a failed read keeps the old token, and Save's 412 merges. */
  function reread() {
    return editor.syncWithServer().catch(() => {});
  }

  function syncNow() {
    if (!editor.id || syncing || saveFirst) return;
    const ran = draft;
    syncList.mutate(editor.id, {
      onSuccess: (run) => setLastRun({ skipped: run.itemsUnmatched, draft: ran }),
      // The last run counted nothing; an earlier run's count would read as its.
      onError: () => setLastRun(undefined),
      // A sync records its run on the collection, even one that fails: read
      // it for the status and so Save and Delete send the new token.
      onSettled: () => {
        setRereading(true);
        void reread().finally(() => setRereading(false));
      },
    });
  }

  const staged = draft.stagedItems ?? NO_TITLES;
  // After Create the page moves to the collection's edit URL. Until it gets
  // there it reports clean, so the guard never asks; a poster that failed to
  // upload then counts as unsaved again.
  const location = useLocation();
  const moving = openEdit !== null && location.pathname !== scope.paths.edit(openEdit);
  const dirty = editor.isDirty || (!created && (staged.length > 0 || syncedTouched(draft.synced)));
  useReportUnsavedChanges(!leaving && !detour && !moving && dirty);

  // Leave only once the clean report has reached the guard.
  useEffect(() => {
    if (hasUnsaved) return;
    if (leaving) {
      navigate(leaving, { replace: true });
    } else if (detour) {
      // Carry the list the editor was opened from, so it can go back there afterwards.
      navigate(detour, { state: listReturnState(listPath) });
    } else if (moving && openEdit) {
      // Keep the list the editor was opened from, so Back and Delete still return to it.
      navigate(scope.paths.edit(openEdit, { libraryId }), {
        replace: true,
        state: listReturnState(listPath),
      });
      // Create unmounts its button; carry on where the contents start.
      if (smart) focusLibrariesLine();
      else if (!newList) document.querySelector<HTMLInputElement>("[data-title-search]")?.focus();
    }
  }, [
    hasUnsaved,
    leaving,
    detour,
    libraryId,
    listPath,
    moving,
    navigate,
    newList,
    openEdit,
    scope,
    smart,
  ]);

  // Something the save couldn't keep (artwork that failed to upload, an edit
  // made while saving) stays here with its message rather than being dropped.
  if (afterSave && !editor.isSaving) {
    setAfterSave(null);
    if (!editor.isDirty) setDetour(afterSave);
  }

  const libraryOptions = adminLibraries.map(({ id, name, type }) => ({ id, name, type }));
  const named = (ids: readonly number[]) =>
    ids.flatMap((id) => {
      const library = libraryOptions.find((entry) => entry.id === id);
      return library ? [{ id, name: library.name }] : [];
    });
  const chosenLibraries = named(draft.libraryIds);
  const savedLibraries = named(view?.libraryIds ?? []);
  const untickWarning = useUntickWarning(
    isServer && kind === "manual" ? editor.id : undefined,
    editor.base.libraryIds,
    draft.libraryIds,
    libraryOptions,
  );
  const otherProfileNames = isServer
    ? []
    : profiles.filter((entry) => entry.id !== profile?.id).map((entry) => entry.name);

  // The admin Home and library page rows that show a server collection.
  const libraryNames = new Map(adminLibraries.map((library) => [library.id, library.name]));
  const rowsReported = isServer && adminCapabilities.data?.section_references === true;
  const rowsQuery = useAdminCollectionRows(created ? editor.id : undefined, rowsReported);
  const savedRows = rowsQuery.data;
  let rowsState: RowsState | null = null;
  if (rowsReported) {
    if (!created) rowsState = { status: "ready", rows: [] };
    else if (savedRows) rowsState = { status: "ready", rows: savedRows };
    else if (rowsQuery.isError)
      rowsState = { status: "error", onRetry: () => void rowsQuery.refetch() };
    else rowsState = { status: "loading" };
  }

  // Until the rows load (or when they don't), the list's count of them still
  // warns before a hide. The editor page reads that list already.
  const { data: listedRowCount = 0 } = useQuery({
    queryKey: scope.keys.list,
    queryFn: () => scope.fetchList(),
    staleTime: Infinity,
    enabled: isServer && created,
    select: (data) =>
      (data.collections as Array<{ id: string; row_count?: number }>).find(
        (entry) => entry.id === editor.id,
      )?.row_count ?? 0,
  });
  let hideConfirm: HideConfirm | null = null;
  if (savedRows) {
    if (savedRows.length > 0)
      hideConfirm = { rowCount: savedRows.length, places: rowPlaces(savedRows, libraryNames) };
  } else if (isServer && listedRowCount > 0) {
    hideConfirm = { rowCount: listedRowCount, places: null };
  }

  // Back from Add row in Home rows (a personal collection: Settings > Home
  // Screen): the new row flashes once it's listed.
  const addedRow = (location.state as Partial<AddedRowState> | null)?.addedRow;
  const addedRowId =
    addedRow?.surface === (isServer ? "admin" : "profile") ? addedRow.id : undefined;
  const highlightId = useAddedRowHighlight(isServer ? addedRowId : undefined, savedRows);
  // A personal collection's rows are read in Where it shows; it hands the header its line.
  const [personalOnRows, setPersonalOnRows] = useState<string | null>(null);

  // Add as a row: Home rows on that page, coming back here. Unsaved changes are asked about first.
  const [savingFirst, setSavingFirst] = useState<{ where: string; path: string } | null>(null);
  function addAsRow(page: PageRef, where = rowPlaceInSentence(page, libraryNames)) {
    if (!editor.id) return;
    const editLibraryId = Number(new URLSearchParams(location.search).get("libraryId")) || null;
    const returnTo = scope.paths.edit(editor.id, { libraryId: libraryId ?? editLibraryId });
    const path = isServer
      ? addRowPath(editor.id, page, returnTo)
      : addToMyHomePath({ source: "user", id: editor.id }, page, returnTo);
    if (editor.isDirty) setSavingFirst({ where, path });
    else setDetour(path);
  }
  async function saveAndContinue() {
    if (!savingFirst || saveBlockedReason) return;
    const saved = await editor.save();
    setSavingFirst(null);
    if (saved) setAfterSave(savingFirst.path);
  }
  // Only a hidden server collection waits to be shown: a personal row's See all
  // opens however its Collections tab switch is set.
  const addRowBlocked = draft.server?.visibility === "hidden" ? SHOW_IT_FIRST : null;
  // Hidden as saved: discarding keeps it hidden, so only saving lets Home rows add it.
  const discardKeepsHidden = view?.server?.visibility === "hidden";

  // Delete: rows that show it go first, each with a fresh token, then the collection.
  const deleteRows = useDeleteCollectionRows();
  // The rows the delete is working through, so the dialog doesn't change under it.
  const [deletingRows, setDeletingRows] = useState<readonly CollectionRow[] | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  // Reading the collection's token before any row goes.
  const [checkingDelete, setCheckingDelete] = useState(false);
  function closeDelete() {
    setConfirmDelete(false);
    setDeletingRows(null);
    setDeleteError(null);
  }
  // When the rows don't load but the list counts none, the plain confirm stays
  // usable: the server still refuses with a 409 if one appeared.
  const deleteRowsState = rowsState?.status === "error" && listedRowCount === 0 ? null : rowsState;
  async function deleteServerCollection() {
    if (!view || !editor.etag) return;
    const rows = deletingRows ?? savedRows ?? [];
    setDeleteError(null);
    if (rows.length > 0) {
      // A deleted row can't come back: check the collection's token first, so
      // a Delete that would answer 412 deletes nothing.
      setCheckingDelete(true);
      const current = await scope.fetchSnapshot(view.id).then(
        (snapshot) => snapshot.etag,
        () => null,
      );
      // Changed: read it again, so the next Delete sends the new token.
      if (current && current !== editor.etag) await reread();
      setCheckingDelete(false);
      if (current !== editor.etag) {
        setDeleteError(current ? CHANGED_BEFORE_DELETE : CHECK_BEFORE_DELETE_FAILED);
        return;
      }
      setDeletingRows(rows);
      const { remaining } = await deleteRows
        .mutateAsync({ collectionId: view.id, rows })
        .catch(() => ({ remaining: [...rows] }));
      if (remaining.length > 0) {
        setDeletingRows(remaining);
        setDeleteError(rowsLeftMessage(rowLabels(remaining, libraryNames)));
        return;
      }
    }
    remove.mutate(
      { id: view.id, etag: editor.etag },
      // Read the rows again, so the dialog shows what still stands in the way.
      // refetch() ignores `enabled`, so ask only a server that reports rows.
      {
        onError: () => {
          setDeletingRows(null);
          if (rowsReported) void rowsQuery.refetch();
        },
      },
    );
  }

  // Open ▾: a server collection opens in each of its libraries; a personal one has one page.
  let open: OpenTarget[] = [];
  if (view && created) {
    open = isServer
      ? savedLibraries.map((library) => ({
          label: library.name,
          href: buildLibraryCollectionCatalogHref(view.id, view.name, library.id),
        }))
      : [{ label: "Open", href: scope.paths.browse(view) }];
  }

  async function create() {
    const result = await editor.create();
    if (!result) return;
    if (newList) {
      const { tone, text } = firstSyncMessage(result.sync);
      const description = result.warnings.length > 0 ? result.warnings.join(" ") : undefined;
      if (tone === "warning" || description) toast.warning(text, { description });
      else toast.success(text);
    } else {
      onCreated?.(result.id);
    }
    setOpenEdit(result.id);
  }

  const needsLibraries = scope.requireLibraries && draft.libraryIds.length === 0;
  const needsList = newList && !draft.synced?.list;
  // A saved list's changed source must be one it can follow.
  const listProblem =
    draft.list && editor.changed.includes("list") ? savedListProblem(draft.list) : null;
  // A created Synced list keeps this bar until it moves to its edit page; one Create is enough.
  const canCreate = !editor.id && draft.name.trim() !== "" && !needsLibraries && !needsList;
  const pending = editor.pendingLabels;
  // What the save bar adds after the pending fields.
  let afterPending: string | null = kind === "manual" ? TITLES_ALREADY_SAVED : listProblem;
  if (smart) {
    afterPending = editor.changed.some((field) => PREVIEWED.has(field))
      ? PREVIEW_SHOWS_UNSAVED
      : null;
  }
  if (needsLibraries && !needsList) afterPending = PICK_A_LIBRARY;
  // Before Create: what's still missing, in the order the page asks for it.
  let createHint: string | null = null;
  if (needsList) createHint = PICK_A_LIST_FIRST;
  else if (draft.name.trim() === "") createHint = NAME_IT_THEN_CREATE;
  else if (needsLibraries) createHint = PICK_LIBRARIES_FIRST;
  else if (newList) createHint = SYNCS_ON_CREATE;
  else if (smart && preview.status === "ready") createHint = smartCreateHint(preview.total);
  else if (staged.length > 0) createHint = titlesReadyToAdd(staged.length);
  // Save and Save and continue wait for the same things.
  let saveBlockedReason: string | null = null;
  if (editor.conflicts.length > 0) saveBlockedReason = SAVE_AFTER_CONFLICTS;
  else if (draft.name.trim() === "" || needsLibraries || listProblem)
    saveBlockedReason = SAVE_NOT_READY;
  const saveBar = created ? (
    <SaveBar
      placement="page"
      className="max-w-3xl"
      dirtyCount={pending.length}
      visible={editor.isDirty || Boolean(editor.saveError)}
      isSaving={editor.isSaving}
      saveLabel={editor.saveError ? "Try again" : "Save"}
      canSave={!saveBlockedReason}
      onSave={() => void editor.save()}
      onDiscard={() => {
        editor.discard();
        setDiscards((count) => count + 1);
      }}
      message={
        editor.saveError ? (
          `${SAVE_FAILED} · ${editor.saveError}`
        ) : (
          <>
            {notSavedMessage(pending)}{" "}
            {afterPending ? (
              <span className="text-muted-foreground ml-3 font-normal">{afterPending}</span>
            ) : null}
          </>
        )
      }
    />
  ) : (
    <SaveBar
      placement="page"
      className="max-w-3xl"
      dirtyCount={0}
      visible
      tone="idle"
      isSaving={editor.isSaving}
      saveLabel="Create collection"
      discardLabel="Cancel"
      canSave={canCreate}
      onSave={() => void create()}
      onDiscard={() => navigate(listPath)}
      message={
        editor.saveError ? (
          `${SAVE_FAILED} · ${editor.saveError}`
        ) : (
          <>
            {NOT_CREATED_YET} {/* A phone's bar has room for the state and the buttons only. */}
            {createHint ? (
              <span className="text-muted-foreground ml-3 font-normal max-sm:sr-only">
                {createHint}
              </span>
            ) : null}
          </>
        )
      }
    />
  );

  const artworkSlots = capabilities?.artwork === false ? [] : scope.artworkSlots;
  // Without its own poster a collection shows a collage of its titles, except
  // a server Smart collection, whose titles come from its rules.
  const collages = isServer ? !smart : personalCapabilities.data?.poster_collages === true;
  const poster = useListedPoster(scope, view, {
    awaitCollage: created && collages && artworkSlots.length > 0,
    version: editor.etag,
  });
  // A new Synced list starts with its pick's poster.
  const posterUrl = poster.url ?? draft.synced?.posterUrl;

  let rowsThatShowIt: ReactNode;
  if (!isServer) {
    rowsThatShowIt = (
      <PersonalRowsThatShowIt
        collectionId={created ? editor.id : undefined}
        draftLibraryIds={draft.libraryIds}
        addedRowId={addedRowId}
        disabledReason={addRowBlocked}
        onAdd={addAsRow}
        onLineChange={setPersonalOnRows}
      />
    );
  } else if (created) {
    rowsThatShowIt = (
      <RowsThatShowIt
        rows={rowsState}
        libraryNames={libraryNames}
        highlightId={highlightId}
        addAsRow={{
          ...rowPages(chosenLibraries, libraryOptions),
          disabledReason: addRowBlocked,
          onPick: addAsRow,
        }}
      />
    );
  } else {
    rowsThatShowIt = <RowsOnceCreated />;
  }

  // The meta line ends with what keeps the collection filled: its rules or its list.
  let metaExtra: string | undefined;
  if (smart) metaExtra = SMART_UPDATES_ITSELF;
  else if (view?.source) metaExtra = SYNCED_SOURCE_LABEL[view.source];

  let contents: ReactNode;
  let panel: WithoutLibraries<SyncedListPanelProps> | null = null;
  const common = { scopeKind: scope.kind, onChange: editor.setDraft, capabilities };
  if (newList && draft.synced) {
    panel = { ...common, draft: { ...draft, synced: draft.synced }, initialTab: syncedTab };
  } else if (synced && draft.list && view) {
    panel = {
      ...common,
      draft: { ...draft, list: draft.list },
      saved: {
        view,
        syncing,
        skipped,
        saveFirst,
        onSyncNow: canSync ? syncNow : undefined,
        discards,
      },
    };
  }
  if (panel) {
    contents = isServer ? (
      <SyncedListPanel {...({ ...panel, libraries: libraryOptions } as SyncedListPanelProps)} />
    ) : (
      <PersonalSyncedContents {...panel} />
    );
  } else if (smart && isServer) {
    contents = (
      <SmartContents
        scope={scope}
        draft={draft}
        onChange={editor.setDraft}
        libraries={libraryOptions}
        preview={preview}
      />
    );
  } else if (smart) {
    contents = (
      <PersonalSmartContents
        scope={scope}
        draft={draft}
        onChange={editor.setDraft}
        preview={preview}
      />
    );
  } else {
    contents = (
      <ManualContentsPanel
        scope={scope}
        collectionId={editor.id}
        searchLibraries={isServer ? chosenLibraries : []}
        librariesLine={
          isServer ? (
            <LibrariesLine
              lead="Titles from"
              libraries={libraryOptions}
              value={draft.libraryIds}
              onChange={(libraryIds) => editor.setDraft((next) => ({ ...next, libraryIds }))}
              warning={
                untickWarning ? (
                  <p
                    role="note"
                    className="border-warning/50 bg-warning/10 flex items-start gap-2.5 rounded-xl border px-3 py-2.5 text-[13px]"
                  >
                    <AlertTriangle aria-hidden className="text-warning mt-0.5 size-4 shrink-0" />
                    {untickWarning}
                  </p>
                ) : null
              }
            />
          ) : null
        }
        staged={staged}
        onStagedChange={(stagedItems) => editor.setDraft((next) => ({ ...next, stagedItems }))}
        onRetryStaged={(position) => void editor.retryStagedItems(position)}
        itemCount={view?.itemCount}
        onItemsChanged={reread}
      />
    );
  }

  return (
    <>
      <UnsavedChangesGuard />
      <CollectionEditorShell
        header={
          <EditorHeader
            back={{ label: "Collections", href: listPath }}
            kind={kind}
            name={view?.name ?? draft.name}
            created={created}
            shared={view?.personal?.shared}
            posterUrl={posterUrl}
            meta={
              newList ? (
                <p className="text-muted-foreground text-[14px]">{SYNCED_CREATE_SUBTITLE}</p>
              ) : view ? (
                <CollectionMetaLine
                  libraryNames={
                    isServer ? savedLibraries.map((library) => library.name) : undefined
                  }
                  itemCount={view.itemCount}
                  extra={[
                    metaExtra,
                    isServer && savedRows ? onRowsLine(savedRows, libraryNames) : personalOnRows,
                  ].filter((part): part is string => Boolean(part))}
                />
              ) : null
            }
            open={open}
            sync={canSync ? { syncing, saveFirst, onSyncNow: syncNow } : undefined}
            onDelete={() => setConfirmDelete(true)}
          />
        }
        banner={
          editor.conflicts.length > 0 ? (
            <ConflictBanner
              fields={editor.conflicts.map((field) => DRAFT_FIELD_LABEL[field])}
              onKeepMine={() => editor.resolveConflicts("mine")}
              onUseTheirs={() => editor.resolveConflicts("theirs")}
            />
          ) : null
        }
        footer={saveBar}
      >
        <DetailsPanel
          draft={draft}
          onChange={editor.setDraft}
          showOnly={!isServer && !smart}
          nameNote={synced ? syncedNameNote(draft) : undefined}
        />
        {contents}
        <WhereItShowsPanel
          scopeKind={scope.kind}
          collectionId={created ? editor.id : undefined}
          draft={draft}
          onChange={editor.setDraft}
          libraries={chosenLibraries}
          otherProfileNames={otherProfileNames}
          savedShared={view?.personal?.shared ?? false}
          rows={rowsThatShowIt}
          hideConfirm={hideConfirm}
        />
        {artworkSlots.length > 0 ? (
          <LookPanel
            slots={artworkSlots}
            saved={{
              poster: poster.isCollage ? undefined : posterUrl,
              backdrop: view?.backdropUrl,
            }}
            posterFallback={{ collages, collageUrl: poster.isCollage ? poster.url : undefined }}
            value={draft.artwork}
            errors={editor.artworkErrors}
            onRetry={() => void editor.save()}
            onChange={(artwork) => editor.setDraft((current) => ({ ...current, artwork }))}
          />
        ) : null}
      </CollectionEditorShell>
      {view && editor.etag && isServer ? (
        <DeleteCollectionDialog
          open={confirmDelete}
          onOpenChange={(open) => (open ? setConfirmDelete(true) : closeDelete())}
          title={`Delete "${view.name}"?`}
          libraryNames={savedLibraries.map((library) => library.name)}
          rows={deletingRows ? { status: "ready", rows: deletingRows } : deleteRowsState}
          rowLibraryNames={libraryNames}
          isPending={checkingDelete || deleteRows.isPending || remove.isPending}
          error={deleteError}
          onConfirm={() => void deleteServerCollection()}
        />
      ) : null}
      {view && editor.etag && !isServer ? (
        <ConfirmDialog
          open={confirmDelete}
          onOpenChange={setConfirmDelete}
          title={`Delete "${view.name}"?`}
          description={personalDeleteDescription(view.personal?.shared ?? false)}
          confirmLabel="Delete"
          variant="destructive"
          isPending={remove.isPending}
          onConfirm={() => remove.mutate({ id: view.id, etag: editor.etag! })}
        />
      ) : null}
      <SaveFirstDialog
        open={savingFirst !== null}
        description={
          savingFirst
            ? saveFirstDescription(
                view?.name ?? draft.name,
                savingFirst.where,
                pending,
                kind === "manual",
              )
            : ""
        }
        isSaving={editor.isSaving}
        onCancel={() => setSavingFirst(null)}
        discardBlockedReason={discardKeepsHidden ? DISCARD_KEEPS_IT_HIDDEN : null}
        saveBlockedReason={saveBlockedReason}
        onDiscard={() => {
          if (!savingFirst || discardKeepsHidden) return;
          editor.discard();
          setDiscards((count) => count + 1);
          setSavingFirst(null);
          setDetour(savingFirst.path);
        }}
        onSave={() => void saveAndContinue()}
      />
    </>
  );
}
