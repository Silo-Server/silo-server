import { JobPageControls } from "@/components/admin/JobPageControls";
import { Fragment, useState, useEffect, useMemo, type ComponentProps } from "react";
import { useSortableAccessibility } from "@/hooks/useSortableAccessibility";
import { useEventChannel } from "@/components/realtimeEventsContext";
import type { Library, LibraryRealtimeMonitoringEntry, ScanRun } from "@/api/types";
import {
  useAdminLibraries,
  useLibraryCapabilities,
  useReorderLibraries,
  useDeleteLibrary,
  useScanAllLibraries,
  useLibraryRealtimeMonitoring,
  useLibraryRefreshJobs,
} from "@/hooks/queries/admin/libraries";
import { useAdminTrickplayLibraries } from "@/hooks/queries/admin/trickplay";
import { useActiveScans } from "@/hooks/queries/admin/scans";
import { buildLibraryReorderEntries, moveLibraryByOne } from "./adminLibraryOrder";
import { LibraryEditorDialog } from "@/components/admin/libraries/LibraryEditorDialog";
import { libraryTypeLabel } from "@/components/admin/libraries/libraryTypes";
import { LibraryRefreshDialog } from "@/components/admin/libraries/LibraryRefreshDialog";
import { LibraryCardList } from "@/components/admin/libraries/LibraryCardList";
import {
  LibraryActiveWork,
  LibraryLastScanned,
  LibraryScanWarning,
  LibraryStatusBadges,
  MountCheckButtonIcon,
} from "@/components/admin/libraries/LibraryRowParts";
import { useLibrariesTableLayout } from "@/components/admin/libraries/diagnosticsListState";
import { useCollapsedScans } from "@/components/admin/libraries/useCollapsedScans";
import { useLibraryRows, type LibraryRow } from "@/components/admin/libraries/useLibraryRows";
import type { AdminTrickplayLibrary } from "@/hooks/queries/admin/trickplay";
import { MetadataMatcherQueuesSection } from "@/components/admin/libraries/MetadataMatcherQueuesSection";
import { AmbiguousRootsSection } from "@/components/admin/libraries/AmbiguousRootsSection";
import { SkippedRootsSection } from "@/components/admin/libraries/SkippedRootsSection";
import { UnmatchedItemsSection } from "@/components/admin/libraries/UnmatchedItemsSection";
import { StaleIDsSection } from "@/components/admin/libraries/StaleIDsSection";
import { PageHeader } from "@/components/page/PageHeader";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Link, Navigate, useLocation } from "react-router";
import {
  Plus,
  Pencil,
  Trash2,
  RefreshCw,
  ScanLine,
  DatabaseBackup,
  GripVertical,
  Wrench,
  ChevronUp,
  ChevronDown,
  Square,
} from "lucide-react";
import { legacyLibrariesAutoscanTarget } from "@/pages/autoscanSearchParams";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { cn } from "@/lib/utils";
import {
  formatActiveScanMode,
  formatActiveScanProgress,
  formatActiveScanTarget,
  formatActiveScanStarted,
  formatActiveScanTrigger,
} from "@/lib/scanRuns";
import {
  DndContext,
  DragOverlay,
  PointerSensor,
  KeyboardSensor,
  closestCenter,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import type { DragStartEvent, DragEndEvent, SensorDescriptor, SensorOptions } from "@dnd-kit/core";
import {
  SortableContext,
  verticalListSortingStrategy,
  useSortable,
  arrayMove,
} from "@dnd-kit/sortable";
import { sortableKeyboardCoordinates } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { useT, type Translate } from "@/i18n";

// Not translated: a separator glyph.
const SEPARATOR = "·";

// Stable fallbacks while queries load. A fresh `[]` on every render re-ran the
// effect that copies libraries into the reorder state, so the page re-rendered
// until the query resolved.
const NO_LIBRARIES: Library[] = [];
const NO_ACTIVE_SCANS: ScanRun[] = [];

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
  const { data: libraries = NO_LIBRARIES, isLoading } = useAdminLibraries();
  const { data: libraryCapabilities } = useLibraryCapabilities();
  const { data: activeScans = NO_ACTIVE_SCANS } = useActiveScans();
  const { data: realtimeMonitoring } = useLibraryRealtimeMonitoring();
  const { data: trickplayLibraries } = useAdminTrickplayLibraries();
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
  const [confirmDeleteLib, setConfirmDeleteLib] = useState<Library | null>(null);
  const [confirmEmptyRootLib, setConfirmEmptyRootLib] = useState<Library | null>(null);
  const [refreshLib, setRefreshLib] = useState<Library | null>(null);
  const deleteMutation = useDeleteLibrary();
  const scanAllMutation = useScanAllLibraries();
  const isDesktop = useLibrariesTableLayout();

  function openEditor(lib: Library) {
    setEditingLib(lib);
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
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
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
  const activeScanGroups = useMemo(() => {
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
  // Below lg the header buttons share rows at equal width and wrap as needed,
  // with Add Library on a row of its own; from lg they keep their natural size.
  const headerButtonClass = "max-lg:h-11 max-lg:min-w-fit max-lg:flex-1";

  return (
    <div className="space-y-6">
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
            {activeScanGroups.length > 0 && (
              <ScanQueuePopover
                groups={activeScanGroups}
                cancellingLibraryID={
                  cancelScansMutation.isPending ? (cancelScansMutation.variables ?? null) : null
                }
                onCancel={(libraryID) => cancelScansMutation.mutate(libraryID)}
                triggerClassName={headerButtonClass}
              />
            )}
            <Button
              variant="outline"
              size="sm"
              className={headerButtonClass}
              onClick={() => scanAllMutation.mutate()}
              disabled={scanAllMutation.isPending}
            >
              <ScanLine className="h-3.5 w-3.5" />
              {t("web.admin.libraries.page.scanAll")}
            </Button>
            <Button variant="outline" size="sm" className={headerButtonClass} asChild>
              <Link to="/admin/maintenance">
                <Wrench className="me-1 h-4 w-4" />
                {t("web.admin.libraries.page.catalogMaintenance")}
              </Link>
            </Button>
            <Button
              size="sm"
              className="max-lg:h-11 max-lg:basis-full"
              onClick={() => {
                setEditingLib(null);
                setDialogOpen(true);
              }}
            >
              <Plus className="me-1 h-4 w-4" /> {t("web.admin.libraries.page.addLibrary")}
            </Button>
            <LibraryEditorDialog
              open={dialogOpen}
              onOpenChange={(open) => {
                setDialogOpen(open);
                if (!open) setEditingLib(null);
              }}
              library={editingLib}
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

      <div className="space-y-6">
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
          />
        ) : (
          <LibraryCardList
            rows={rows}
            realtimeMonitoringByLibraryId={realtimeMonitoringByLibraryId}
            trickplayByLibraryId={trickplayByLibraryId}
            onMove={handleMove}
          />
        )}

        <UnmatchedItemsSection />
        <MetadataMatcherQueuesSection libraries={libraries} />
        <AmbiguousRootsSection libraries={libraries} />
        <SkippedRootsSection />
        <StaleIDsSection />
      </div>
      {/* After the content so its padded row never pushes the page header down. */}
      <JobPageControls
        query={refreshJobsQuery}
        label={t("web.admin.libraries.page.loadOlderRefreshJobs")}
      />
    </div>
  );
}

/** The libraries list from the `lg` breakpoint: a table reordered by dragging. */
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
}) {
  const t = useT();
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
            <TableRow>
              <TableHead className="w-10" />
              <TableHead>{t("web.admin.libraries.column.name")}</TableHead>
              <TableHead>{t("web.admin.libraries.column.paths")}</TableHead>
              <TableHead>{t("web.admin.libraries.column.type")}</TableHead>
              <TableHead>{t("web.admin.libraries.column.status")}</TableHead>
              <TableHead>{t("web.admin.libraries.column.lastScanned")}</TableHead>
              <TableHead className="w-[15rem]">{t("web.admin.libraries.column.actions")}</TableHead>
            </TableRow>
          </TableHeader>
          <SortableContext
            items={rows.map((row) => row.library.id)}
            strategy={verticalListSortingStrategy}
          >
            <TableBody>
              {rows.map((row) => {
                const { library: lib, scan, refresh, mountCheck, cleanup } = row;
                return (
                  <Fragment key={lib.id}>
                    <SortableLibraryRow id={lib.id}>
                      <TableCell className="font-medium">{lib.name}</TableCell>
                      <TableCell className="text-xs">
                        {lib.paths.length === 1 ? (
                          <span dir="ltr" className="text-muted-foreground font-mono">
                            {lib.paths[0]}
                          </span>
                        ) : (
                          <button
                            type="button"
                            className="text-muted-foreground hover:text-foreground text-start transition-colors"
                            onClick={row.edit}
                          >
                            {t("web.admin.libraries.list.folderCount", {
                              count: lib.paths.length,
                            })}
                          </button>
                        )}
                      </TableCell>
                      <TableCell>
                        <Badge variant="secondary">{libraryTypeLabel(lib.type)}</Badge>
                      </TableCell>
                      <TableCell>
                        <LibraryStatusBadges
                          row={row}
                          realtimeMonitoring={realtimeMonitoringByLibraryId.get(lib.id)}
                          trickplay={trickplayByLibraryId.get(lib.id)}
                        />
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">
                        <LibraryLastScanned row={row} />
                      </TableCell>
                      <TableCell className="w-[15rem] align-middle">
                        <div className="flex flex-nowrap items-center justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            className={cn("h-7 w-7", scan.stop && "text-destructive")}
                            title={scan.label}
                            aria-label={scan.label}
                            disabled={scan.disabled}
                            onClick={scan.run}
                          >
                            {scan.stop ? (
                              <Square className="h-3 w-3 fill-current" />
                            ) : (
                              <RefreshCw
                                className={`h-3 w-3 ${scan.pending ? "animate-spin" : ""}`}
                              />
                            )}
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className={cn("h-7 w-7", refresh.stop && "text-destructive")}
                            title={refresh.label}
                            aria-label={refresh.label}
                            disabled={refresh.disabled}
                            onClick={refresh.run}
                          >
                            {refresh.stop ? (
                              <Square className="h-3 w-3 fill-current" />
                            ) : (
                              <DatabaseBackup className="h-3 w-3" />
                            )}
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7"
                            title={mountCheck.message ?? t("web.admin.libraries.list.verifyMounts")}
                            aria-label={
                              mountCheck.message ?? t("web.admin.libraries.list.verifyMounts")
                            }
                            disabled={mountCheck.pending}
                            onClick={mountCheck.run}
                          >
                            <MountCheckButtonIcon
                              pending={mountCheck.pending}
                              result={mountCheck.result}
                            />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7"
                            aria-label={t("web.admin.libraries.list.edit", { name: lib.name })}
                            onClick={row.edit}
                          >
                            <Pencil className="h-3 w-3" aria-hidden="true" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7"
                            aria-label={t("web.admin.libraries.list.delete", {
                              name: lib.name,
                            })}
                            onClick={row.remove}
                          >
                            <Trash2 className="h-3 w-3" aria-hidden="true" />
                          </Button>
                          {cleanup.available ? (
                            <Button
                              variant="ghost"
                              size="icon"
                              className="text-destructive h-7 w-7"
                              title={t("web.admin.libraries.list.confirmCleanupTitle")}
                              disabled={cleanup.pending}
                              onClick={cleanup.run}
                            >
                              <Trash2 className="h-3 w-3" />
                            </Button>
                          ) : null}
                        </div>
                      </TableCell>
                    </SortableLibraryRow>
                    {row.hasActiveWork ? (
                      <TableRow className="hover:bg-transparent">
                        <TableCell colSpan={7} className="bg-muted/20 px-4 py-2">
                          <LibraryActiveWork row={row} className="ps-12" />
                        </TableCell>
                      </TableRow>
                    ) : null}
                  </Fragment>
                );
              })}
              {rows
                .filter((row) => row.hasScanWarning)
                .map((row) => (
                  <TableRow key={`${row.library.id}-warning`}>
                    <TableCell colSpan={7} className="bg-destructive/5 text-sm">
                      <LibraryScanWarning row={row} />
                    </TableCell>
                  </TableRow>
                ))}
            </TableBody>
          </SortableContext>
        </Table>
      </div>
      <DragOverlay>
        {activeLibrary ? (
          <Table>
            <TableBody>
              <TableRow className="bg-background shadow-lg">
                <TableCell className="w-10">
                  <GripVertical className="text-muted-foreground h-4 w-4" />
                </TableCell>
                <TableCell className="font-medium">{activeLibrary.name}</TableCell>
                <TableCell className="text-muted-foreground text-xs">
                  {activeLibrary.paths.length === 1 ? (
                    <span dir="ltr" className="font-mono">
                      {activeLibrary.paths[0]}
                    </span>
                  ) : (
                    t("web.admin.libraries.list.folderCount", {
                      count: activeLibrary.paths.length,
                    })
                  )}
                </TableCell>
                <TableCell>
                  <Badge variant="secondary">{libraryTypeLabel(activeLibrary.type)}</Badge>
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

function ScanQueuePopover({
  groups,
  cancellingLibraryID,
  onCancel,
  triggerClassName,
}: {
  groups: Array<{
    libraryID: number;
    library: Library | null;
    scans: ScanRun[];
    runningCount: number;
    queuedCount: number;
  }>;
  cancellingLibraryID: number | null;
  onCancel: (libraryID: number) => void;
  triggerClassName?: string;
}) {
  const t = useT();
  const totalRunning = groups.reduce((sum, group) => sum + group.runningCount, 0);
  const totalQueued = groups.reduce((sum, group) => sum + group.queuedCount, 0);
  const totalScans = totalRunning + totalQueued;

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button variant="outline" size="sm" className={cn("gap-2", triggerClassName)}>
          <span className="relative flex h-2 w-2">
            <span className="bg-success absolute inline-flex h-full w-full animate-ping rounded-full opacity-75" />
            <span className="bg-success relative inline-flex h-2 w-2 rounded-full" />
          </span>
          <span className="tabular-nums">
            {t("web.admin.libraries.scanQueue.trigger", {
              count: totalScans,
              count_text: totalScans,
            })}
          </span>
        </Button>
      </PopoverTrigger>

      <PopoverContent align="end" className="w-[400px] max-w-[calc(100vw-1rem)] p-0">
        {/* Accent bar */}
        <div className="scan-queue-accent absolute inset-x-0 top-0 h-px rounded-t-xl" />

        {/* Header */}
        <div className="border-border/40 flex items-center justify-between border-b px-4 py-3">
          <div className="flex items-center gap-2.5">
            <RefreshCw
              className="text-primary h-3.5 w-3.5 animate-spin"
              style={{ animationDuration: "3s" }}
            />
            <span className="text-sm font-semibold">
              {t("web.admin.libraries.scanQueue.title")}
            </span>
          </div>
          <div className="text-muted-foreground flex items-center gap-1.5 text-[11px]">
            {totalRunning > 0 && (
              <>
                <span className="bg-success inline-block h-1.5 w-1.5 rounded-full" />
                <span className="tabular-nums">
                  {t("web.admin.libraries.scans.running", {
                    count: totalRunning,
                    count_text: totalRunning,
                  })}
                </span>
              </>
            )}
            {totalRunning > 0 && totalQueued > 0 && (
              <span className="text-border">{SEPARATOR}</span>
            )}
            {totalQueued > 0 && (
              <>
                <span className="bg-muted-foreground/40 inline-block h-1.5 w-1.5 rounded-full" />
                <span className="tabular-nums">
                  {t("web.admin.libraries.scans.queued", {
                    count: totalQueued,
                    count_text: totalQueued,
                  })}
                </span>
              </>
            )}
          </div>
        </div>

        {/* Library groups */}
        <div className="max-h-[360px] overflow-y-auto px-3 py-2">
          {groups.map((group, gi) => (
            <ScanQueueGroup
              key={group.libraryID}
              group={group}
              groupIndex={gi}
              isLast={gi === groups.length - 1}
              cancelling={cancellingLibraryID === group.libraryID}
              onCancel={onCancel}
            />
          ))}
        </div>
      </PopoverContent>
    </Popover>
  );
}

function ScanQueueGroup({
  group,
  groupIndex,
  isLast,
  cancelling,
  onCancel,
}: {
  group: {
    libraryID: number;
    library: Library | null;
    scans: ScanRun[];
    runningCount: number;
    queuedCount: number;
  };
  groupIndex: number;
  isLast: boolean;
  cancelling: boolean;
  onCancel: (libraryID: number) => void;
}) {
  const t = useT();
  const { expanded, setExpanded, visibleScans, hiddenCount, collapsible } = useCollapsedScans(
    group.scans,
  );

  return (
    <div>
      {/* Library header row */}
      <div className="flex items-center gap-2 py-1.5">
        <span className="text-muted-foreground/60 text-[10px] font-semibold tracking-widest uppercase">
          {getLibraryScanGroupName(t, group.library, group.libraryID)}
        </span>
        <div className="bg-border/25 h-px flex-1" />
        <Button
          variant="ghost"
          size="sm"
          className="text-muted-foreground hover:text-destructive h-6 gap-1 px-2 text-[10px]"
          disabled={cancelling}
          onClick={() => onCancel(group.libraryID)}
        >
          <Square className="h-2.5 w-2.5" />
          {t("common.cancel")}
        </Button>
      </div>

      {/* Scan rows */}
      {visibleScans.map((scan, si) => (
        <div
          key={scan.id}
          className={cn(
            "flex items-start gap-2.5 rounded-lg px-2.5 py-2 transition-colors",
            scan.status === "running" ? "bg-primary/[0.04]" : "",
          )}
          style={{
            animation: "fade-in 0.25s ease-out backwards",
            animationDelay: `${Math.min(groupIndex * 4 + si, 12) * 40}ms`,
          }}
        >
          {/* Status dot */}
          <div className="relative mt-[5px] flex h-2.5 w-2.5 shrink-0 items-center justify-center">
            {scan.status === "running" ? (
              <>
                <span className="bg-success/20 absolute h-2.5 w-2.5 animate-ping rounded-full" />
                <span className="bg-success shadow-success/30 relative h-[7px] w-[7px] rounded-full shadow-[0_0_5px_1px]" />
              </>
            ) : (
              <span className="bg-muted-foreground/20 ring-muted-foreground/15 h-[5px] w-[5px] rounded-full ring-[1.5px]" />
            )}
          </div>

          {/* Details */}
          <div className="min-w-0 flex-1">
            <div className="flex items-baseline gap-1.5">
              <span className="text-xs leading-snug font-medium">{formatActiveScanMode(scan)}</span>
              {scan.trigger && (
                <span className="bg-muted/50 text-muted-foreground rounded px-1 py-px text-[9px] font-medium">
                  {formatActiveScanTrigger(scan.trigger)}
                </span>
              )}
            </div>
            <div className="text-muted-foreground mt-px flex items-center gap-1 text-[10px] leading-relaxed">
              <code
                className="text-muted-foreground/70 max-w-[200px] truncate font-mono"
                title={scan.path}
              >
                {formatActiveScanTarget(scan)}
              </code>
              <span className="text-border/50 shrink-0">{SEPARATOR}</span>
              <span className="shrink-0">
                {scan.status === "running"
                  ? formatActiveScanStarted(scan.started_at)
                  : t("web.admin.libraries.scanQueue.waiting")}
              </span>
            </div>
            {formatActiveScanProgress(scan) && (
              <div className="text-muted-foreground/80 mt-1 truncate text-[10px] leading-relaxed">
                {formatActiveScanProgress(scan)}
              </div>
            )}
          </div>
        </div>
      ))}
      {collapsible ? (
        <button
          type="button"
          className="text-muted-foreground hover:text-foreground flex items-center gap-1 rounded-lg px-2.5 py-1.5 text-[10px] transition-colors"
          onClick={() => setExpanded((current) => !current)}
        >
          {expanded ? (
            <>
              {t("web.admin.libraries.scanQueue.showLess")} <ChevronUp className="h-3 w-3" />
            </>
          ) : (
            <>
              {t(
                hiddenCount <= group.queuedCount
                  ? "web.admin.libraries.scanQueue.moreQueued"
                  : "web.admin.libraries.scanQueue.more",
                { count: hiddenCount, count_text: hiddenCount },
              )}{" "}
              <ChevronDown className="h-3 w-3" />
            </>
          )}
        </button>
      ) : null}

      {/* Separator between groups */}
      {!isLast && <div className="bg-border/20 my-1 h-px" />}
    </div>
  );
}

function getLibraryScanGroupName(t: Translate, library: Library | null, libraryID: number) {
  return library?.name ?? t("web.admin.libraries.scanQueue.libraryFallback", { id: libraryID });
}

function SortableLibraryRow({ id, children }: { id: number; children: React.ReactNode }) {
  const t = useT();
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id,
  });

  const style: React.CSSProperties = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.4 : 1,
  };

  return (
    <TableRow ref={setNodeRef} style={style}>
      <TableCell className="w-10">
        <button
          type="button"
          aria-label={t("web.admin.libraries.list.dragToReorder")}
          className="hover:bg-surface-hover cursor-grab touch-none rounded-md p-1 transition-colors"
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
