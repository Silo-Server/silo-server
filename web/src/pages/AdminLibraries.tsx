import { Fragment, useState, useEffect, useCallback, useMemo } from "react";
import type { FormEvent } from "react";
import { useDebounce } from "@/hooks/useDebounce";
import { useEventChannel } from "@/components/realtimeEventsContext";
import type {
  AdminJob,
  CreateLibraryRequest,
  Library,
  LibraryMountCheckResponse,
  LibraryRoot,
  LibrarySkippedRoot,
  ScanRun,
  StaleMediaID,
  UnmatchedLibraryItem,
} from "@/api/types";
import {
  useAdminLibraries,
  useCancelLibraryScans,
  useReorderLibraries,
  useSkippedLibraryRoots,
  useLibraryRoots,
  useUpsertLibraryRootOverride,
  useDeleteLibraryRootOverride,
  useStaleMediaIDs,
  useCheckLibraryMount,
  useCreateLibrary,
  useUpdateLibrary,
  useDeleteLibrary,
  useScanLibrary,
  useScanAllLibraries,
  useLibraryRefreshJobs,
  useRefreshLibraryMetadata,
  useConfirmEmptyRootCleanup,
  useLibraryProviders,
  useSetLibraryProviders,
  useUploadLibraryPoster,
  useDeleteLibraryPoster,
  useUnmatchedLibraryItems,
  UNMATCHED_PAGE_SIZE,
} from "@/hooks/queries/admin/libraries";
import { useActiveScans } from "@/hooks/queries/admin/scans";
import { buildLibraryReorderEntries } from "./adminLibraryOrder";
import MatchItemDialog from "@/components/MatchItemDialog";
import { useAdminPlugins } from "@/hooks/queries/admin/plugins";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogDescription,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { LANGUAGES } from "@/player/utils/languageNames";
import { Link } from "react-router";
import {
  Plus,
  Pencil,
  Trash2,
  RefreshCw,
  DatabaseBackup,
  ArrowUp,
  ArrowDown,
  GripVertical,
  Wrench,
  HardDrive,
  ImageIcon,
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  ChevronsRight,
  ChevronUp,
  ChevronDown,
  AlertTriangle,
  Square,
  Unlink,
  Search,
  FolderOpen,
} from "lucide-react";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import FolderBrowser from "@/components/FolderBrowser";
import PathAutocompleteInput from "@/components/PathAutocompleteInput";
import { cn } from "@/lib/utils";
import {
  compareActiveScans,
  formatActiveScanMode,
  formatActiveScanProgress,
  formatActiveScanTime,
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
import type { DragStartEvent, DragEndEvent } from "@dnd-kit/core";
import {
  SortableContext,
  verticalListSortingStrategy,
  useSortable,
  arrayMove,
} from "@dnd-kit/sortable";
import { sortableKeyboardCoordinates } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { formatJobProgress } from "@/components/adminCatalogMaintenanceFormatters";

export default function AdminLibraries() {
  useEventChannel("scans");
  const { data: libraries = [], isLoading } = useAdminLibraries();
  const { data: activeScans = [] } = useActiveScans();
  const { data: libraryRefreshJobs = [] } = useLibraryRefreshJobs();
  const { data: skippedRoots = [] } = useSkippedLibraryRoots();
  const { data: staleIDs = [] } = useStaleMediaIDs();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingLib, setEditingLib] = useState<Library | null>(null);
  const [confirmDeleteLib, setConfirmDeleteLib] = useState<Library | null>(null);
  const [confirmEmptyRootLib, setConfirmEmptyRootLib] = useState<Library | null>(null);
  const [lastMountCheckByLibraryId, setLastMountCheckByLibraryId] = useState<
    Record<number, LibraryMountCheckResponse>
  >({});
  const deleteMutation = useDeleteLibrary();
  const mountCheckMutation = useCheckLibraryMount();
  const scanMutation = useScanLibrary();
  const scanAllMutation = useScanAllLibraries();
  const cancelScansMutation = useCancelLibraryScans();

  // DnD reorder state
  const reorderMutation = useReorderLibraries();
  const [orderedLibraries, setOrderedLibraries] = useState<Library[]>(libraries);
  const [activeId, setActiveId] = useState<number | null>(null);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  useEffect(() => {
    setOrderedLibraries(libraries);
  }, [libraries]);

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
    const next = arrayMove(orderedLibraries, oldIndex, newIndex);
    const prev = orderedLibraries;
    setOrderedLibraries(next);
    reorderMutation.mutate(buildLibraryReorderEntries(next), {
      onError: () => setOrderedLibraries(prev),
    });
  }

  function handleDragCancel() {
    setActiveId(null);
  }

  const activeLibrary = activeId != null ? orderedLibraries.find((l) => l.id === activeId) : null;
  const refreshMutation = useRefreshLibraryMetadata();
  const confirmEmptyRootCleanupMutation = useConfirmEmptyRootCleanup();
  const activeRefreshJobsByLibraryId = useMemo(() => {
    const jobsByLibraryID = new Map<number, AdminJob>();
    for (const job of libraryRefreshJobs) {
      if (job.status !== "queued" && job.status !== "running") {
        continue;
      }
      const libraryID = getLibraryRefreshLibraryID(job);
      if (libraryID === null || jobsByLibraryID.has(libraryID)) {
        continue;
      }
      jobsByLibraryID.set(libraryID, job);
    }
    return jobsByLibraryID;
  }, [libraryRefreshJobs]);
  const activeScansByLibraryId = useMemo(() => {
    const scansByLibraryID = new Map<number, ScanRun[]>();
    for (const scan of activeScans) {
      const list = scansByLibraryID.get(scan.library_id) ?? [];
      list.push(scan);
      scansByLibraryID.set(scan.library_id, list);
    }
    for (const scans of scansByLibraryID.values()) {
      scans.sort(compareActiveScans);
    }
    return scansByLibraryID;
  }, [activeScans]);
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
        return getLibraryScanGroupName(left.library, left.libraryID).localeCompare(
          getLibraryScanGroupName(right.library, right.libraryID),
        );
      });
  }, [activeScansByLibraryId, libraries]);

  function handleDelete(lib: Library) {
    setConfirmDeleteLib(lib);
  }

  function handleConfirmEmptyRootCleanup(lib: Library) {
    setConfirmEmptyRootLib(lib);
  }

  function handleMountCheck(libraryId: number) {
    mountCheckMutation.mutate(libraryId, {
      onSuccess: (result) => {
        setLastMountCheckByLibraryId((current) => ({
          ...current,
          [libraryId]: result,
        }));
      },
    });
  }

  if (isLoading) return <div className="p-8">Loading libraries...</div>;

  return (
    <div className="space-y-6">
      <ConfirmDialog
        open={confirmDeleteLib !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmDeleteLib(null);
        }}
        title="Delete library"
        description={`Delete library "${confirmDeleteLib?.name}"? This action cannot be undone.`}
        confirmLabel="Delete"
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
        title="Confirm empty root cleanup"
        description={`If the next scan still finds 0 media files for "${confirmEmptyRootLib?.name}", remove the library items?`}
        confirmLabel="Confirm"
        variant="destructive"
        onConfirm={() => {
          if (confirmEmptyRootLib) confirmEmptyRootCleanupMutation.mutate(confirmEmptyRootLib.id);
          setConfirmEmptyRootLib(null);
        }}
      />
      <div className="page-header">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Libraries</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Manage library roots and scans. Catalog import/export now lives under Maintenance.
          </p>
        </div>
        <div className="flex gap-2">
          {activeScanGroups.length > 0 && (
            <ScanQueuePopover
              groups={activeScanGroups}
              cancellingLibraryID={
                cancelScansMutation.isPending ? (cancelScansMutation.variables ?? null) : null
              }
              onCancel={(libraryID) => cancelScansMutation.mutate(libraryID)}
            />
          )}
          <Button
            variant="outline"
            size="sm"
            onClick={() => scanAllMutation.mutate()}
            disabled={scanAllMutation.isPending}
          >
            <RefreshCw
              className={`mr-1 h-4 w-4 ${scanAllMutation.isPending ? "animate-spin" : ""}`}
            />{" "}
            Scan All
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link to="/admin/maintenance">
              <Wrench className="mr-1 h-4 w-4" />
              Catalog Maintenance
            </Link>
          </Button>
          <Dialog
            open={dialogOpen}
            onOpenChange={(open) => {
              setDialogOpen(open);
              if (!open) setEditingLib(null);
            }}
          >
            <DialogTrigger asChild>
              <Button size="sm">
                <Plus className="mr-1 h-4 w-4" /> Add Library
              </Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>{editingLib ? "Edit Library" : "Add Library"}</DialogTitle>
                <DialogDescription>
                  Configure scan roots, metadata sources, and optional chapter thumbnails for this
                  library.
                </DialogDescription>
              </DialogHeader>
              <LibraryForm
                key={editingLib?.id ?? "new"}
                library={editingLib}
                chapterThumbnailsSupported={
                  editingLib?.chapter_thumbnails_supported ??
                  libraries[0]?.chapter_thumbnails_supported ??
                  true
                }
                onClose={() => {
                  setDialogOpen(false);
                  setEditingLib(null);
                }}
              />
            </DialogContent>
          </Dialog>
        </div>
      </div>

      <DndContext
        sensors={sensors}
        collisionDetection={closestCenter}
        onDragStart={handleDragStart}
        onDragEnd={handleDragEnd}
        onDragCancel={handleDragCancel}
      >
        <div className="surface-panel overflow-x-auto rounded-2xl border-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-10" />
                <TableHead>Name</TableHead>
                <TableHead>Paths</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Last Scanned</TableHead>
                <TableHead className="w-32">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <SortableContext
              items={orderedLibraries.map((l) => l.id)}
              strategy={verticalListSortingStrategy}
            >
              <TableBody>
                {orderedLibraries.map((lib) => {
                  const isScanning = scanMutation.isPending && scanMutation.variables === lib.id;
                  const activeRefreshJob = activeRefreshJobsByLibraryId.get(lib.id);
                  const activeLibraryScans = activeScansByLibraryId.get(lib.id) ?? [];
                  const runningLibraryScans = activeLibraryScans.filter(
                    (scan) => scan.status === "running",
                  ).length;
                  const queuedLibraryScans = activeLibraryScans.length - runningLibraryScans;
                  const isRefreshing =
                    (refreshMutation.isPending && refreshMutation.variables === lib.id) ||
                    activeRefreshJob !== undefined;
                  const isCheckingMount =
                    mountCheckMutation.isPending && mountCheckMutation.variables === lib.id;
                  const mountCheck = lastMountCheckByLibraryId[lib.id];
                  return (
                    <SortableLibraryRow key={lib.id} id={lib.id}>
                      <TableCell className="font-medium">{lib.name}</TableCell>
                      <TableCell className="font-mono text-xs">
                        {lib.paths.length === 1 ? (
                          <span className="text-muted-foreground">{lib.paths[0]}</span>
                        ) : (
                          <button
                            type="button"
                            className="text-muted-foreground hover:text-foreground text-left transition-colors"
                            onClick={() => {
                              setEditingLib(lib);
                              setDialogOpen(true);
                            }}
                          >
                            {lib.paths.length} folders
                          </button>
                        )}
                      </TableCell>
                      <TableCell>
                        <Badge variant="secondary">{lib.type}</Badge>
                      </TableCell>
                      <TableCell>
                        <div className="flex flex-col gap-1">
                          <Badge variant={lib.enabled ? "outline" : "destructive"}>
                            {lib.enabled ? "Enabled" : "Disabled"}
                          </Badge>
                          {runningLibraryScans > 0 ? (
                            <Badge variant="secondary">{runningLibraryScans} running</Badge>
                          ) : null}
                          {queuedLibraryScans > 0 ? (
                            <Badge variant="secondary">{queuedLibraryScans} queued</Badge>
                          ) : null}
                          {lib.scan_warning_code === "empty_root" ? (
                            <Badge variant="destructive">Empty root guarded</Badge>
                          ) : null}
                        </div>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">
                        <div className="space-y-1">
                          <div>
                            {lib.last_scanned_at
                              ? new Date(lib.last_scanned_at).toLocaleString()
                              : "Never"}
                          </div>
                          {lib.scan_warning_at ? (
                            <div className="text-destructive text-[11px]">
                              Warning: {new Date(lib.scan_warning_at).toLocaleString()}
                            </div>
                          ) : null}
                        </div>
                      </TableCell>
                      <TableCell>
                        <div className="flex flex-wrap gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7"
                            title="Check mount"
                            disabled={isCheckingMount}
                            onClick={() => handleMountCheck(lib.id)}
                          >
                            <HardDrive
                              className={`h-3 w-3 ${isCheckingMount ? "animate-pulse" : ""}`}
                            />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7"
                            title="Scan Library"
                            disabled={isScanning}
                            onClick={() => scanMutation.mutate(lib.id)}
                          >
                            <RefreshCw className={`h-3 w-3 ${isScanning ? "animate-spin" : ""}`} />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7"
                            title="Refresh metadata"
                            disabled={isRefreshing}
                            onClick={() => refreshMutation.mutate(lib.id)}
                          >
                            <DatabaseBackup
                              className={cn("h-3 w-3", isRefreshing && "animate-spin")}
                            />
                          </Button>
                          {lib.scan_warning_code === "empty_root" ? (
                            <Button
                              variant="ghost"
                              size="icon"
                              className="text-destructive h-7 w-7"
                              title="Confirm deletion for the next empty-root scan"
                              disabled={
                                confirmEmptyRootCleanupMutation.isPending &&
                                confirmEmptyRootCleanupMutation.variables === lib.id
                              }
                              onClick={() => handleConfirmEmptyRootCleanup(lib)}
                            >
                              <Trash2 className="h-3 w-3" />
                            </Button>
                          ) : null}
                          {activeLibraryScans.length > 0 ? (
                            <Button
                              variant="ghost"
                              size="icon"
                              className="text-destructive h-7 w-7"
                              title="Cancel queued and running scans for this library"
                              disabled={
                                cancelScansMutation.isPending &&
                                cancelScansMutation.variables === lib.id
                              }
                              onClick={() => cancelScansMutation.mutate(lib.id)}
                            >
                              <Square className="h-3 w-3" />
                            </Button>
                          ) : null}
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7"
                            aria-label={`Edit ${lib.name}`}
                            onClick={() => {
                              setEditingLib(lib);
                              setDialogOpen(true);
                            }}
                          >
                            <Pencil className="h-3 w-3" aria-hidden="true" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-7 w-7"
                            aria-label={`Delete ${lib.name}`}
                            onClick={() => handleDelete(lib)}
                          >
                            <Trash2 className="h-3 w-3" aria-hidden="true" />
                          </Button>
                        </div>
                        {mountCheck ? (
                          <MountCheckInlineResult result={mountCheck} warning={false} />
                        ) : null}
                        {activeRefreshJob ? (
                          <div className="text-muted-foreground mt-2 space-y-1 text-[11px]">
                            <div>{activeRefreshJob.message || "Metadata refresh queued"}</div>
                            <div>Progress: {formatJobProgress(activeRefreshJob)}</div>
                          </div>
                        ) : null}
                        {activeLibraryScans.length > 0 ? (
                          <div className="text-muted-foreground mt-2 space-y-1 text-[11px]">
                            {activeLibraryScans.slice(0, 2).map((scan) => (
                              <div key={scan.id}>
                                {formatActiveScanMode(scan)}
                                {scan.status === "running" ? " running" : " queued"}
                                {scan.path ? ` · ${scan.path}` : ""}
                              </div>
                            ))}
                            {activeLibraryScans.length > 2 ? (
                              <div>+{activeLibraryScans.length - 2} more scan(s)</div>
                            ) : null}
                          </div>
                        ) : null}
                      </TableCell>
                    </SortableLibraryRow>
                  );
                })}
                {orderedLibraries
                  .filter((lib) => lib.scan_warning_code === "empty_root")
                  .map((lib) => {
                    const mountCheck = lastMountCheckByLibraryId[lib.id];
                    return (
                      <TableRow key={`${lib.id}-warning`}>
                        <TableCell colSpan={7} className="bg-destructive/5 text-sm">
                          <div className="flex flex-col gap-2 py-1">
                            <div className="text-destructive font-medium">
                              Scan found 0 media files for this library. Cleanup was paused to avoid
                              accidental deletion.
                            </div>
                            <div className="text-muted-foreground">
                              {lib.scan_warning_message ??
                                "Run another scan after storage returns, or confirm deletion before the next empty-root scan."}
                            </div>
                            <div>
                              <Button
                                variant="outline"
                                size="sm"
                                disabled={
                                  mountCheckMutation.isPending &&
                                  mountCheckMutation.variables === lib.id
                                }
                                onClick={() => handleMountCheck(lib.id)}
                              >
                                <HardDrive
                                  className={cn(
                                    "mr-1 h-3.5 w-3.5",
                                    mountCheckMutation.isPending &&
                                      mountCheckMutation.variables === lib.id &&
                                      "animate-pulse",
                                  )}
                                />
                                Check Mount
                              </Button>
                            </div>
                            {mountCheck ? (
                              <MountCheckInlineResult result={mountCheck} warning={true} />
                            ) : null}
                          </div>
                        </TableCell>
                      </TableRow>
                    );
                  })}
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
                  <TableCell className="text-muted-foreground font-mono text-xs">
                    {activeLibrary.paths.length === 1
                      ? activeLibrary.paths[0]
                      : `${activeLibrary.paths.length} folders`}
                  </TableCell>
                  <TableCell>
                    <Badge variant="secondary">{activeLibrary.type}</Badge>
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

      <UnmatchedItemsSection />
      <AmbiguousRootsSection libraries={libraries} />
      {skippedRoots.length > 0 ? <SkippedRootsSection skippedRoots={skippedRoots} /> : null}
      {staleIDs.length > 0 && <StaleIDsSection staleIDs={staleIDs} />}
    </div>
  );
}

