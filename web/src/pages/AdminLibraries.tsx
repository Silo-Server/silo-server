import { JobPageControls } from "@/components/admin/JobPageControls";
import { Fragment, useEffect, useMemo, useRef, useState, type ComponentProps } from "react";
import { useSortableAccessibility } from "@/hooks/useSortableAccessibility";
import { useEventChannel } from "@/components/realtimeEventsContext";
import type { Library, LibraryRealtimeMonitoringEntry, ScanRun } from "@/api/types";
import {
  useAdminLibraries,
  useDeleteLibrary,
  useLibraryCapabilities,
  useLibraryMetadataMatchQueues,
  useLibraryRealtimeMonitoring,
  useLibraryRefreshJobs,
  useReorderLibraries,
  useScanAllLibraries,
  useUnmatchedLibraryItems,
} from "@/hooks/queries/admin/libraries";
import { useAdminTrickplayLibraries } from "@/hooks/queries/admin/trickplay";
import { useActiveScans } from "@/hooks/queries/admin/scans";
import { buildLibraryReorderEntries, moveLibraryByOne } from "./adminLibraryOrder";
import {
  LibraryEditorDialog,
  type LibraryEditorSection,
} from "@/components/admin/libraries/LibraryEditorDialog";
import { findLibraryType, libraryTypeLabel } from "@/components/admin/libraries/libraryTypes";
import { LibraryRefreshDialog } from "@/components/admin/libraries/LibraryRefreshDialog";
import { LibraryCardList } from "@/components/admin/libraries/LibraryCardList";
import {
  LibraryActivityPanel,
  LiveActivitySummaryStrip,
} from "@/components/admin/libraries/LibraryActivityPanel";
import {
  getLibraryScanGroupName,
  libraryScanProgressPercent,
  type LibraryRefreshEntry,
  type LibraryScanGroup,
} from "@/components/admin/libraries/libraryActivityHelpers";
import { LibraryCatalogHealthPanel } from "@/components/admin/libraries/LibraryCatalogHealthPanel";
import {
  LibraryActionMenu,
  LibraryLastScanned,
  LibraryScanWarning,
  LibraryStatusBadges,
} from "@/components/admin/libraries/LibraryRowParts";
import { useLibrariesTableLayout } from "@/components/admin/libraries/diagnosticsListState";
import { useLibraryRows, type LibraryRow } from "@/components/admin/libraries/useLibraryRows";
import type { AdminTrickplayLibrary } from "@/hooks/queries/admin/trickplay";
import { PageHeader } from "@/components/page/PageHeader";
import { PathText } from "@/components/PathText";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Navigate, useLocation, useSearchParams } from "react-router";
import { FolderOpen, GripVertical, Plus, RefreshCw, ScanLine, Square } from "lucide-react";
import { legacyLibrariesAutoscanTarget } from "@/pages/autoscanSearchParams";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { cn } from "@/lib/utils";
import {
  DndContext,
  DragOverlay,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import type { DragEndEvent, DragStartEvent, SensorDescriptor, SensorOptions } from "@dnd-kit/core";
import { SortableContext, arrayMove, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { sortableKeyboardCoordinates } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { useMotionSafeOverlay, useMotionSafeSortable } from "@/hooks/useSortableList";
import { numberText, useT } from "@/i18n";
import { useMotionScrollBehavior } from "@/hooks/usePrefersReducedMotion";

// Not translated: a separator glyph.
const SEPARATOR = "·";

// Stable fallbacks while queries load. A fresh `[]` on every render re-ran the
// effect that copies libraries into the reorder state, so the page re-rendered
// until the query resolved.
const NO_LIBRARIES: Library[] = [];
const NO_ACTIVE_SCANS: ScanRun[] = [];

type LibrariesTab = "libraries" | "activity" | "health";

function parseLibrariesTab(value: string | null): LibrariesTab {
  if (value === "activity" || value === "health") return value;
  return "libraries";
}

export default function AdminLibraries() {
  // Autoscan was a tab here before it became its own sidebar topic.
  const { search } = useLocation();
  const autoscanTarget = legacyLibrariesAutoscanTarget(search);
  if (autoscanTarget) return <Navigate to={autoscanTarget} replace />;
  return <LibrariesPage />;
}

function LibrariesPage() {
  const t = useT();
  useEventChannel("scans");
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = parseLibrariesTab(searchParams.get("tab"));
  const [activityLibraryFilter, setActivityLibraryFilter] = useState<number | null>(null);
  const activityTabTriggerRef = useRef<HTMLButtonElement>(null);

  const {
    data: libraries = NO_LIBRARIES,
    isLoading,
    isFetching: librariesFetching,
  } = useAdminLibraries();
  const { data: libraryCapabilities } = useLibraryCapabilities();
  const { data: activeScans = NO_ACTIVE_SCANS } = useActiveScans();
  const { data: realtimeMonitoring } = useLibraryRealtimeMonitoring();
  const { data: trickplayLibraries } = useAdminTrickplayLibraries();
  const unmatchedQuery = useUnmatchedLibraryItems("", false);
  const unmatchedTotal = unmatchedQuery.data?.pages[0]?.total;
  const matcherQueuesQuery = useLibraryMetadataMatchQueues();
  const matcherQueues = matcherQueuesQuery.data;
  const matcherTotal = useMemo(
    () =>
      matcherQueues ? matcherQueues.reduce((sum, queue) => sum + queue.total_count, 0) : undefined,
    [matcherQueues],
  );
  const catalogHealthBadgeCount =
    unmatchedTotal === undefined && matcherTotal === undefined
      ? undefined
      : (unmatchedTotal ?? 0) + (matcherTotal ?? 0);

  const trickplayByLibraryId = useMemo(
    () => new Map(trickplayLibraries?.map((entry) => [Number(entry.library_id), entry]) ?? []),
    [trickplayLibraries],
  );
  const realtimeMonitoringByLibraryId = useMemo(
    () => new Map(realtimeMonitoring?.libraries.map((entry) => [entry.library_id, entry]) ?? []),
    [realtimeMonitoring],
  );
  const refreshJobsQuery = useLibraryRefreshJobs();
  const libraryRefreshJobs = useMemo(() => refreshJobsQuery.data ?? [], [refreshJobsQuery.data]);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingLib, setEditingLib] = useState<Library | null>(null);
  const [editorSection, setEditorSection] = useState<LibraryEditorSection>("general");
  const [confirmDeleteLib, setConfirmDeleteLib] = useState<Library | null>(null);
  const [confirmEmptyRootLib, setConfirmEmptyRootLib] = useState<Library | null>(null);
  const [refreshLib, setRefreshLib] = useState<Library | null>(null);
  const deleteMutation = useDeleteLibrary();
  const scanAllMutation = useScanAllLibraries();
  const isDesktop = useLibrariesTableLayout();

  function setActiveTab(nextTab: LibrariesTab) {
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current);
        if (nextTab === "libraries") {
          next.delete("tab");
        } else {
          next.set("tab", nextTab);
        }
        return next;
      },
      { replace: true },
    );
  }

  function openActivityForLibrary(libraryId: number | null) {
    setActivityLibraryFilter(libraryId);
    setActiveTab("activity");
    // Move focus to the Activity tab trigger once the Libraries panel hides.
    requestAnimationFrame(() => activityTabTriggerRef.current?.focus());
  }

  function openEditor(lib: Library, section: LibraryEditorSection = "general") {
    setEditingLib(lib);
    setEditorSection(section);
    setDialogOpen(true);
  }

  const {
    rowFor,
    activeScansByLibraryId,
    cancelScansMutation,
    refreshMutation,
    confirmCleanupMutation,
  } = useLibraryRows({
    activeScans,
    refreshJobs: libraryRefreshJobs,
    onEdit: openEditor,
    onDelete: setConfirmDeleteLib,
    onRefresh: setRefreshLib,
    onConfirmCleanup: setConfirmEmptyRootLib,
  });

  // DnD reorder state
  const reorderMutation = useReorderLibraries();
  const [orderedLibraries, setOrderedLibraries] = useState<Library[]>(libraries);
  const [activeId, setActiveId] = useState<number | null>(null);
  const sortableAccessibility = useSortableAccessibility(
    orderedLibraries.map((library) => ({ id: library.id, name: library.name })),
  );
  const scrollBehavior = useMotionScrollBehavior();
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
      scrollBehavior,
    }),
  );

  useEffect(() => {
    setOrderedLibraries(libraries);
  }, [libraries]);

  // Shows the new order at once and puts the old one back if saving fails.
  function commitOrder(next: Library[]) {
    const prev = orderedLibraries;
    setOrderedLibraries(next);
    reorderMutation.mutate(buildLibraryReorderEntries(next), {
      onError: () => setOrderedLibraries(prev),
    });
  }

  function handleDragStart(event: DragStartEvent) {
    setActiveId(event.active.id as number);
  }

  function handleDragEnd(event: DragEndEvent) {
    setActiveId(null);
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    const oldIndex = orderedLibraries.findIndex((l) => l.id === active.id);
    const newIndex = orderedLibraries.findIndex((l) => l.id === over.id);
    if (oldIndex === -1 || newIndex === -1) return;
    commitOrder(arrayMove(orderedLibraries, oldIndex, newIndex));
  }

  function handleDragCancel() {
    setActiveId(null);
  }

  function handleMove(libraryId: number, direction: -1 | 1) {
    const next = moveLibraryByOne(orderedLibraries, libraryId, direction);
    if (next) commitOrder(next);
  }

  const activeLibrary = activeId != null ? orderedLibraries.find((l) => l.id === activeId) : null;
  const activeScanGroups: LibraryScanGroup[] = useMemo(() => {
    return Array.from(activeScansByLibraryId.entries())
      .map(([libraryID, scans]) => {
        const library = libraries.find((entry) => entry.id === libraryID) ?? null;
        const runningCount = scans.filter((scan) => scan.status === "running").length;
        return {
          libraryID,
          library,
          scans,
          runningCount,
          queuedCount: scans.length - runningCount,
        };
      })
      .sort((left, right) => {
        if (left.runningCount !== right.runningCount) {
          return right.runningCount - left.runningCount;
        }
        return getLibraryScanGroupName(t, left.library, left.libraryID).localeCompare(
          getLibraryScanGroupName(t, right.library, right.libraryID),
        );
      });
  }, [activeScansByLibraryId, libraries, t]);

  if (isLoading) return <div className="pb-8">{t("web.admin.libraries.page.loading")}</div>;

  const rows = orderedLibraries.map(rowFor);
  const activeRefreshEntries: LibraryRefreshEntry[] = rows
    .filter((row) => row.activeRefreshJob !== undefined)
    .map((row) => ({
      libraryID: row.library.id,
      library: row.library,
      job: row.activeRefreshJob!,
    }));
  const cancellingJobID = rows[0]?.cancellingJobID;
  const cancelJob = rows[0]?.cancelJob ?? (() => {});

  const totalActiveScans = activeScanGroups.reduce((sum, group) => sum + group.scans.length, 0);
  const totalActiveWork = totalActiveScans + activeRefreshEntries.length;
  const hasRunningWork =
    activeScanGroups.some((group) => group.runningCount > 0) ||
    activeRefreshEntries.some((entry) => entry.job.status === "running");

  // Below lg the header buttons share rows at equal width and wrap as needed,
  // with Add Library on a row of its own; from lg they keep their natural size.
  const headerButtonClass = "max-lg:h-11 max-lg:min-w-fit max-lg:flex-1";

  return (
    <div className="space-y-5">
      <ConfirmDialog
        open={confirmDeleteLib !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmDeleteLib(null);
        }}
        title={t("web.admin.libraries.deleteDialog.title")}
        description={t("web.admin.libraries.deleteDialog.description", {
          name: confirmDeleteLib?.name ?? "",
        })}
        confirmLabel={t("web.admin.libraries.deleteDialog.confirm")}
        variant="destructive"
        onConfirm={() => {
          if (confirmDeleteLib) deleteMutation.mutate(confirmDeleteLib.id);
          setConfirmDeleteLib(null);
        }}
      />
      <ConfirmDialog
        open={confirmEmptyRootLib !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmEmptyRootLib(null);
        }}
        title={t("web.admin.libraries.emptyRootDialog.title")}
        description={t("web.admin.libraries.emptyRootDialog.description", {
          name: confirmEmptyRootLib?.name ?? "",
        })}
        confirmLabel={t("web.admin.libraries.emptyRootDialog.confirm")}
        variant="destructive"
        onConfirm={() => {
          if (confirmEmptyRootLib) confirmCleanupMutation.mutate(confirmEmptyRootLib.id);
          setConfirmEmptyRootLib(null);
        }}
      />

      <PageHeader
        title={t("web.admin.libraries.page.title")}
        subtitle={t("web.admin.libraries.page.subtitle")}
        actions={
          <div className="flex gap-2 max-lg:w-full max-lg:flex-wrap">
            <Button
              variant="outline"
              size="sm"
              className={headerButtonClass}
              onClick={() => scanAllMutation.mutate()}
              disabled={scanAllMutation.isPending}
            >
              <ScanLine className="h-3.5 w-3.5" aria-hidden="true" />
              {t("web.admin.libraries.page.scanAll")}
            </Button>
            <Button
              size="sm"
              className="max-lg:h-11 max-lg:basis-full"
              onClick={() => {
                setEditingLib(null);
                setEditorSection("general");
                setDialogOpen(true);
              }}
            >
              <Plus className="me-1 h-4 w-4" aria-hidden="true" />
              {t("web.admin.libraries.page.addLibrary")}
            </Button>
            <LibraryEditorDialog
              open={dialogOpen}
              onOpenChange={(open) => {
                setDialogOpen(open);
                if (!open) {
                  setEditingLib(null);
                  setEditorSection("general");
                }
              }}
              library={editingLib}
              initialSection={editorSection}
              chapterThumbnailsSupported={
                editingLib?.chapter_thumbnails_supported ??
                libraries[0]?.chapter_thumbnails_supported ??
                true
              }
              trickplaySupported={
                editingLib?.trickplay_supported ??
                libraries[0]?.trickplay_supported ??
                (libraryCapabilities?.trickplay === true
                  ? libraryCapabilities.trickplay_supported
                  : undefined)
              }
            />
            <LibraryRefreshDialog
              libraryName={refreshLib?.name ?? null}
              onOpenChange={(open) => {
                // Keep the dialog up until the queued request settles.
                if (!open && !refreshMutation.isPending) setRefreshLib(null);
              }}
              isPending={refreshMutation.isPending}
              onConfirm={(mode) => {
                if (!refreshLib) return;
                const id = refreshLib.id;
                refreshMutation.mutate(
                  { id, mode },
                  { onSettled: () => setRefreshLib((open) => (open?.id === id ? null : open)) },
                );
              }}
            />
          </div>
        }
      />

      <Tabs
        value={activeTab}
        onValueChange={(value) => {
          const next = parseLibrariesTab(value);
          if (next === "activity") setActivityLibraryFilter(null);
          setActiveTab(next);
        }}
      >
        <TabsList
          variant="line"
          className="border-border w-full justify-start overflow-x-auto border-b max-lg:h-auto! max-lg:[scrollbar-width:none] max-lg:[&::-webkit-scrollbar]:hidden"
        >
          <TabsTrigger value="libraries" className="flex-none shrink-0 gap-2 max-lg:h-11">
            {t("web.admin.libraries.tabs.libraries")}
            <span className="bg-secondary text-secondary-foreground rounded-full px-2 py-0.5 text-[11px] font-medium tabular-nums">
              {numberText(rows.length)}
            </span>
          </TabsTrigger>

          <TabsTrigger
            ref={activityTabTriggerRef}
            value="activity"
            className="flex-none shrink-0 gap-2 max-lg:h-11"
          >
            {t("web.admin.libraries.tabs.activity")}
            {totalActiveWork > 0 ? (
              <span
                className={cn(
                  "inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-medium tabular-nums",
                  hasRunningWork
                    ? "bg-success/15 text-success"
                    : "bg-secondary text-secondary-foreground",
                )}
              >
                {hasRunningWork ? (
                  <span className="bg-success inline-block h-1.5 w-1.5 rounded-full" />
                ) : null}
                {t("web.admin.libraries.tabs.activeCount", { count: totalActiveWork })}
              </span>
            ) : null}
          </TabsTrigger>

          <TabsTrigger value="health" className="flex-none shrink-0 gap-2 max-lg:h-11">
            {t("web.admin.libraries.tabs.health")}
            {catalogHealthBadgeCount !== undefined && catalogHealthBadgeCount > 0 ? (
              <span className="bg-warning/15 text-warning rounded-full px-2 py-0.5 text-[11px] font-medium tabular-nums">
                {numberText(catalogHealthBadgeCount)}
              </span>
            ) : null}
          </TabsTrigger>
        </TabsList>

        <TabsContent value="libraries" className="space-y-4 pt-4">
          <LiveActivitySummaryStrip
            groups={activeScanGroups}
            refreshEntries={activeRefreshEntries}
            onViewQueue={() => openActivityForLibrary(null)}
          />

          {isDesktop ? (
            <LibraryTable
              rows={rows}
              activeLibrary={activeLibrary ?? null}
              realtimeMonitoringByLibraryId={realtimeMonitoringByLibraryId}
              trickplayByLibraryId={trickplayByLibraryId}
              sensors={sensors}
              accessibility={sortableAccessibility}
              onDragStart={handleDragStart}
              onDragEnd={handleDragEnd}
              onDragCancel={handleDragCancel}
              onViewActivity={openActivityForLibrary}
            />
          ) : (
            <LibraryCardList
              rows={rows}
              realtimeMonitoringByLibraryId={realtimeMonitoringByLibraryId}
              trickplayByLibraryId={trickplayByLibraryId}
              onMove={handleMove}
              onViewActivity={openActivityForLibrary}
              // One move at a time: each sends the whole order, and the list it
              // starts from must already include the previous move.
              moving={reorderMutation.isPending || librariesFetching}
            />
          )}
        </TabsContent>

        <TabsContent value="activity" className="pt-4">
          <LibraryActivityPanel
            groups={activeScanGroups}
            refreshEntries={activeRefreshEntries}
            refreshJobsLoading={refreshJobsQuery.isPending && refreshJobsQuery.data === undefined}
            refreshJobsError={refreshJobsQuery.isError}
            selectedLibraryID={activityLibraryFilter}
            onSelectLibraryID={setActivityLibraryFilter}
            cancellingLibraryID={
              cancelScansMutation.isPending ? (cancelScansMutation.variables ?? null) : null
            }
            cancellingJobID={cancellingJobID}
            onCancelScans={(libraryID) => cancelScansMutation.mutate(libraryID)}
            onCancelJob={cancelJob}
            footer={
              <JobPageControls
                query={refreshJobsQuery}
                label={t("web.admin.libraries.page.loadOlderRefreshJobs")}
              />
            }
          />
        </TabsContent>

        <TabsContent value="health" className="pt-4">
          <LibraryCatalogHealthPanel
            libraries={libraries}
            unmatchedCount={unmatchedTotal}
            unmatchedError={unmatchedQuery.isError && unmatchedQuery.data === undefined}
            matcherCount={matcherTotal}
            matcherError={matcherQueuesQuery.isError && matcherQueuesQuery.data === undefined}
          />
        </TabsContent>
      </Tabs>
    </div>
  );
}

