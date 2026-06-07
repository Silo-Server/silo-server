import { Fragment, useState, useEffect, useCallback, useMemo, useRef } from "react";
import type { ReactNode } from "react";
import { useDebounce } from "@/hooks/useDebounce";
import { useEventChannel } from "@/components/realtimeEventsContext";
import type {
  AdminJob,
  Library,
  LibraryMetadataMatchQueueStatus,
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
  useDeleteLibrary,
  useScanLibrary,
  useScanAllLibraries,
  useLibraryRefreshJobs,
  useRefreshLibraryMetadata,
  useCancelAdminJob,
  useConfirmEmptyRootCleanup,
  useLibraryMetadataMatchQueues,
  useUploadLibraryPoster,
  useDeleteLibraryPoster,
  useUnmatchedLibraryItems,
  UNMATCHED_PAGE_SIZE,
} from "@/hooks/queries/admin/libraries";
import { useActiveScans } from "@/hooks/queries/admin/scans";
import { buildLibraryReorderEntries } from "./adminLibraryOrder";
import MatchItemDialog from "@/components/MatchItemDialog";
import { LibraryForm } from "@/components/admin/libraries/LibraryForm";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
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
import { Link } from "react-router";
import {
  Plus,
  Pencil,
  Trash2,
  RefreshCw,
  ScanLine,
  DatabaseBackup,
  CheckCircle2,
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
import { cn } from "@/lib/utils";
import { toast } from "sonner";
import { getLibraryRefreshLibraryID } from "@/lib/adminJobs";
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

// Keep toast lifetime and delayed state cleanup coordinated so visible feedback
// disappears before state is cleared.
const MOUNT_CHECK_FEEDBACK_MS = 5_000;

export default function AdminLibraries() {
  useEventChannel("scans");
  const { data: libraries = [], isLoading } = useAdminLibraries();
  const { data: activeScans = [] } = useActiveScans();
  const { data: libraryRefreshJobs = [] } = useLibraryRefreshJobs();
  const { data: metadataMatchQueues = [] } = useLibraryMetadataMatchQueues();
  const { data: skippedRoots = [] } = useSkippedLibraryRoots();
  const { data: staleIDs = [] } = useStaleMediaIDs();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingLib, setEditingLib] = useState<Library | null>(null);
  const [confirmDeleteLib, setConfirmDeleteLib] = useState<Library | null>(null);
  const [confirmEmptyRootLib, setConfirmEmptyRootLib] = useState<Library | null>(null);
  const [lastMountCheckByLibraryId, setLastMountCheckByLibraryId] = useState<
    Record<number, LibraryMountCheckResponse>
  >({});
  const mountCheckClearTimeoutsRef = useRef<Record<number, number>>({});
  const deleteMutation = useDeleteLibrary();
  const mountCheckMutation = useCheckLibraryMount();
  const scanMutation = useScanLibrary();
  const scanAllMutation = useScanAllLibraries();
  const cancelScansMutation = useCancelLibraryScans();
  const cancelAdminJobMutation = useCancelAdminJob();

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

  useEffect(() => {
    return () => {
      // Cancel pending mount-check cleanup timers so unmount cannot clear state
      // after the page leaves.
      for (const timeoutID of Object.values(mountCheckClearTimeoutsRef.current)) {
        window.clearTimeout(timeoutID);
      }
    };
  }, []);

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
  const metadataMatchQueueByLibraryId = useMemo(() => {
    const queuesByLibraryID = new Map<number, LibraryMetadataMatchQueueStatus>();
    for (const queue of metadataMatchQueues) {
      if (queue.total_count > 0) {
        queuesByLibraryID.set(queue.library_id, queue);
      }
    }
    return queuesByLibraryID;
  }, [metadataMatchQueues]);
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
        if (result.healthy) {
          toast.success(formatMountCheckMessage(result), { duration: MOUNT_CHECK_FEEDBACK_MS });
        } else {
          toast.error(formatMountCheckMessage(result), { duration: MOUNT_CHECK_FEEDBACK_MS });
        }
        const existingTimeout = mountCheckClearTimeoutsRef.current[libraryId];
        if (existingTimeout) {
          window.clearTimeout(existingTimeout);
        }
        // Match the toast duration so the inline mount-check result stays visible
        // for the same window.
        mountCheckClearTimeoutsRef.current[libraryId] = window.setTimeout(() => {
          setLastMountCheckByLibraryId((current) => {
            const next = { ...current };
            delete next[libraryId];
            return next;
          });
          delete mountCheckClearTimeoutsRef.current[libraryId];
        }, MOUNT_CHECK_FEEDBACK_MS);
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
            <ScanLine className="h-3.5 w-3.5" />
            Scan All Libraries
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
                extraContent={
                  editingLib ? (
                    <div>
                      <LibraryPosterSection library={editingLib} />
                    </div>
                  ) : null
                }
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
                <TableHead className="w-[15rem]">Actions</TableHead>
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
                  const metadataMatchQueue = metadataMatchQueueByLibraryId.get(lib.id);
                  const hasMetadataMatchQueue = (metadataMatchQueue?.total_count ?? 0) > 0;
                  const runningLibraryScans = activeLibraryScans.filter(
                    (scan) => scan.status === "running",
                  ).length;
                  const queuedLibraryScans = activeLibraryScans.length - runningLibraryScans;
                  const isRefreshStarting =
                    refreshMutation.isPending && refreshMutation.variables === lib.id;
                  const isCheckingMount =
                    mountCheckMutation.isPending && mountCheckMutation.variables === lib.id;
                  const mountCheck = lastMountCheckByLibraryId[lib.id];
                  const hasActiveWork =
                    activeRefreshJob !== undefined || activeLibraryScans.length > 0;
                  const isCancellingLibraryScans =
                    cancelScansMutation.isPending && cancelScansMutation.variables === lib.id;
                  const isCancellingRefreshJob =
                    activeRefreshJob !== undefined &&
                    cancelAdminJobMutation.isPending &&
                    cancelAdminJobMutation.variables === activeRefreshJob.id;
                  return (
                    <Fragment key={lib.id}>
                      <SortableLibraryRow id={lib.id}>
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
                            {hasMetadataMatchQueue ? (
                              <Badge variant="secondary">
                                {metadataMatchQueue?.total_count.toLocaleString()} matching
                              </Badge>
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
                        <TableCell className="w-[15rem] align-middle">
                          <div className="flex flex-nowrap items-center justify-end gap-1">
                            <Button
                              variant="ghost"
                              size="icon"
                              className={cn(
                                "h-7 w-7",
                                activeLibraryScans.length > 0 && "text-destructive",
                              )}
                              title={
                                activeLibraryScans.length > 0
                                  ? "Stop Library Scans"
                                  : "Scan Library"
                              }
                              aria-label={
                                activeLibraryScans.length > 0
                                  ? "Stop Library Scans"
                                  : "Scan Library"
                              }
                              disabled={
                                activeLibraryScans.length > 0
                                  ? isCancellingLibraryScans
                                  : isScanning
                              }
                              onClick={() => {
                                if (activeLibraryScans.length > 0) {
                                  cancelScansMutation.mutate(lib.id);
                                  return;
                                }
                                scanMutation.mutate(lib.id);
                              }}
                            >
                              {activeLibraryScans.length > 0 ? (
                                <Square className="h-3 w-3 fill-current" />
                              ) : (
                                <RefreshCw
                                  className={`h-3 w-3 ${isScanning ? "animate-spin" : ""}`}
                                />
                              )}
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              className={cn("h-7 w-7", activeRefreshJob && "text-destructive")}
                              title={activeRefreshJob ? "Stop Metadata Refresh" : "Rescan Metadata"}
                              aria-label={
                                activeRefreshJob ? "Stop Metadata Refresh" : "Rescan Metadata"
                              }
                              disabled={
                                activeRefreshJob ? isCancellingRefreshJob : isRefreshStarting
                              }
                              onClick={() => {
                                if (activeRefreshJob) {
                                  cancelAdminJobMutation.mutate(activeRefreshJob.id);
                                  return;
                                }
                                refreshMutation.mutate(lib.id);
                              }}
                            >
                              {activeRefreshJob ? (
                                <Square className="h-3 w-3 fill-current" />
                              ) : (
                                <DatabaseBackup className="h-3 w-3" />
                              )}
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="h-7 w-7"
                              title={
                                mountCheck ? formatMountCheckMessage(mountCheck) : "Verify Mounts"
                              }
                              aria-label={
                                mountCheck ? formatMountCheckMessage(mountCheck) : "Verify Mounts"
                              }
                              disabled={isCheckingMount}
                              onClick={() => handleMountCheck(lib.id)}
                            >
                              <MountCheckButtonIcon pending={isCheckingMount} result={mountCheck} />
                            </Button>
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
                          </div>
                        </TableCell>
                      </SortableLibraryRow>
                      {hasActiveWork ? (
                        <LibraryActiveWorkRow
                          activeLibraryScans={activeLibraryScans}
                          activeRefreshJob={activeRefreshJob}
                          cancellingJobID={
                            cancelAdminJobMutation.isPending
                              ? cancelAdminJobMutation.variables
                              : undefined
                          }
                          cancellingLibraryID={
                            cancelScansMutation.isPending
                              ? cancelScansMutation.variables
                              : undefined
                          }
                          libraryID={lib.id}
                          onCancelJob={(jobID) => cancelAdminJobMutation.mutate(jobID)}
                          onCancelScans={(libraryID) => cancelScansMutation.mutate(libraryID)}
                        />
                      ) : null}
                    </Fragment>
                  );
                })}
                {orderedLibraries
                  .filter((lib) => lib.scan_warning_code === "empty_root")
                  .map((lib) => {
                    const mountCheck = lastMountCheckByLibraryId[lib.id];
                    const isCheckingMount =
                      mountCheckMutation.isPending && mountCheckMutation.variables === lib.id;
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
                                title={
                                  mountCheck ? formatMountCheckMessage(mountCheck) : "Check mount"
                                }
                                disabled={isCheckingMount}
                                onClick={() => handleMountCheck(lib.id)}
                              >
                                <MountCheckButtonIcon
                                  className="mr-1 h-3.5 w-3.5"
                                  pending={isCheckingMount}
                                  result={mountCheck}
                                />
                                Check Mount
                              </Button>
                            </div>
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

function LibraryActiveWorkRow({
  activeRefreshJob,
  activeLibraryScans,
  cancellingJobID,
  cancellingLibraryID,
  libraryID,
  onCancelJob,
  onCancelScans,
}: {
  activeRefreshJob: AdminJob | undefined;
  activeLibraryScans: ScanRun[];
  cancellingJobID?: string;
  cancellingLibraryID?: number;
  libraryID: number;
  onCancelJob: (jobID: string) => void;
  onCancelScans: (libraryID: number) => void;
}) {
  return (
    <TableRow className="hover:bg-transparent">
      <TableCell colSpan={7} className="bg-muted/20 px-4 py-2">
        <div className="text-muted-foreground flex flex-col gap-1.5 pl-12 text-[11px]">
          {activeRefreshJob ? (
            <div className="flex min-w-0 items-start gap-1.5">
              <StopTaskButton
                disabled={cancellingJobID === activeRefreshJob.id}
                label="Cancel metadata refresh"
                onClick={() => onCancelJob(activeRefreshJob.id)}
              />
              <DatabaseBackup className="mt-0.5 h-3 w-3 shrink-0" />
              <div className="min-w-0 flex-1 space-y-0.5">
                <div className="flex min-w-0 items-center gap-1.5">
                  <span className="text-foreground/80 font-medium">Metadata</span>
                  <span className="truncate">
                    {activeRefreshJob.message || "Metadata refresh queued"}
                  </span>
                </div>
                <div className="text-muted-foreground/80 truncate text-[10px]">
                  {formatJobProgress(activeRefreshJob)}
                </div>
              </div>
            </div>
          ) : null}
          {activeLibraryScans.map((scan) => (
            <LibraryScanTaskRow
              key={scan.id}
              scan={scan}
              cancelling={cancellingLibraryID === libraryID}
              libraryID={libraryID}
              onCancelScans={onCancelScans}
            />
          ))}
        </div>
      </TableCell>
    </TableRow>
  );
}

function LibraryScanTaskRow({
  scan,
  cancelling,
  libraryID,
  onCancelScans,
}: {
  scan: ScanRun;
  cancelling: boolean;
  libraryID: number;
  onCancelScans: (libraryID: number) => void;
}) {
  const progress = formatActiveScanProgress(scan);

  return (
    <div className="flex min-w-0 items-start gap-1.5">
      <StopTaskButton
        disabled={cancelling}
        label="Cancel library scans"
        onClick={() => onCancelScans(libraryID)}
      />
      <RefreshCw
        className={cn("mt-0.5 h-3 w-3 shrink-0", scan.status === "running" && "animate-spin")}
      />
      <div className="min-w-0 flex-1 space-y-0.5">
        <div className="flex min-w-0 items-center gap-1.5">
          <span className="text-foreground/80 font-medium">Scan</span>
          <span className="shrink-0">{scan.status === "running" ? "Running" : "Queued"}</span>
        </div>
        <div className="text-muted-foreground/80 truncate text-[10px]">
          {formatActiveScanMode(scan)}
          {scan.path ? ` · ${scan.path}` : " · Entire library"}
        </div>
        {progress ? (
          <div className="text-muted-foreground/80 truncate text-[10px]">{progress}</div>
        ) : null}
      </div>
    </div>
  );
}

function StopTaskButton({
  disabled,
  label,
  onClick,
}: {
  disabled?: boolean;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className="text-destructive hover:bg-destructive/10 hover:text-destructive focus-visible:ring-ring flex h-5 w-5 shrink-0 items-center justify-center rounded transition-colors focus-visible:ring-2 focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-50"
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={onClick}
    >
      <Square className="h-3 w-3 fill-current" />
    </button>
  );
}

function MountCheckButtonIcon({
  result,
  pending,
  className = "h-3 w-3",
}: {
  result?: LibraryMountCheckResponse;
  pending: boolean;
  className?: string;
}) {
  if (pending) {
    return <HardDrive className={cn(className, "animate-pulse")} />;
  }
  if (result?.healthy) {
    return <CheckCircle2 className={cn(className, "text-success")} />;
  }
  if (result) {
    return <AlertTriangle className={cn(className, "text-destructive")} />;
  }
  return <HardDrive className={className} />;
}

function formatMountCheckMessage(result: LibraryMountCheckResponse) {
  const failingRoots = result.roots.filter((root) => !root.reachable);
  const firstFailure = failingRoots[0];
  if (!result.healthy && firstFailure) {
    const detail = firstFailure.error_message ? ` (${firstFailure.error_message})` : "";
    return `${result.summary}: ${firstFailure.path}${detail}`;
  }
  return result.summary;
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

function CollapsibleDiagnosticsSection({
  title,
  description,
  count,
  icon,
  iconClassName,
  open,
  onOpenChange,
  children,
}: {
  title: string;
  description: string;
  count: number;
  icon: ReactNode;
  iconClassName?: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  children: ReactNode;
}) {
  return (
    <section className="surface-panel-subtle overflow-hidden rounded-2xl">
      <button
        type="button"
        className="hover:bg-surface-hover/40 flex w-full items-center gap-3 px-5 py-4 text-left transition-colors"
        aria-expanded={open}
        onClick={() => onOpenChange(!open)}
      >
        <div
          className={cn(
            "flex h-8 w-8 shrink-0 items-center justify-center rounded-lg",
            iconClassName ?? "bg-amber-500/10",
          )}
        >
          {icon}
        </div>
        <div className="min-w-0 flex-1 space-y-0.5">
          <h2 className="text-sm font-semibold tracking-wide">{title}</h2>
          <p className="text-muted-foreground text-xs leading-relaxed">{description}</p>
        </div>
        {open ? (
          <Badge variant="secondary" className="text-[11px] tabular-nums">
            {count}
          </Badge>
        ) : (
          <div className="text-2xl leading-none font-bold tabular-nums">{count}</div>
        )}
        <ChevronDown
          className={cn(
            "text-muted-foreground h-4 w-4 shrink-0 transition-transform",
            !open && "-rotate-90",
          )}
        />
      </button>
      {open ? <div className="px-3 pb-3">{children}</div> : null}
    </section>
  );
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
  const [open, setOpen] = useState(false);
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
    <CollapsibleDiagnosticsSection
      title="Ambiguous Roots"
      description="Scanner roots that stay visible but do not enter unattended metadata matching."
      count={roots.length}
      icon={<FolderOpen className="h-4 w-4 text-amber-500" />}
      open={open}
      onOpenChange={setOpen}
    >
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

      {editingRoot ? (
        <RootOverrideDialog
          key={`${editingRoot.library_id}:${editingRoot.root_path}`}
          root={editingRoot}
          onOpenChange={(open) => {
            if (!open) setEditingRoot(null);
          }}
        />
      ) : null}
    </CollapsibleDiagnosticsSection>
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
  const [open, setOpen] = useState(false);
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
    <CollapsibleDiagnosticsSection
      title="Unmatched Items"
      description="Items that could not be matched to any metadata provider."
      count={total}
      icon={<Search className="h-4 w-4 text-amber-500" />}
      open={open}
      onOpenChange={setOpen}
    >
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
    </CollapsibleDiagnosticsSection>
  );
}

/* ─── Stale External IDs ────────────────────────────────────────── */

type StaleSortField = "title" | "year" | "library" | "provider" | "first_seen" | "last_seen";

function StaleIDsSection({ staleIDs }: { staleIDs: StaleMediaID[] }) {
  const [open, setOpen] = useState(false);
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
    <CollapsibleDiagnosticsSection
      title="Stale External IDs"
      description="Provider IDs no longer resolve; metadata refresh will fail until re-matched."
      count={staleIDs.length}
      icon={<Unlink className="h-4 w-4 text-red-400" />}
      iconClassName="bg-red-500/10"
      open={open}
      onOpenChange={setOpen}
    >
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
    </CollapsibleDiagnosticsSection>
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