function ScanQueuePopover({
  groups,
  cancellingLibraryID,
  onCancel,
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
}) {
  const totalRunning = groups.reduce((sum, group) => sum + group.runningCount, 0);
  const totalQueued = groups.reduce((sum, group) => sum + group.queuedCount, 0);
  const totalScans = totalRunning + totalQueued;

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button variant="outline" size="sm" className="gap-2">
          <span className="relative flex h-2 w-2">
            <span className="bg-success absolute inline-flex h-full w-full animate-ping rounded-full opacity-75" />
            <span className="bg-success relative inline-flex h-2 w-2 rounded-full" />
          </span>
          <span className="tabular-nums">
            {totalScans} {totalScans === 1 ? "scan" : "scans"}
          </span>
        </Button>
      </PopoverTrigger>

      <PopoverContent align="end" className="w-[400px] p-0">
        {/* Accent bar */}
        <div className="scan-queue-accent absolute inset-x-0 top-0 h-px rounded-t-xl" />

        {/* Header */}
        <div className="border-border/40 flex items-center justify-between border-b px-4 py-3">
          <div className="flex items-center gap-2.5">
            <RefreshCw
              className="text-primary h-3.5 w-3.5 animate-spin"
              style={{ animationDuration: "3s" }}
            />
            <span className="text-sm font-semibold">Scan Queue</span>
          </div>
          <div className="text-muted-foreground flex items-center gap-1.5 text-[11px]">
            {totalRunning > 0 && (
              <>
                <span className="bg-success inline-block h-1.5 w-1.5 rounded-full" />
                <span className="tabular-nums">{totalRunning} running</span>
              </>
            )}
            {totalRunning > 0 && totalQueued > 0 && <span className="text-border">·</span>}
            {totalQueued > 0 && (
              <>
                <span className="bg-muted-foreground/40 inline-block h-1.5 w-1.5 rounded-full" />
                <span className="tabular-nums">{totalQueued} queued</span>
              </>
            )}
          </div>
        </div>

        {/* Library groups */}
        <div className="max-h-[360px] overflow-y-auto px-3 py-2">
          {groups.map((group, gi) => (
            <div key={group.libraryID}>
              {/* Library header row */}
              <div className="flex items-center gap-2 py-1.5">
                <span className="text-muted-foreground/60 text-[10px] font-semibold tracking-widest uppercase">
                  {getLibraryScanGroupName(group.library, group.libraryID)}
                </span>
                <div className="bg-border/25 h-px flex-1" />
                <Button
                  variant="ghost"
                  size="sm"
                  className="text-muted-foreground hover:text-destructive h-6 gap-1 px-2 text-[10px]"
                  disabled={cancellingLibraryID === group.libraryID}
                  onClick={() => onCancel(group.libraryID)}
                >
                  <Square className="h-2.5 w-2.5" />
                  Cancel
                </Button>
              </div>

              {/* Scan rows */}
              {group.scans.map((scan, si) => (
                <div
                  key={scan.id}
                  className={cn(
                    "flex items-start gap-2.5 rounded-lg px-2.5 py-2 transition-colors",
                    scan.status === "running" ? "bg-primary/[0.04]" : "",
                  )}
                  style={{
                    animation: "fade-in 0.25s ease-out backwards",
                    animationDelay: `${(gi * 4 + si) * 40}ms`,
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
                      <span className="text-xs leading-snug font-medium">
                        {formatActiveScanMode(scan)}
                      </span>
                      {scan.trigger && (
                        <span className="bg-muted/50 text-muted-foreground rounded px-1 py-px text-[9px] font-medium">
                          {formatActiveScanTrigger(scan.trigger)}
                        </span>
                      )}
                    </div>
                    <div className="text-muted-foreground mt-px flex items-center gap-1 text-[10px] leading-relaxed">
                      {scan.path ? (
                        <code className="text-muted-foreground/70 max-w-[200px] truncate font-mono">
                          {scan.path}
                        </code>
                      ) : (
                        <span>Entire library</span>
                      )}
                      <span className="text-border/50 shrink-0">·</span>
                      <span className="shrink-0">
                        {scan.status === "running"
                          ? formatActiveScanTime(scan.started_at, "Started")
                          : "Waiting for capacity"}
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

              {/* Separator between groups */}
              {gi < groups.length - 1 && <div className="bg-border/20 my-1 h-px" />}
            </div>
          ))}
        </div>
      </PopoverContent>
    </Popover>
  );
}

function getLibraryScanGroupName(library: Library | null, libraryID: number) {
  return library?.name ?? `Library #${libraryID}`;
}

function getLibraryRefreshLibraryID(job: AdminJob): number | null {
  const value = (job.request_payload as { library_id?: unknown }).library_id;
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  if (typeof value === "string") {
    const parsed = Number.parseInt(value, 10);
    return Number.isFinite(parsed) ? parsed : null;
  }
  return null;
}

function SortableLibraryRow({ id, children }: { id: number; children: React.ReactNode }) {
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
          aria-label="Drag to reorder"
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

function MountCheckInlineResult({
  result,
  warning,
}: {
  result: LibraryMountCheckResponse;
  warning: boolean;
}) {
  const failingRoots = result.roots.filter((root) => !root.reachable);

  return (
    <div
      className={cn(
        "mt-2 space-y-1 rounded-md border px-2 py-1.5 text-xs",
        result.healthy
          ? "border-success/25 bg-success/5 text-success"
          : "border-destructive/25 bg-destructive/5 text-destructive",
      )}
    >
      <div className="font-medium">{result.summary}</div>
      {result.healthy && warning ? (
        <div className="text-foreground/70">
          Storage looks available again. Run Scan Library to verify contents and clear the warning.
        </div>
      ) : null}
      {!result.healthy
        ? failingRoots.map((root) => (
            <div key={root.path} className="text-foreground/70">
              <span className="font-mono">{root.path}</span>
              {root.error_message ? `: ${root.error_message}` : ""}
            </div>
          ))
        : null}
      <div className="text-foreground/50">
        Checked {new Date(result.checked_at).toLocaleString()}
      </div>
    </div>
  );
}

/* ─── Pagination helper ─────────────────────────────────────────── */

const PAGE_SIZE = 10;

function usePagination<T>(items: T[], pageSize = PAGE_SIZE) {
  const [page, setPage] = useState(0);
  const totalPages = Math.max(1, Math.ceil(items.length / pageSize));
  const clamped = Math.min(page, totalPages - 1);

  const slice = useMemo(
    () => items.slice(clamped * pageSize, clamped * pageSize + pageSize),
    [items, clamped, pageSize],
  );

  return {
    page: clamped,
    totalPages,
    total: items.length,
    rows: slice,
    setPage,
    canPrev: clamped > 0,
    canNext: clamped < totalPages - 1,
    first: () => setPage(0),
    prev: () => setPage((p) => Math.max(0, p - 1)),
    next: () => setPage((p) => Math.min(totalPages - 1, p + 1)),
    last: () => setPage(totalPages - 1),
    // 1-indexed display range
    rangeStart: items.length === 0 ? 0 : clamped * pageSize + 1,
    rangeEnd: Math.min((clamped + 1) * pageSize, items.length),
  };
}

function PaginationBar({
  total,
  rangeStart,
  rangeEnd,
  canPrev,
  canNext,
  first,
  prev,
  next,
  last,
}: {
  total: number;
  rangeStart: number;
  rangeEnd: number;
  canPrev: boolean;
  canNext: boolean;
  first: () => void;
  prev: () => void;
  next: () => void;
  last: () => void;
}) {
  if (total <= PAGE_SIZE) return null;
  return (
    <div className="border-border/40 flex items-center justify-between border-t px-1 pt-3">
      <span className="text-muted-foreground text-xs tracking-tight tabular-nums">
        {rangeStart}&ndash;{rangeEnd} of {total}
      </span>
      <div className="flex items-center gap-0.5">
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7"
          disabled={!canPrev}
          onClick={first}
          title="First page"
        >
          <ChevronsLeft className="h-3.5 w-3.5" />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7"
          disabled={!canPrev}
          onClick={prev}
          title="Previous page"
        >
          <ChevronLeft className="h-3.5 w-3.5" />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7"
          disabled={!canNext}
          onClick={next}
          title="Next page"
        >
          <ChevronRight className="h-3.5 w-3.5" />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7"
          disabled={!canNext}
          onClick={last}
          title="Last page"
        >
          <ChevronsRight className="h-3.5 w-3.5" />
        </Button>
      </div>
    </div>
  );
}

/* ─── Sortable column header ────────────────────────────────────── */

type SortDir = "asc" | "desc";

function SortableHead<K extends string>({
  field,
  activeField,
  activeDir,
  onSort,
  children,
  className,
}: {
  field: K;
  activeField: K;
  activeDir: SortDir;
  onSort: (field: K) => void;
  children: React.ReactNode;
  className?: string;
}) {
  const active = field === activeField;
  return (
    <TableHead className={cn("select-none", className)}>
      <button
        type="button"
        className="hover:text-foreground inline-flex items-center gap-1 transition-colors"
        onClick={() => onSort(field)}
      >
        {children}
        {active ? (
          activeDir === "asc" ? (
            <ChevronUp className="h-3 w-3" />
          ) : (
            <ChevronDown className="h-3 w-3" />
          )
        ) : (
          <ChevronDown className="h-3 w-3 opacity-0 group-hover:opacity-30" />
        )}
      </button>
    </TableHead>
  );
}

function useSort<K extends string>(defaultField: K, defaultDir: SortDir = "desc") {
  const [sortField, setSortField] = useState<K>(defaultField);
  const [sortDir, setSortDir] = useState<SortDir>(defaultDir);

  const toggle = useCallback(
    (field: K) => {
      if (field === sortField) {
        setSortDir((d) => (d === "asc" ? "desc" : "asc"));
      } else {
        setSortField(field);
        setSortDir("asc");
      }
    },
    [sortField],
  );

  return { sortField, sortDir, toggle };
}

/* ─── Skipped Roots (Troubleshooting) ───────────────────────────── */

function AmbiguousRootsSection({ libraries }: { libraries: Library[] }) {
  const [selectedLibraryId, setSelectedLibraryId] = useState<number | undefined>(libraries[0]?.id);
  const [search, setSearch] = useState("");
  const [editingRoot, setEditingRoot] = useState<LibraryRoot | null>(null);
  const effectiveSelectedLibraryId = selectedLibraryId ?? libraries[0]?.id;
  const { data: roots = [] } = useLibraryRoots(effectiveSelectedLibraryId, "ambiguous");

  const filteredRoots = useMemo(() => {
    if (!search) return roots;
    const q = search.toLowerCase();
    return roots.filter(
      (root) =>
        root.root_path.toLowerCase().includes(q) ||
        root.title.toLowerCase().includes(q) ||
        (root.sample_file_path ?? "").toLowerCase().includes(q),
    );
  }, [roots, search]);

  const pag = usePagination(filteredRoots);

  if (libraries.length === 0) {
    return null;
  }

  return (
    <section className="surface-panel-subtle overflow-hidden rounded-2xl">
      <div className="flex items-start gap-3 px-5 pt-5 pb-4">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-amber-500/10">
          <FolderOpen className="h-4 w-4 text-amber-500" />
        </div>
        <div className="space-y-0.5">
          <h2 className="text-sm font-semibold tracking-wide">Ambiguous Roots</h2>
          <p className="text-muted-foreground text-xs leading-relaxed">
            Scanner roots that stay visible but do not enter unattended metadata matching.
          </p>
        </div>
        <Badge variant="secondary" className="ml-auto text-[11px] tabular-nums">
          {roots.length}
        </Badge>
      </div>

      <div className="px-3 pb-3">
        <div className="mb-2 flex flex-col gap-2 sm:flex-row">
          <Select
            value={
              effectiveSelectedLibraryId != null ? String(effectiveSelectedLibraryId) : undefined
            }
            onValueChange={(value) => {
              setSelectedLibraryId(Number.parseInt(value, 10));
              pag.setPage(0);
            }}
          >
            <SelectTrigger className="h-8 w-full text-xs sm:w-[220px]">
              <SelectValue placeholder="Select library" />
            </SelectTrigger>
            <SelectContent>
              {libraries.map((library) => (
                <SelectItem key={library.id} value={String(library.id)}>
                  {library.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <div className="relative flex-1">
            <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2" />
            <Input
              placeholder="Filter by path, title, or sample file..."
              value={search}
              onChange={(e) => {
                setSearch(e.target.value);
                pag.setPage(0);
              }}
              className="h-8 pl-8 text-xs"
            />
          </div>
        </div>

        <div className="border-border/40 bg-background/40 overflow-x-auto rounded-xl border">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Root</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Confidence</TableHead>
                <TableHead className="text-right">Files</TableHead>
                <TableHead className="w-[120px]">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {filteredRoots.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={5} className="text-muted-foreground text-center text-sm">
                    No ambiguous roots for this library.
                  </TableCell>
                </TableRow>
              ) : (
                pag.rows.map((root) => (
                  <TableRow key={`${root.library_id}:${root.root_path}`}>
                    <TableCell className="max-w-[28rem]">
                      <div className="space-y-1">
                        <div className="truncate text-sm font-medium">
                          {root.title || root.root_path.split("/").filter(Boolean).pop()}
                        </div>
                        <code className="text-muted-foreground block truncate text-[11px]">
                          {root.root_path}
                        </code>
                        {root.evidence_json ? (
                          <div className="text-muted-foreground truncate text-[11px]">
                            {buildRootEvidenceSummary(root)}
                          </div>
                        ) : null}
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline">{root.inferred_type || "unknown"}</Badge>
                    </TableCell>
                    <TableCell>
                      <Badge variant="secondary">{root.type_confidence}</Badge>
                    </TableCell>
                    <TableCell className="text-muted-foreground text-right text-xs tabular-nums">
                      {root.observed_file_count}
                    </TableCell>
                    <TableCell>
                      <Button
                        variant="outline"
                        size="sm"
                        className="h-7 text-xs"
                        onClick={() => setEditingRoot(root)}
                      >
                        Override
                      </Button>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </div>
        <PaginationBar {...pag} />
      </div>

      {editingRoot ? (
        <RootOverrideDialog
          key={`${editingRoot.library_id}:${editingRoot.root_path}`}
          root={editingRoot}
          onOpenChange={(open) => {
            if (!open) setEditingRoot(null);
          }}
        />
      ) : null}
    </section>
  );
}

function buildRootEvidenceSummary(root: LibraryRoot): string {
  const evidence = root.evidence_json ?? {};
  const parts: string[] = [];

  if (typeof evidence.has_folder_ids === "boolean") {
    parts.push(evidence.has_folder_ids ? "folder IDs" : "no folder IDs");
  }
  if (typeof evidence.season_structure_files === "number" && evidence.season_structure_files > 0) {
    parts.push(`${evidence.season_structure_files} season-structured files`);
  }
  if (typeof evidence.movie_evidence_files === "number" && evidence.movie_evidence_files > 0) {
    parts.push(`${evidence.movie_evidence_files} movie-shaped files`);
  }
  if (typeof evidence.wrapper_collapses === "number" && evidence.wrapper_collapses > 0) {
    parts.push(`${evidence.wrapper_collapses} wrapper collapses`);
  }
  if (typeof evidence.ancestor_promotions === "number" && evidence.ancestor_promotions > 0) {
    parts.push(`${evidence.ancestor_promotions} ancestor promotions`);
  }

  return parts.join(" · ");
}

function RootOverrideDialog({
  root,
  onOpenChange,
}: {
  root: LibraryRoot;
  onOpenChange: (open: boolean) => void;
}) {
  const upsertOverride = useUpsertLibraryRootOverride();
  const deleteOverride = useDeleteLibraryRootOverride();
  const [forcedType, setForcedType] = useState(
    root.active_override?.forced_type || root.inferred_type || "",
  );
  const [forcedTitle, setForcedTitle] = useState(
    root.active_override?.forced_title || root.title || "",
  );
  const [forcedYear, setForcedYear] = useState(
    root.active_override?.forced_year
      ? String(root.active_override.forced_year)
      : root.year
        ? String(root.year)
        : "",
  );
  const [forcedTmdbID, setForcedTmdbID] = useState(
    root.active_override?.forced_tmdb_id || root.tmdb_id || "",
  );
  const [forcedImdbID, setForcedImdbID] = useState(
    root.active_override?.forced_imdb_id || root.imdb_id || "",
  );
  const [forcedTvdbID, setForcedTvdbID] = useState(
    root.active_override?.forced_tvdb_id || root.tvdb_id || "",
  );
  const [note, setNote] = useState(root.active_override?.note || "");

  return (
    <Dialog open={true} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Root Override</DialogTitle>
          <DialogDescription>
            Force the inferred identity for <code>{root.root_path}</code>.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label>Type</Label>
              <Select
                value={forcedType || "auto"}
                onValueChange={(value) => setForcedType(value === "auto" ? "" : value)}
              >
                <SelectTrigger>
                  <SelectValue placeholder="Auto" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="auto">Auto</SelectItem>
                  <SelectItem value="movie">Movie</SelectItem>
                  <SelectItem value="series">Series</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label>Year</Label>
              <Input
                value={forcedYear}
                onChange={(e) => setForcedYear(e.target.value)}
                placeholder="2024"
              />
            </div>
          </div>

          <div className="space-y-1.5">
            <Label>Title</Label>
            <Input
              value={forcedTitle}
              onChange={(e) => setForcedTitle(e.target.value)}
              placeholder="Forced title"
            />
          </div>

          <div className="grid gap-3 sm:grid-cols-3">
            <div className="space-y-1.5">
              <Label>TMDB ID</Label>
              <Input value={forcedTmdbID} onChange={(e) => setForcedTmdbID(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label>IMDb ID</Label>
              <Input value={forcedImdbID} onChange={(e) => setForcedImdbID(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label>TVDB ID</Label>
              <Input value={forcedTvdbID} onChange={(e) => setForcedTvdbID(e.target.value)} />
            </div>
          </div>

          <div className="space-y-1.5">
            <Label>Note</Label>
            <Input
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder="Why this override exists"
            />
          </div>

          <div className="flex justify-between gap-2">
            <Button
              variant="outline"
              disabled={!root.active_override || deleteOverride.isPending}
              onClick={() => {
                deleteOverride.mutate(
                  { library_id: root.library_id, root_path: root.root_path },
                  { onSuccess: () => onOpenChange(false) },
                );
              }}
            >
              Remove Override
            </Button>
            <Button
              onClick={() => {
                const parsedYear = Number.parseInt(forcedYear, 10);
                upsertOverride.mutate(
                  {
                    library_id: root.library_id,
                    root_path: root.root_path,
                    forced_type: forcedType || undefined,
                    forced_title: forcedTitle || undefined,
                    forced_year: Number.isFinite(parsedYear) ? parsedYear : undefined,
                    forced_tmdb_id: forcedTmdbID || undefined,
                    forced_imdb_id: forcedImdbID || undefined,
                    forced_tvdb_id: forcedTvdbID || undefined,
                    note: note || undefined,
                  },
                  { onSuccess: () => onOpenChange(false) },
                );
              }}
              disabled={upsertOverride.isPending}
            >
              Save Override
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

type SkippedSortField = "root_path" | "library" | "reason" | "first_seen" | "last_seen";

function SkippedRootsSection({ skippedRoots }: { skippedRoots: LibrarySkippedRoot[] }) {
  const [search, setSearch] = useState("");
  const [expandedKey, setExpandedKey] = useState<string | null>(null);
  const { sortField, sortDir, toggle } = useSort<SkippedSortField>("last_seen", "desc");

  const filtered = useMemo(() => {
    if (!search) return skippedRoots;
    const q = search.toLowerCase();
    return skippedRoots.filter(
      (r) =>
        r.root_path.toLowerCase().includes(q) ||
        r.library_name.toLowerCase().includes(q) ||
        r.reason.toLowerCase().includes(q),
    );
  }, [skippedRoots, search]);

  const sorted = useMemo(() => {
    const cmp = sortDir === "asc" ? 1 : -1;
    return [...filtered].sort((a, b) => {
      switch (sortField) {
        case "root_path":
          return cmp * a.root_path.localeCompare(b.root_path);
        case "library":
          return cmp * a.library_name.localeCompare(b.library_name);
        case "reason":
          return cmp * a.reason.localeCompare(b.reason);
        case "first_seen":
          return cmp * a.first_seen_at.localeCompare(b.first_seen_at);
        case "last_seen":
          return cmp * a.last_seen_at.localeCompare(b.last_seen_at);
        default:
          return 0;
      }
    });
  }, [filtered, sortField, sortDir]);

  const pag = usePagination(sorted);

  return (
    <section className="surface-panel-subtle overflow-hidden rounded-2xl">
      <div className="flex items-start gap-3 px-5 pt-5 pb-4">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-amber-500/10">
          <AlertTriangle className="h-4 w-4 text-amber-500" />
        </div>
        <div className="space-y-0.5">
          <h2 className="text-sm font-semibold tracking-wide">Troubleshooting</h2>
          <p className="text-muted-foreground text-xs leading-relaxed">
            Roots where the inferred canonical folder lacks embedded provider IDs.
          </p>
        </div>
        <Badge variant="secondary" className="ml-auto text-[11px] tabular-nums">
          {skippedRoots.length}
        </Badge>
      </div>

      <div className="px-3 pb-3">
        <div className="relative mb-2">
          <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2" />
          <Input
            placeholder="Filter by path, library, or reason..."
            value={search}
            onChange={(e) => {
              setSearch(e.target.value);
              pag.setPage(0);
            }}
            className="h-8 pl-8 text-xs"
          />
        </div>
        <div className="border-border/40 bg-background/40 overflow-x-auto rounded-xl border">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <SortableHead
                  field="root_path"
                  activeField={sortField}
                  activeDir={sortDir}
                  onSort={toggle}
                >
                  Item
                </SortableHead>
                <SortableHead
                  field="library"
                  activeField={sortField}
                  activeDir={sortDir}
                  onSort={toggle}
                >
                  Library
                </SortableHead>
                <SortableHead
                  field="reason"
                  activeField={sortField}
                  activeDir={sortDir}
                  onSort={toggle}
                >
                  Reason
                </SortableHead>
                <TableHead className="text-right">Files</TableHead>
                <SortableHead
                  field="first_seen"
                  activeField={sortField}
                  activeDir={sortDir}
                  onSort={toggle}
                >
                  First seen
                </SortableHead>
                <SortableHead
                  field="last_seen"
                  activeField={sortField}
                  activeDir={sortDir}
                  onSort={toggle}
                >
                  Last seen
                </SortableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {pag.rows.map((root) => {
                const rowKey = `${root.library_id}:${root.root_path}`;
                const isExpanded = expandedKey === rowKey;
                return (
                  <Fragment key={rowKey}>
                    <TableRow
                      className="cursor-pointer"
                      onClick={() => setExpandedKey(isExpanded ? null : rowKey)}
                    >
                      <TableCell className="max-w-[20rem]">
                        <div className="flex items-center gap-2">
                          <ChevronRight
                            className={cn(
                              "text-muted-foreground h-3.5 w-3.5 shrink-0 transition-transform",
                              isExpanded && "rotate-90",
                            )}
                          />
                          <span className="truncate text-sm font-medium">
                            {root.root_path.split("/").filter(Boolean).pop()}
                          </span>
                        </div>
                      </TableCell>
                      <TableCell className="text-sm">{root.library_name}</TableCell>
                      <TableCell>
                        <Badge
                          variant="outline"
                          className="border-warning/30 bg-warning/5 text-warning"
                        >
                          {root.reason}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-right text-xs tabular-nums">
                        {root.file_count}
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs tabular-nums">
                        {new Date(root.first_seen_at).toLocaleString()}
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs tabular-nums">
                        {new Date(root.last_seen_at).toLocaleString()}
                      </TableCell>
                    </TableRow>
                    {isExpanded && (
                      <TableRow className="hover:bg-transparent">
                        <TableCell colSpan={6} className="bg-muted/30 border-b px-4 py-3">
                          <div className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-xs">
                            <span className="text-muted-foreground font-medium">Root path</span>
                            <code className="font-mono break-all select-all">{root.root_path}</code>
                            {root.sample_file_path && (
                              <>
                                <span className="text-muted-foreground font-medium">
                                  Sample file
                                </span>
                                <code className="font-mono break-all select-all">
                                  {root.sample_file_path}
                                </code>
                              </>
                            )}
                            <span className="text-muted-foreground font-medium">
                              Files affected
                            </span>
                            <span>{root.file_count}</span>
                          </div>
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                );
              })}
            </TableBody>
          </Table>
        </div>
        <PaginationBar {...pag} />
      </div>
    </section>
  );
}

/* ─── Unmatched Items ──────────────────────────────────────────── */

function UnmatchedItemsSection() {
  const [page, setPage] = useState(0);
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebounce(search, 250);
  const [matchItem, setMatchItem] = useState<UnmatchedLibraryItem | null>(null);
  const { data } = useUnmatchedLibraryItems(page, debouncedSearch);
  const total = data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / UNMATCHED_PAGE_SIZE));
  const clamped = Math.min(page, totalPages - 1);
  const rangeStart = total === 0 ? 0 : clamped * UNMATCHED_PAGE_SIZE + 1;
  const rangeEnd = Math.min((clamped + 1) * UNMATCHED_PAGE_SIZE, total);
  const items = data?.items ?? [];

  // Hide the section only when there are genuinely no unmatched items and no
  // active search — keep it mounted while searching so the box and the
  // "no matches" state stay visible even when a query returns nothing.
  if (total === 0 && page === 0 && search.trim() === "") return null;

  return (
    <section className="surface-panel-subtle overflow-hidden rounded-2xl">
      <div className="flex items-start gap-3 px-5 pt-5 pb-4">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-amber-500/10">
          <Search className="h-4 w-4 text-amber-500" />
        </div>
        <div className="space-y-0.5">
          <h2 className="text-sm font-semibold tracking-wide">Unmatched Items</h2>
          <p className="text-muted-foreground text-xs leading-relaxed">
            Items that could not be matched to any metadata provider.
          </p>
        </div>
        <Badge variant="secondary" className="ml-auto text-[11px] tabular-nums">
          {total}
        </Badge>
      </div>

      <div className="px-3 pb-3">
        <div className="relative mb-2">
          <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2" />
          <Input
            placeholder="Search all unmatched items by title, library, or type..."
            value={search}
            onChange={(e) => {
              // Search is server-side and spans the whole table; jump back to
              // the first page so results start at the top as the query changes.
              setSearch(e.target.value);
              setPage(0);
            }}
            className="h-8 pl-8 text-xs"
          />
        </div>
        <div className="border-border/40 bg-background/40 overflow-x-auto rounded-xl border">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Title</TableHead>
                <TableHead>Library</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="w-[100px]">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={5} className="text-muted-foreground text-center text-sm">
                    No unmatched items match your search.
                  </TableCell>
                </TableRow>
              ) : (
                items.map((u) => (
                  <TableRow key={u.content_id}>
                    <TableCell className="font-medium">
                      <Link
                        to={`/item/${u.content_id}`}
                        className="hover:text-primary underline-offset-2 hover:underline"
                      >
                        {u.title}
                      </Link>
                      {u.year ? (
                        <span className="text-muted-foreground ml-1 text-xs">({u.year})</span>
                      ) : null}
                    </TableCell>
                    <TableCell className="text-sm">{u.library_name}</TableCell>
                    <TableCell>
                      <Badge variant="outline">{u.content_type}</Badge>
                    </TableCell>
                    <TableCell>
                      <Badge variant="secondary">{u.status}</Badge>
                    </TableCell>
                    <TableCell>
                      <Button
                        variant="outline"
                        size="sm"
                        className="h-7 gap-1 text-xs"
                        onClick={() => setMatchItem(u)}
                      >
                        <Search className="h-3 w-3" />
                        Match
                      </Button>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </div>
        {total > UNMATCHED_PAGE_SIZE && (
          <PaginationBar
            total={total}
            rangeStart={rangeStart}
            rangeEnd={rangeEnd}
            canPrev={clamped > 0}
            canNext={clamped < totalPages - 1}
            first={() => setPage(0)}
            prev={() => setPage((p) => Math.max(0, p - 1))}
            next={() => setPage((p) => Math.min(totalPages - 1, p + 1))}
            last={() => setPage(totalPages - 1)}
          />
        )}
      </div>
      {matchItem && (
        <MatchItemDialog
          item={{
            content_id: matchItem.content_id,
            library_id: matchItem.library_id,
            title: matchItem.title,
            year: matchItem.year,
            type: matchItem.content_type as "movie" | "series" | "season" | "episode",
          }}
          open={true}
          onOpenChange={(open) => {
            if (!open) setMatchItem(null);
          }}
        />
      )}
    </section>
  );
}

/* ─── Stale External IDs ────────────────────────────────────────── */

type StaleSortField = "title" | "year" | "library" | "provider" | "first_seen" | "last_seen";

function StaleIDsSection({ staleIDs }: { staleIDs: StaleMediaID[] }) {
  const [search, setSearch] = useState("");
  const [matchItem, setMatchItem] = useState<StaleMediaID | null>(null);
  const { sortField, sortDir, toggle } = useSort<StaleSortField>("last_seen", "desc");

  const filtered = useMemo(() => {
    if (!search) return staleIDs;
    const q = search.toLowerCase();
    return staleIDs.filter(
      (s) =>
        s.title.toLowerCase().includes(q) ||
        s.provider_id.toLowerCase().includes(q) ||
        s.provider.toLowerCase().includes(q) ||
        s.library_name.toLowerCase().includes(q),
    );
  }, [staleIDs, search]);

  const sorted = useMemo(() => {
    const cmp = sortDir === "asc" ? 1 : -1;
    return [...filtered].sort((a, b) => {
      switch (sortField) {
        case "title":
          return cmp * a.title.localeCompare(b.title);
        case "year":
          return cmp * (a.year - b.year);
        case "library":
          return cmp * a.library_name.localeCompare(b.library_name);
        case "provider":
          return cmp * a.provider.localeCompare(b.provider);
        case "first_seen":
          return cmp * a.first_seen_at.localeCompare(b.first_seen_at);
        case "last_seen":
          return cmp * a.last_seen_at.localeCompare(b.last_seen_at);
        default:
          return 0;
      }
    });
  }, [filtered, sortField, sortDir]);

  const pag = usePagination(sorted);

  return (
    <section className="surface-panel-subtle overflow-hidden rounded-2xl">
      <div className="flex items-start gap-3 px-5 pt-5 pb-4">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-red-500/10">
          <Unlink className="h-4 w-4 text-red-400" />
        </div>
        <div className="space-y-0.5">
          <h2 className="text-sm font-semibold tracking-wide">Stale External IDs</h2>
          <p className="text-muted-foreground text-xs leading-relaxed">
            Provider IDs no longer resolve &mdash; metadata refresh will fail until re-matched.
          </p>
        </div>
        <Badge variant="secondary" className="ml-auto text-[11px] tabular-nums">
          {staleIDs.length}
        </Badge>
      </div>

      <div className="px-3 pb-3">
        <div className="relative mb-2">
          <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2" />
          <Input
            placeholder="Filter by title, provider, or ID..."
            value={search}
            onChange={(e) => {
              setSearch(e.target.value);
              pag.setPage(0);
            }}
            className="h-8 pl-8 text-xs"
          />
        </div>
        <div className="border-border/40 bg-background/40 overflow-x-auto rounded-xl border">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <SortableHead
                  field="title"
                  activeField={sortField}
                  activeDir={sortDir}
                  onSort={toggle}
                >
                  Title
                </SortableHead>
                <SortableHead
                  field="year"
                  activeField={sortField}
                  activeDir={sortDir}
                  onSort={toggle}
                >
                  Year
                </SortableHead>
                <SortableHead
                  field="library"
                  activeField={sortField}
                  activeDir={sortDir}
                  onSort={toggle}
                >
                  Library
                </SortableHead>
                <SortableHead
                  field="provider"
                  activeField={sortField}
                  activeDir={sortDir}
                  onSort={toggle}
                >
                  Provider
                </SortableHead>
                <TableHead>Provider ID</TableHead>
                <SortableHead
                  field="first_seen"
                  activeField={sortField}
                  activeDir={sortDir}
                  onSort={toggle}
                >
                  First seen
                </SortableHead>
                <SortableHead
                  field="last_seen"
                  activeField={sortField}
                  activeDir={sortDir}
                  onSort={toggle}
                >
                  Last seen
                </SortableHead>
                <TableHead className="w-[100px]">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {pag.rows.map((s) => (
                <TableRow key={`${s.content_id}:${s.provider}`}>
                  <TableCell className="font-medium">{s.title}</TableCell>
                  <TableCell className="tabular-nums">{s.year}</TableCell>
                  <TableCell className="text-sm">{s.library_name}</TableCell>
                  <TableCell>
                    <Badge variant="outline">{s.provider}</Badge>
                  </TableCell>
                  <TableCell className="font-mono text-xs">{s.provider_id}</TableCell>
                  <TableCell className="text-muted-foreground text-xs tabular-nums">
                    {new Date(s.first_seen_at).toLocaleString()}
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs tabular-nums">
                    {new Date(s.last_seen_at).toLocaleString()}
                  </TableCell>
                  <TableCell>
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-7 gap-1 text-xs"
                      onClick={() => setMatchItem(s)}
                    >
                      <Search className="h-3 w-3" />
                      Match
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
        <PaginationBar {...pag} />
      </div>
      {matchItem && (
        <MatchItemDialog
          item={{
            content_id: matchItem.content_id,
            library_id: matchItem.library_id,
            title: matchItem.title,
            year: matchItem.year,
            type: matchItem.content_type as "movie" | "series" | "season" | "episode",
          }}
          open={true}
          onOpenChange={(open) => {
            if (!open) setMatchItem(null);
          }}
        />
      )}
    </section>
  );
}

type LevelChainItem = {
  plugin_installation_id: number;
  capability_id: string;
  provider_slug: string;
  enabled: boolean;
};

function contentLevelsForType(libraryType: string): string[] {
  switch (libraryType) {
    case "series":
      return ["series", "season", "episode"];
    case "movies":
      return ["movie"];
    case "mixed":
      return ["movie", "series", "season", "episode"];
    default:
      return [];
  }
}

function buildDefaultLevelChains(
  metadataProviders: Array<{
    plugin_installation_id: number;
    capability_id: string;
    slug: string;
    defaultPriority: Record<string, number>;
  }>,
  libraryType: string,
): Record<string, LevelChainItem[]> {
  const defaultChain: Record<string, LevelChainItem[]> = {};
  for (const level of contentLevelsForType(libraryType)) {
    const sorted = [...metadataProviders].sort((a, b) => {
      const pa = a.defaultPriority[level] ?? 0;
      const pb = b.defaultPriority[level] ?? 0;
      if ((pa === 0) !== (pb === 0)) return pa === 0 ? 1 : -1;
      return pa - pb;
    });
    defaultChain[level] = sorted.map((provider) => ({
      plugin_installation_id: provider.plugin_installation_id,
      capability_id: provider.capability_id,
      provider_slug: provider.slug,
      enabled: (provider.defaultPriority[level] ?? 0) > 0,
    }));
  }
  return defaultChain;
}

function buildLevelChainsFromServer(
  currentChain: {
    levels?: Record<
      string,
      Array<{
        plugin_installation_id: number;
        capability_id: string;
        provider_slug: string;
        enabled: boolean;
      }>
    >;
  } | null,
  metadataProviders: Array<{
    plugin_installation_id: number;
    capability_id: string;
    slug: string;
    defaultPriority: Record<string, number>;
  }>,
  libraryType: string,
): Record<string, LevelChainItem[]> {
  const mapped: Record<string, LevelChainItem[]> = {};
  if (currentChain?.levels) {
    for (const [level, entries] of Object.entries(currentChain.levels)) {
      mapped[level] = entries.map((entry) => ({
        plugin_installation_id: entry.plugin_installation_id,
        capability_id: entry.capability_id,
        provider_slug: entry.provider_slug,
        enabled: entry.enabled,
      }));
    }
  }

  const defaults = buildDefaultLevelChains(metadataProviders, libraryType);
  for (const level of contentLevelsForType(libraryType)) {
    if (!mapped[level] || mapped[level].length === 0) {
      mapped[level] = defaults[level] ?? [];
    }
  }
  return mapped;
}

function contentLevelLabel(level: string): string {
  return level.charAt(0).toUpperCase() + level.slice(1);
}

function ProviderLevelSection({
  level,
  items,
  onReorder,
  onToggleEnabled,
}: {
  level: string;
  items: LevelChainItem[];
  onReorder: (items: LevelChainItem[]) => void;
  onToggleEnabled: (index: number) => void;
}) {
  const [collapsed, setCollapsed] = useState(false);

  const moveItem = (index: number, direction: -1 | 1) => {
    const newItems = [...items];
    const target = index + direction;
    if (target < 0 || target >= newItems.length) return;
    [newItems[index], newItems[target]] = [newItems[target]!, newItems[index]!];
    onReorder(newItems);
  };

  return (
    <div className="mb-3">
      <button
        type="button"
        onClick={() => setCollapsed(!collapsed)}
        className="text-primary hover:text-primary/80 mb-1.5 flex items-center gap-1.5 text-xs font-semibold tracking-wider uppercase"
      >
        {collapsed ? <ChevronRight className="h-3 w-3" /> : <ChevronDown className="h-3 w-3" />}
        {contentLevelLabel(level)}
      </button>
      {!collapsed && (
        <div className="flex flex-col gap-1">
          {items.map((item, i) => (
            <div
              key={`${item.plugin_installation_id}:${item.capability_id}`}
              className={cn(
                "flex items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm",
                item.enabled
                  ? "border-border bg-muted text-foreground"
                  : "border-border/50 bg-muted/30 text-muted-foreground",
              )}
            >
              <input
                type="checkbox"
                checked={item.enabled}
                onChange={() => onToggleEnabled(i)}
                className="h-3.5 w-3.5"
                style={{ accentColor: "var(--primary)" }}
              />
              <span className="flex-1 font-mono text-xs">{item.provider_slug}</span>
              <div className="flex gap-0.5">
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-5 w-5"
                  disabled={i === 0}
                  onClick={() => moveItem(i, -1)}
                >
                  <ArrowUp className="h-2.5 w-2.5" />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="h-5 w-5"
                  disabled={i === items.length - 1}
                  onClick={() => moveItem(i, 1)}
                >
                  <ArrowDown className="h-2.5 w-2.5" />
                </Button>
              </div>
              <span className="text-muted-foreground/70 font-mono text-[10px]">{i + 1}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function LibraryForm({
  library,
  chapterThumbnailsSupported,
  onClose,
}: {
  library: Library | null;
  chapterThumbnailsSupported: boolean;
  onClose: () => void;
}) {
  const [name, setName] = useState(library?.name ?? "");
  const [paths, setPaths] = useState<string[]>(library?.paths?.length ? library.paths : [""]);
  const [type, setType] = useState(library?.type ?? "movies");
  const [enabled, setEnabled] = useState(library?.enabled ?? true);
  const [metadataLanguage, setMetadataLanguage] = useState(library?.metadata_language ?? "en");
  const [chapterThumbnailsEnabled, setChapterThumbnailsEnabled] = useState(
    library?.chapter_thumbnails_enabled ?? false,
  );
  const [introDetectionEnabled, setIntroDetectionEnabled] = useState(
    library?.intro_detection_enabled ?? false,
  );
  const [levelChains, setLevelChains] = useState<Record<string, LevelChainItem[]>>({});
  const [chainDirty, setChainDirty] = useState(false);
  const [browserOpen, setBrowserOpen] = useState(false);

  const createMutation = useCreateLibrary();
  const updateMutation = useUpdateLibrary();
  const setChainMutation = useSetLibraryProviders();
  const { installations } = useAdminPlugins();
  const { data: currentChain } = useLibraryProviders(library?.id ?? null);

  // Derive available metadata providers from plugin installations with metadata_provider.v1 capabilities,
  // sorted by their declared default_priority for the current library type.
  const metadataProviders = useMemo(() => {
    const result: Array<{
      plugin_installation_id: number;
      capability_id: string;
      slug: string;
      defaultPriority: Record<string, number>;
    }> = [];
    for (const inst of installations) {
      if (!inst.enabled) continue;
      for (const cap of inst.capabilities ?? []) {
        if (cap.type === "metadata_provider.v1") {
          const dp =
            (cap.metadata?.default_priority as Record<string, number>) ??
            ((cap.metadata?.metadata as Record<string, unknown>)?.default_priority as Record<
              string,
              number
            >) ??
            {};
          result.push({
            plugin_installation_id: inst.id,
            capability_id: cap.id,
            slug: cap.display_name || cap.id,
            defaultPriority: dp,
          });
        }
      }
    }
    return result;
  }, [installations]);

  const isPending =
    createMutation.isPending || updateMutation.isPending || setChainMutation.isPending;

  const defaultLevelChains = useMemo(
    () => buildDefaultLevelChains(metadataProviders, type),
    [metadataProviders, type],
  );
  const resolvedLevelChains = useMemo(() => {
    if (!library) {
      return defaultLevelChains;
    }
    if (currentChain === undefined) {
      return levelChains;
    }
    return buildLevelChainsFromServer(currentChain, metadataProviders, type);
  }, [currentChain, defaultLevelChains, levelChains, library, metadataProviders, type]);
  const activeLevelChains = chainDirty ? levelChains : resolvedLevelChains;

  function updatePath(index: number, value: string) {
    const next = [...paths];
    next[index] = value;
    setPaths(next);
  }

  function addPath() {
    setPaths([...paths, ""]);
  }

  function removePath(index: number) {
    setPaths(paths.filter((_, i) => i !== index));
  }

  function handleBrowseSelect(selectedPaths: string[]) {
    const merged = [...paths.filter((path) => path.trim())];
    for (const selectedPath of selectedPaths) {
      if (!merged.includes(selectedPath)) {
        merged.push(selectedPath);
      }
    }
    setPaths(merged.length > 0 ? merged : [""]);
    setBrowserOpen(false);
  }

  function handleTypeChange(newType: string) {
    setType(newType);
    if (!library) {
      setLevelChains(buildDefaultLevelChains(metadataProviders, newType));
      setChainDirty(true);
    }
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const filteredPaths = paths.filter((p) => p.trim());
    if (filteredPaths.length === 0) return;

    const body: CreateLibraryRequest = {
      name,
      paths: filteredPaths,
      type,
      enabled,
      metadata_language: metadataLanguage,
      chapter_thumbnails_enabled: chapterThumbnailsEnabled,
      intro_detection_enabled: introDetectionEnabled,
    };

    if (library) {
      updateMutation.mutate(
        { id: library.id, body },
        {
          onSuccess: () => {
            if (chainDirty) {
              setChainMutation.mutate(
                {
                  id: library.id,
                  body: {
                    levels: Object.fromEntries(
                      Object.entries(activeLevelChains).map(([level, items]) => [
                        level,
                        items.map((item, i) => ({
                          plugin_installation_id: item.plugin_installation_id,
                          capability_id: item.capability_id,
                          priority: i,
                          enabled: item.enabled,
                        })),
                      ]),
                    ),
                  },
                },
                { onSuccess: onClose },
              );
            } else {
              onClose();
            }
          },
        },
      );
    } else {
      createMutation.mutate(body, {
        onSuccess: (created) => {
          if (chainDirty) {
            setChainMutation.mutate(
              {
                id: created.id,
                body: {
                  levels: Object.fromEntries(
                    Object.entries(activeLevelChains).map(([level, items]) => [
                      level,
                      items.map((item, i) => ({
                        plugin_installation_id: item.plugin_installation_id,
                        capability_id: item.capability_id,
                        priority: i,
                        enabled: item.enabled,
                      })),
                    ]),
                  ),
                },
              },
              { onSuccess: onClose },
            );
          } else {
            onClose();
          }
        },
      });
    }
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-3">
      <div className="grid grid-cols-[1fr_auto] items-end gap-3">
        <div className="space-y-1.5">
          <Label>Name</Label>
          <Input value={name} onChange={(e) => setName(e.target.value)} required />
        </div>
        <div className="flex items-center gap-2 pb-0.5">
          <Switch id="enabled-switch" checked={enabled} onCheckedChange={setEnabled} />
          <Label htmlFor="enabled-switch" className="text-muted-foreground text-xs">
            Enabled
          </Label>
        </div>
      </div>
      <div className="space-y-1.5">
        <Label>Paths</Label>
        {paths.map((p, i) => (
          <div key={i} className="flex gap-1">
            <PathAutocompleteInput
              value={p}
              onValueChange={(value) => updatePath(i, value)}
              placeholder="/mnt/media/movies"
              required
            />
            {paths.length > 1 && (
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="h-9 w-9 shrink-0"
                onClick={() => removePath(i)}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </Button>
            )}
          </div>
        ))}
        <div className="flex gap-1">
          <Button type="button" variant="outline" size="sm" onClick={addPath}>
            <Plus className="mr-1 h-3.5 w-3.5" /> Add Path
          </Button>
          <Button type="button" variant="outline" size="sm" onClick={() => setBrowserOpen(true)}>
            <FolderOpen className="mr-1 h-3.5 w-3.5" /> Browse
          </Button>
        </div>
        <FolderBrowser
          open={browserOpen}
          onOpenChange={setBrowserOpen}
          onSelect={handleBrowseSelect}
          existingPaths={paths.filter((path) => path.trim())}
        />
      </div>
      <div className="grid grid-cols-2 items-end gap-3">
        <div className="space-y-1.5">
          <Label>Type</Label>
          <Select value={type} onValueChange={handleTypeChange}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="movies">Movies</SelectItem>
              <SelectItem value="series">Series</SelectItem>
              <SelectItem value="mixed">Mixed</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-1.5">
          <Label>Metadata Language</Label>
          <Select value={metadataLanguage} onValueChange={setMetadataLanguage}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {LANGUAGES.map((lang) => (
                <SelectItem key={lang.code} value={lang.code}>
                  {lang.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
      <div className="rounded-xl border border-white/10 bg-white/5 p-3">
        <div className="flex items-start justify-between gap-3">
          <div className="space-y-1">
            <Label htmlFor="chapter-thumbnails-switch">Generate chapter thumbnails</Label>
            <p className="text-xs text-white/60">
              Stores chapter preview images in the configured public asset S3 bucket. Chapter
              markers and chapter menus still work without thumbnails.
            </p>
            {!chapterThumbnailsSupported ? (
              <p className="text-xs text-amber-300">
                Public asset S3 storage is required before this can be enabled.
              </p>
            ) : null}
          </div>
          <Switch
            id="chapter-thumbnails-switch"
            checked={chapterThumbnailsEnabled}
            disabled={!chapterThumbnailsSupported}
            onCheckedChange={setChapterThumbnailsEnabled}
          />
        </div>
      </div>
      <div className="rounded-xl border border-white/10 bg-white/5 p-3">
        <div className="flex items-start justify-between gap-3">
          <div className="space-y-1">
            <Label htmlFor="intro-detection-switch">Detect intro markers</Label>
            <p className="text-xs text-white/60">
              Runs background audio analysis for episodes in this library. Embedded intro chapters
              are used when available.
            </p>
          </div>
          <Switch
            id="intro-detection-switch"
            checked={introDetectionEnabled}
            onCheckedChange={setIntroDetectionEnabled}
          />
        </div>
      </div>
      {library && (
        <div>
          <LibraryPosterSection library={library} />
        </div>
      )}

      {/* Per-level Metadata Provider Sections */}
      {contentLevelsForType(type).length > 0 && (
        <div className="mt-4 border-t border-white/10 pt-4">
          <h3 className="mb-3 text-sm font-semibold text-white">Metadata Providers</h3>
          {contentLevelsForType(type).map((level) => {
            const items = activeLevelChains[level] ?? [];
            return (
              <ProviderLevelSection
                key={level}
                level={level}
                items={items}
                onReorder={(newItems) => {
                  setLevelChains({ ...activeLevelChains, [level]: newItems });
                  setChainDirty(true);
                }}
                onToggleEnabled={(index) => {
                  setLevelChains((prev) => {
                    const source = prev[level] ?? activeLevelChains[level] ?? [];
                    const updated = [...source];
                    updated[index] = { ...updated[index]!, enabled: !updated[index]!.enabled };
                    return { ...prev, [level]: updated };
                  });
                  setChainDirty(true);
                }}
              />
            );
          })}
        </div>
      )}

      <Button type="submit" className="w-full" disabled={isPending}>
        {isPending ? "Saving..." : "Save"}
      </Button>
    </form>
  );
}

function LibraryPosterSection({ library }: { library: Library }) {
  const uploadMutation = useUploadLibraryPoster();
  const deleteMutation = useDeleteLibraryPoster();
  const fileInputId = `poster-upload-${library.id}`;

  function handleFileChange(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    if (!file) return;
    uploadMutation.mutate({ id: library.id, file });
    e.target.value = "";
  }

  return (
    <div className="space-y-1.5">
      <Label>Poster</Label>
      <div className="flex items-center gap-2">
        {library.poster_url ? (
          <img
            src={library.poster_url}
            alt={`${library.name} poster`}
            className="border-border h-14 flex-shrink-0 rounded border object-cover"
            style={{ aspectRatio: "16/9" }}
          />
        ) : (
          <div
            className="border-border bg-muted/30 flex h-14 flex-shrink-0 items-center justify-center rounded border border-dashed"
            style={{ aspectRatio: "16/9" }}
          >
            <ImageIcon className="text-muted-foreground/40 h-4 w-4" />
          </div>
        )}
        <input
          id={fileInputId}
          type="file"
          accept="image/jpeg,image/png,image/webp"
          className="hidden"
          onChange={handleFileChange}
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-8 text-xs"
          onClick={() => document.getElementById(fileInputId)?.click()}
          disabled={uploadMutation.isPending}
        >
          {uploadMutation.isPending ? "..." : library.poster_url ? "Replace" : "Upload"}
        </Button>
        {library.poster_url && (
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="text-muted-foreground hover:text-destructive h-8 w-8"
            onClick={() => deleteMutation.mutate(library.id)}
            disabled={deleteMutation.isPending}
            title="Remove poster"
          >
            <Trash2 className="h-3 w-3" />
          </Button>
        )}
      </div>
    </div>
  );
}