/** The libraries list from the `lg` breakpoint: a calm table reordered by dragging. */
function LibraryTable({
  rows,
  activeLibrary,
  realtimeMonitoringByLibraryId,
  trickplayByLibraryId,
  sensors,
  accessibility,
  onDragStart,
  onDragEnd,
  onDragCancel,
  onViewActivity,
}: {
  rows: LibraryRow[];
  activeLibrary: Library | null;
  realtimeMonitoringByLibraryId: Map<number, LibraryRealtimeMonitoringEntry>;
  trickplayByLibraryId: Map<number, AdminTrickplayLibrary>;
  sensors: SensorDescriptor<SensorOptions>[];
  accessibility: ComponentProps<typeof DndContext>["accessibility"];
  onDragStart: (event: DragStartEvent) => void;
  onDragEnd: (event: DragEndEvent) => void;
  onDragCancel: () => void;
  onViewActivity: (libraryId: number) => void;
}) {
  const t = useT();
  const overlayMotion = useMotionSafeOverlay();

  if (rows.length === 0) {
    return (
      <div className="surface-panel text-muted-foreground rounded-2xl border-0 px-6 py-10 text-center text-sm">
        {t("web.admin.libraries.list.empty")}
      </div>
    );
  }

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCenter}
      accessibility={accessibility}
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
      onDragCancel={onDragCancel}
    >
      <div className="surface-panel overflow-x-auto rounded-2xl border-0">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="w-10" />
              <TableHead>{t("web.admin.libraries.column.library")}</TableHead>
              <TableHead>{t("web.admin.libraries.column.status")}</TableHead>
              <TableHead className="whitespace-nowrap">
                {t("web.admin.libraries.column.lastScanned")}
              </TableHead>
              <TableHead className="w-[13.5rem] text-end">
                {t("web.admin.libraries.column.actions")}
              </TableHead>
            </TableRow>
          </TableHeader>
          <SortableContext
            items={rows.map((row) => row.library.id)}
            strategy={verticalListSortingStrategy}
          >
            <TableBody>
              {rows.map((row) => {
                const { library: lib, scan } = row;
                const TypeIcon = findLibraryType(lib.type)?.icon ?? FolderOpen;
                const singlePath = lib.paths.length === 1 ? lib.paths[0] : undefined;
                const progressPct =
                  row.runningScanCount > 0 ? libraryScanProgressPercent(row.activeScans) : null;

                return (
                  <Fragment key={lib.id}>
                    <SortableLibraryRow id={lib.id} progressPercent={progressPct}>
                      <TableCell className="max-w-[26rem] py-3">
                        <div className="flex min-w-0 items-center gap-3">
                          <div className="bg-secondary flex h-8 w-8 shrink-0 items-center justify-center rounded-lg">
                            <TypeIcon
                              className="text-muted-foreground h-4 w-4"
                              aria-hidden="true"
                            />
                          </div>
                          <div className="min-w-0 flex-1 space-y-0.5">
                            <div dir="auto" className="bidi-text truncate font-medium">
                              {lib.name}
                            </div>
                            <div className="text-muted-foreground flex min-w-0 items-center gap-1.5 text-xs">
                              <span className="shrink-0">{libraryTypeLabel(lib.type)}</span>
                              <span aria-hidden="true">{SEPARATOR}</span>
                              <button
                                type="button"
                                className="hover:text-foreground min-w-0 truncate text-start transition-colors"
                                title={singlePath}
                                onClick={row.editFolders}
                              >
                                {singlePath !== undefined ? (
                                  <span dir="ltr" className="font-mono text-[11px]">
                                    <PathText path={singlePath} />
                                  </span>
                                ) : (
                                  t("web.admin.libraries.list.folderCount", {
                                    count: lib.paths.length,
                                  })
                                )}
                              </button>
                            </div>
                          </div>
                        </div>
                      </TableCell>
                      <TableCell>
                        <LibraryStatusBadges
                          row={row}
                          realtimeMonitoring={realtimeMonitoringByLibraryId.get(lib.id)}
                          trickplay={trickplayByLibraryId.get(lib.id)}
                          onViewActivity={onViewActivity}
                          showIdle
                        />
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs whitespace-nowrap">
                        <LibraryLastScanned row={row} />
                      </TableCell>
                      <TableCell className="w-[13.5rem] align-middle">
                        <div className="flex flex-nowrap items-center justify-end gap-1.5">
                          <Button
                            variant="outline"
                            size="sm"
                            className={cn(
                              "h-7 gap-1.5 px-2.5 text-xs",
                              scan.stop &&
                                "border-destructive/35 bg-destructive/10 text-destructive hover:bg-destructive/20 hover:text-destructive",
                            )}
                            title={scan.label}
                            aria-label={scan.label}
                            disabled={scan.disabled}
                            onClick={scan.run}
                          >
                            {scan.stop ? (
                              <>
                                <Square className="h-2.5 w-2.5 fill-current" aria-hidden="true" />
                                {t("web.admin.libraries.list.stopAction")}
                              </>
                            ) : (
                              <>
                                <RefreshCw
                                  className={cn("h-3 w-3", scan.pending && "animate-spin")}
                                  aria-hidden="true"
                                />
                                {t("web.admin.libraries.list.scanAction")}
                              </>
                            )}
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            className="text-muted-foreground hover:text-foreground h-7 px-2.5 text-xs"
                            aria-label={t("web.admin.libraries.list.edit", { name: lib.name })}
                            onClick={row.edit}
                          >
                            {t("web.admin.libraries.list.editAction")}
                          </Button>
                          <LibraryActionMenu row={row} />
                        </div>
                      </TableCell>
                    </SortableLibraryRow>
                    {row.hasScanWarning ? (
                      <TableRow className="hover:bg-transparent">
                        <TableCell colSpan={5} className="bg-destructive/5 text-sm">
                          <LibraryScanWarning row={row} />
                        </TableCell>
                      </TableRow>
                    ) : null}
                  </Fragment>
                );
              })}
            </TableBody>
          </SortableContext>
        </Table>
      </div>
      <DragOverlay {...overlayMotion}>
        {activeLibrary ? (
          <Table>
            <TableBody>
              <TableRow className="bg-background shadow-lg">
                <TableCell className="w-10">
                  <GripVertical className="text-muted-foreground h-4 w-4" />
                </TableCell>
                <TableCell className="font-medium">
                  <div className="flex items-center gap-3">
                    <span>{activeLibrary.name}</span>
                    <span className="text-muted-foreground text-xs">
                      {libraryTypeLabel(activeLibrary.type)}
                    </span>
                  </div>
                </TableCell>
                <TableCell />
                <TableCell />
                <TableCell />
              </TableRow>
            </TableBody>
          </Table>
        ) : null}
      </DragOverlay>
    </DndContext>
  );
}

