import { useLayoutEffect, useMemo, useRef, useState } from "react";
import type { PageSectionConfig } from "@/api/types";
import {
  useAdminSectionCapabilities,
  useDeleteSection,
  useDeleteSections,
  useRestoreDefaultSections,
} from "@/hooks/queries/sections";
import { fetchRecipeCatalog } from "@/lib/recipes";
import { useQuery } from "@tanstack/react-query";
import { useAdminCollections } from "@/hooks/queries/admin/collections";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import {
  useAdminHomeRows,
  type BatchFailure,
  type ShownBatchResult,
} from "@/hooks/queries/homeRows/useAdminHomeRows";
import { Copy, Pencil, RotateCcw, SquareCheckBig, Star, StarOff, Trash2 } from "lucide-react";

import { toast } from "sonner";
import {
  adminSectionMutationMessage,
  fetchAdminSectionSnapshot,
  fetchAdminSectionDeleteTargets,
  fetchAdminSections,
} from "@/api/adminSections";
import { V2ProblemError } from "@/api/v2/request";
import { HomeRowsPage, type SharedRowMenuItems } from "@/components/homeRows/HomeRowsPage";
import { DeleteRowDialog, DeleteRowsDialog } from "@/components/homeRows/DeleteRowDialog";
import type { PageMoreMenuItem } from "@/components/calm/PageMoreMenu";
import { RestoreDialog } from "@/components/homeRows/RestoreDialog";
import { MAX_SELECTED_ROWS, SelectModeBar } from "@/components/homeRows/SelectModeBar";
import { AddRowDialog } from "@/components/homeRows/addRow/AddRowDialog";
import { AddToOtherLibrariesDialog } from "@/components/homeRows/AddToOtherLibrariesDialog";
import { canCopyToLibraries, copyTargetPages } from "@/lib/homeRows/bulkCopy";
import { collectionKind, type CollectionSummary } from "@/lib/homeRows/describe";
import type { ActionMenuItem } from "@/components/calm/ActionMenu";
import { useNewRowHighlight } from "@/components/homeRows/useNewRowHighlight";
import { useRowFocus } from "@/components/homeRows/useRowFocus";
import { useRowDialog, useRowLinks } from "@/components/homeRows/useRowLinks";
import { libraryPagesOf, pageLabel, pageParam, samePage } from "@/lib/homeRows/pages";
import type { HomeRow } from "@/lib/homeRows/types";
import { updateCheckboxSelection } from "@/lib/checkboxSelection";

function rowCount(count: number) {
  return count === 1 ? "1 row" : `${count} rows`;
}

function batchFailureText({ title, reason, message }: BatchFailure) {
  if (reason === "changed") return `${title} changed since you opened this page.`;
  if (reason === "legacy") return `${title} is a Trakt row, which can't be turned back on.`;
  return `${title}: ${message}`;
}

/** Reports a select-mode Turn on / Turn off, naming every row left as it was. */
function reportShownBatch({ changedIds, failures }: ShownBatchResult, shown: boolean) {
  const verb = shown ? "Turned on" : "Turned off";
  const attempted = changedIds.length + failures.length;
  const description = failures.map(batchFailureText).join(" ");
  if (attempted === 0) toast.success(`The selected rows are already ${shown ? "on" : "off"}.`);
  else if (failures.length === 0) toast.success(`${verb} ${rowCount(changedIds.length)}.`);
  else if (changedIds.length > 0)
    toast.warning(`${verb} ${changedIds.length} of ${rowCount(attempted)}.`, { description });
  else
    toast.error(`Could not turn ${shown ? "on" : "off"} ${rowCount(attempted)}.`, { description });
}