function SortableLibraryRow({
  id,
  progressPercent,
  children,
}: {
  id: number;
  progressPercent: number | null;
  children: React.ReactNode;
}) {
  const t = useT();
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } =
    useMotionSafeSortable({
      id,
    });

  const style: React.CSSProperties = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.4 : 1,
    ...(progressPercent !== null
      ? {
          backgroundImage: `linear-gradient(to right, color-mix(in srgb, var(--success) 75%, transparent) ${progressPercent}%, transparent ${progressPercent}%)`,
          backgroundSize: "100% 2px",
          backgroundRepeat: "no-repeat",
          backgroundPosition: "bottom left",
        }
      : {}),
  };

  return (
    <TableRow
      ref={setNodeRef}
      style={style}
      className={cn(
        "group",
        progressPercent !== null && "forced-colors:border-b-2 forced-colors:border-b-[Highlight]",
      )}
    >
      <TableCell className="w-10">
        <button
          type="button"
          aria-label={t("web.admin.libraries.list.dragToReorder")}
          className="hover:bg-surface-hover cursor-grab touch-none rounded-md p-1 opacity-45 transition-opacity group-hover:opacity-100 focus-visible:opacity-100"
          {...attributes}
          {...listeners}
        >
          <GripVertical className="text-muted-foreground h-4 w-4" />
        </button>
      </TableCell>
      {children}
    </TableRow>
  );
}