export default function AdminHomeRows() {
  const adapter = useAdminHomeRows();
  const { scope, serverCapabilities: capabilities } = adapter;
  const capabilitiesFailed = useAdminSectionCapabilities().isError;
  const activeLibraryId = adapter.libraryId ?? null;
  const currentPageKey = pageParam(adapter.page);
  const currentPageLabel = pageLabel(adapter.page, adapter.pages);
  const { data: librariesData } = useAdminLibraries();
  const librariesList = useMemo(() => librariesData ?? [], [librariesData]);
  const focus = useRowFocus(adapter.rows, adapter.pending);
  const [snapshotLoading, setSnapshotLoading] = useState(false);
  const snapshotRequest = useRef(0);
  // A row or order read started on one page must not open a dialog, or arm a
  // confirmation, after the admin has moved to another page.
  useLayoutEffect(() => {
    snapshotRequest.current++;
  }, [currentPageKey]);
  const [deleteETag, setDeleteETag] = useState<string | null>(null);
  const [deleteConflict, setDeleteConflict] = useState(false);
  const deletedRow = useRef(false);
  const [deleteTargets, setDeleteTargets] = useState<
    Awaited<ReturnType<typeof fetchAdminSectionDeleteTargets>>
  >([]);
  // The page's rows and version as Restore read them: the dialog names what
  // this version holds, and the restore is checked against the same version.
  const [restoreSnapshot, setRestoreSnapshot] = useState<
    | (Awaited<ReturnType<typeof fetchAdminSections>> & {
        scope: typeof scope;
        libraryId: number | undefined;
      })
    | null
  >(null);
  const [restoreConflict, setRestoreConflict] = useState(false);
  const { data: collectionsData } = useAdminCollections();
  const { data: recipeCatalog, isError: recipeCatalogFailed } = useQuery({
    queryKey: ["recipe-catalog"],
    queryFn: fetchRecipeCatalog,
    staleTime: 5 * 60 * 1000,
  });
  // Every library collection, hidden ones included: a row may show one the picker no longer offers.
  const collectionSummaries = useMemo(
    () =>
      new Map<string, CollectionSummary>(
        (collectionsData ?? []).map((collection) => [
          collection.id,
          { title: collection.title, kind: collectionKind(collection.collection_type) },
        ]),
      ),
    [collectionsData],
  );
  const [confirmDeleteSection, setConfirmDeleteSection] = useState<PageSectionConfig | null>(null);
  const [confirmDeleteSelected, setConfirmDeleteSelected] = useState(false);
  const [selectMode, setSelectMode] = useState(false);
  // Selection belongs to one page; switching pages starts with nothing selected.
  const [selection, setSelection] = useState<{ page: string; ids: Set<string> }>({
    page: currentPageKey,
    ids: new Set(),
  });
  const selectedSectionIds = useMemo(
    () => (selection.page === currentPageKey ? selection.ids : new Set<string>()),
    [selection, currentPageKey],
  );
  const selectionAnchorRef = useRef<string | null>(null);
  const deleteMutation = useDeleteSection();
  const deleteSectionsMutation = useDeleteSections();
  const restoreDefaultsMutation = useRestoreDefaultSections();
  const [confirmRestoreOpen, setConfirmRestoreOpen] = useState(false);
  const [resetProfiles, setResetProfiles] = useState(false);
  // The Add row / Edit row dialog: open with no session to add a row.
  const [rowDialog, setRowDialog] = useRowDialog();
  const [highlightId, setHighlightId] = useNewRowHighlight();
  // ⋯ Add to other libraries…: the row being copied.
  const [copyRow, setCopyRow] = useState<HomeRow | null>(null);
  const libraryPages = useMemo(() => libraryPagesOf(adapter.pages), [adapter.pages]);
  /** The library pages a row on this page fits, this page included. */
  const copyPagesFor = (row: HomeRow) =>
    activeLibraryId === null ? [] : copyTargetPages(row, libraryPages, activeLibraryId);

  const rowIds = useMemo(() => adapter.rows.map((row) => row.id), [adapter.rows]);
  const selectedSections = useMemo(
    () => adapter.sections.filter((section) => selectedSectionIds.has(section.id)),
    [adapter.sections, selectedSectionIds],
  );
  const canManageCurrentScope = adapter.canEdit;

  function setSelectedSectionIds(update: (previous: Set<string>) => Set<string>) {
    setSelection((current) => ({
      page: currentPageKey,
      ids: update(current.page === currentPageKey ? current.ids : new Set()),
    }));
  }

  function clearSectionSelection() {
    setSelectedSectionIds(() => new Set());
    selectionAnchorRef.current = null;
  }

  function exitSelectMode() {
    setSelectMode(false);
    clearSectionSelection();
  }

  function selectAllRows(checked: boolean) {
    setSelectedSectionIds(() => new Set(checked ? rowIds : []));
    selectionAnchorRef.current = null;
  }

  async function setSelectedShown(shown: boolean) {
    const ids = selectedSections.map((section) => section.id);
    if (ids.length === 0 || ids.length > MAX_SELECTED_ROWS) return;
    try {
      reportShownBatch(await adapter.setShownMany(ids, shown), shown);
    } catch (error) {
      toast.error(adminSectionMutationMessage(error, "Could not change these rows"));
    }
  }

  function updateSectionSelection(sectionId: string, checked: boolean, extendRange: boolean) {
    const anchorId = extendRange && selectedSectionIds.size > 0 ? selectionAnchorRef.current : null;
    setSelectedSectionIds((previous) =>
      updateCheckboxSelection(previous, rowIds, anchorId, sectionId, checked, extendRange),
    );
    if (anchorId === null || !rowIds.includes(anchorId)) {
      selectionAnchorRef.current = sectionId;
    }
  }

  async function prepareSnapshot(action: () => Promise<void>) {
    setSnapshotLoading(true);
    try {
      await action();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Could not load the current rows");
    } finally {
      setSnapshotLoading(false);
    }
  }

  function sectionFor(row: HomeRow) {
    return adapter.sections.find((section) => section.id === row.id);
  }

  function handleDelete(section: PageSectionConfig) {
    const request = ++snapshotRequest.current;
    void prepareSnapshot(async () => {
      const snapshot = await fetchAdminSectionSnapshot(section.id);
      if (request !== snapshotRequest.current) return;
      deletedRow.current = false;
      setConfirmDeleteSection(snapshot.section);
      setDeleteETag(snapshot.etag);
      setDeleteConflict(false);
    });
  }

  /** Edit row…: reads the row as it is now, then opens it in the row dialog. */
  function openRow(row: HomeRow) {
    if (!sectionFor(row) || !canManageCurrentScope || snapshotLoading) return;
    const request = ++snapshotRequest.current;
    void prepareSnapshot(async () => {
      const session = await adapter.openEdit(row.id);
      if (request !== snapshotRequest.current) return;
      setRowDialog({ session });
    });
  }

  function openAddRow() {
    snapshotRequest.current++;
    setRowDialog({ session: null });
  }

  useRowLinks({
    adapter,
    catalog: recipeCatalog,
    catalogFailed: recipeCatalogFailed,
    // Whether the page can change is known once the capabilities load (or
    // fail, which locks it); a row being read first finishes, as the buttons wait.
    settled:
      adapter.status === "ready" &&
      (capabilities !== undefined || capabilitiesFailed) &&
      !snapshotLoading,
    onAdd: (seed) => {
      snapshotRequest.current++;
      setRowDialog({ session: null, seed });
    },
    onEdit: openRow,
  });

  function confirmDeleteRow() {
    if (!confirmDeleteSection || !deleteETag) return;
    const id = confirmDeleteSection.id;
    deleteMutation.mutate(
      { id, etag: deleteETag },
      {
        onSuccess: () => {
          deletedRow.current = true;
          focus.afterRemoval(id);
          setConfirmDeleteSection(null);
        },
        onError: (error) => {
          setDeleteConflict(error instanceof V2ProblemError && error.status === 412);
          toast.error(error instanceof Error ? error.message : "Could not delete this row");
        },
      },
    );
  }

  function prepareBulkDelete() {
    const ids = selectedSections.map((section) => section.id);
    if (ids.length === 0 || ids.length > MAX_SELECTED_ROWS) return;
    const request = ++snapshotRequest.current;
    void prepareSnapshot(async () => {
      const targets = await fetchAdminSectionDeleteTargets(ids);
      if (request !== snapshotRequest.current) return;
      setDeleteTargets(targets);
      setConfirmDeleteSelected(true);
    });
  }

  function openRestore() {
    const request = ++snapshotRequest.current;
    void prepareSnapshot(async () => {
      const libraryId = activeLibraryId ?? undefined;
      const snapshot = await fetchAdminSections(scope, libraryId);
      if (request !== snapshotRequest.current) return;
      setRestoreSnapshot({ ...snapshot, scope, libraryId });
      setRestoreConflict(false);
      setConfirmRestoreOpen(true);
    });
  }

  function closeRestore() {
    snapshotRequest.current++;
    setConfirmRestoreOpen(false);
    setResetProfiles(false);
  }

  function confirmRestore() {
    if (!restoreSnapshot) return;
    restoreDefaultsMutation.mutate(
      {
        scope: restoreSnapshot.scope,
        library_id: restoreSnapshot.libraryId,
        etag: restoreSnapshot.etag,
        reset_profiles: Boolean(capabilities?.reset_profiles && resetProfiles),
      },
      {
        onSuccess: () => {
          toast.success(`Restored the default rows on ${currentPageLabel}.`);
          setConfirmRestoreOpen(false);
          setResetProfiles(false);
        },
        onError: (error) => {
          const stale = error instanceof V2ProblemError && error.status === 412;
          setRestoreConflict(stale);
          if (!stale)
            toast.error(adminSectionMutationMessage(error, "Could not restore the default rows"));
        },
      },
    );
  }

  // What Restore replaces, as of the version the dialog read.
  const restoreSections = restoreSnapshot?.sections ?? [];
  const restoreCollectionTitles = restoreSections
    .filter((section) => section.section_type === "collection")
    .map((section) => section.title);

  const moreItems: PageMoreMenuItem[] = [
    {
      key: "select",
      label: "Select rows",
      help: "Turn several rows on or off, or delete them together.",
      icon: SquareCheckBig,
      // Focus moves to Select all once select mode opens.
      returnFocus: false,
      disabled: !canManageCurrentScope || selectMode || adapter.rows.length === 0,
      onSelect: () => setSelectMode(true),
    },
    {
      key: "restore",
      label: "Restore defaults…",
      help: "Put back the rows Silo starts with on this page.",
      icon: RotateCcw,
      // The restore itself is an admin row write, so `pending` covers it.
      disabled: !canManageCurrentScope || snapshotLoading || adapter.pending,
      onSelect: openRestore,
    },
  ];

  function rowMenuItems(row: HomeRow, shared: SharedRowMenuItems): ActionMenuItem[] {
    const section = sectionFor(row);
    const busy = !canManageCurrentScope || snapshotLoading || !section;
    return [
      {
        key: "edit",
        label: "Edit row…",
        icon: Pencil,
        disabled: busy,
        onSelect: () => openRow(row),
      },
      {
        key: "hero",
        label: row.hero ? "Stop using as hero banner" : "Use as hero banner",
        icon: row.hero ? StarOff : Star,
        disabled: !canManageCurrentScope,
        onSelect: () => void adapter.setHero(row.id, !row.hero),
      },
      shared.moveToTop,
      shared.moveToBottom,
      ...(adapter.capabilities.libraryCopies &&
      canCopyToLibraries(row) &&
      copyPagesFor(row).length > 1
        ? [
            {
              key: "copy",
              label: "Add to other libraries…",
              icon: Copy,
              disabled: busy || adapter.pending,
              onSelect: () => setCopyRow(row),
            },
          ]
        : []),
      {
        key: "delete",
        label: "Delete row…",
        icon: Trash2,
        destructive: true,
        group: true,
        disabled: busy,
        onSelect: () => section && handleDelete(section),
      },
    ];
  }

  const deleteProgressLabel = `Deleting ${deleteSectionsMutation.progress?.completed ?? 0} of ${deleteSectionsMutation.progress?.total ?? deleteTargets.length}…`;
  const bulkBusy =
    !canManageCurrentScope ||
    adapter.pending ||
    snapshotLoading ||
    deleteSectionsMutation.isPending;

  function handleDeleteCapturedSections() {
    deleteSectionsMutation.mutate(deleteTargets, {
      onSuccess: (result) => {
        setSelectedSectionIds(() => new Set(result.failedIds));
        setConfirmDeleteSelected(false);
      },
    });
  }

  return (
    <div
      aria-busy={deleteSectionsMutation.isPending}
      inert={deleteSectionsMutation.isPending ? true : undefined}
    >
      <HomeRowsPage
        adapter={adapter}
        title="Sections"
        subtitle="The sections everyone sees on Home and on library pages. Profiles can still hide, rename or reorder them."
        focus={focus}
        collection={(id) =>
          // Unknown until the list loads; after that, a missing id is a deleted collection.
          collectionsData ? (collectionSummaries.get(id) ?? null) : undefined
        }
        onOpenRow={openRow}
        highlightRowId={highlightId}
        rowMenuItems={rowMenuItems}
        moreItems={moreItems}
        addRow={{ onClick: openAddRow, disabled: !canManageCurrentScope || snapshotLoading }}
        selection={
          selectMode
            ? {
                selectedIds: selectedSectionIds,
                onChange: updateSectionSelection,
                onSelectAll: selectAllRows,
                onExit: exitSelectMode,
                label: (row) => `Select ${row.title}`,
                bar: (
                  <SelectModeBar
                    count={selectedSections.length}
                    busy={bulkBusy}
                    onTurnOn={() => void setSelectedShown(true)}
                    onTurnOff={() => void setSelectedShown(false)}
                    onDelete={prepareBulkDelete}
                  />
                ),
              }
            : undefined
        }
        notices={
          snapshotLoading ? (
            <p role="status" className="text-muted-foreground text-sm">
              Loading the current rows…
            </p>
          ) : null
        }
      >
        <DeleteRowDialog
          rowTitle={confirmDeleteSection?.title ?? ""}
          pageLabel={currentPageLabel}
          open={confirmDeleteSection !== null}
          conflict={deleteConflict}
          busy={deleteMutation.isPending}
          canConfirm={Boolean(deleteETag) && !snapshotLoading}
          onConfirm={confirmDeleteRow}
          onReload={() => {
            if (confirmDeleteSection) handleDelete(confirmDeleteSection);
          }}
          onOpenChange={(open) => {
            if (!open) {
              snapshotRequest.current++;
              setConfirmDeleteSection(null);
            }
          }}
          onCloseAutoFocus={(event) => {
            // After a delete the row's menu is about to go; focus moves to its
            // neighbour once the list refetches.
            if (deletedRow.current) event.preventDefault();
          }}
        />
        <DeleteRowsDialog
          count={deleteTargets.length}
          pageLabel={currentPageLabel}
          open={confirmDeleteSelected}
          busy={deleteSectionsMutation.isPending}
          progress={deleteProgressLabel}
          onConfirm={handleDeleteCapturedSections}
          onOpenChange={(open) => {
            if (!open) setConfirmDeleteSelected(false);
          }}
        />
        <RestoreDialog
          open={confirmRestoreOpen}
          page={adapter.page}
          pageLabel={currentPageLabel}
          otherLibraryLabels={adapter.pages
            .filter(
              (option) => option.ref.kind === "library" && !samePage(option.ref, adapter.page),
            )
            .map((option) => option.label)}
          rowCount={restoreSections.length}
          collectionRowTitles={restoreCollectionTitles}
          resetSupported={Boolean(capabilities?.reset_profiles)}
          resetProfiles={resetProfiles}
          onResetProfilesChange={setResetProfiles}
          conflict={restoreConflict}
          busy={restoreDefaultsMutation.isPending || snapshotLoading}
          canConfirm={restoreSnapshot !== null}
          onConfirm={confirmRestore}
          onReload={() => void adapter.reload().finally(openRestore)}
          onOpenChange={(open) => {
            if (!open) closeRestore();
          }}
        />

        {copyRow && activeLibraryId !== null ? (
          <AddToOtherLibrariesDialog
            row={copyRow}
            pages={copyPagesFor(copyRow)}
            currentId={activeLibraryId}
            onCopy={(ids) => adapter.copyToLibraries(copyRow.id, ids)}
            onClose={() => {
              focus.returnToMenu(copyRow.id);
              setCopyRow(null);
            }}
          />
        ) : null}
        {rowDialog ? (
          <AddRowDialog
            key={rowDialog.key}
            adapter={adapter}
            catalog={recipeCatalog}
            catalogFailed={recipeCatalogFailed}
            libraries={librariesList}
            session={rowDialog.session}
            initialSeed={rowDialog.seed}
            onClose={() => setRowDialog(null)}
            onSaved={(newIds) =>
              rowDialog.seed?.onAdded
                ? rowDialog.seed.onAdded(newIds)
                : setHighlightId(newIds[0] ?? null)
            }
            onDelete={(session) => {
              setRowDialog(null);
              const section = sectionFor(session.row);
              if (section) handleDelete(section);
            }}
          />
        ) : null}
      </HomeRowsPage>
    </div>
  );
}
