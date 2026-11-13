import { useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";

import type { AdminJob, Library, LibraryMountCheckResponse, ScanRun } from "@/api/types";
import {
  useCancelAdminJob,
  useCancelLibraryScans,
  useCheckLibraryMount,
  useConfirmEmptyRootCleanup,
  useRefreshLibraryMetadata,
  useScanLibrary,
} from "@/hooks/queries/admin/libraries";
import { useT, type Translate } from "@/i18n";
import { getLibraryRefreshLibraryID } from "@/lib/adminJobs";
import { compareActiveScans } from "@/lib/scanRuns";

// Keep toast lifetime and delayed state cleanup coordinated so visible feedback
// disappears before state is cleared.
const MOUNT_CHECK_FEEDBACK_MS = 5_000;

/**
 * Everything the libraries table row and the phone card show and do for one
 * library, so both layouts read the same state and call the same handlers.
 */
export interface LibraryRow {
  library: Library;
  activeScans: ScanRun[];
  runningScanCount: number;
  queuedScanCount: number;
  activeRefreshJob: AdminJob | undefined;
  hasActiveWork: boolean;
  /** Whether the library has a scan warning to explain under it. */
  hasScanWarning: boolean;
  /** Scan the library, or stop its scans while any are queued or running. */
  scan: { stop: boolean; pending: boolean; disabled: boolean; label: string; run: () => void };
  /** Open the metadata refresh dialog, or stop the active refresh job. */
  refresh: { stop: boolean; disabled: boolean; label: string; run: () => void };
  /** Check that the library's roots are reachable; the result shows for a few seconds. */
  mountCheck: {
    pending: boolean;
    result: LibraryMountCheckResponse | undefined;
    /** The last result as one sentence, while it is shown. */
    message: string | undefined;
    run: () => void;
  };
  /** Confirm deleting items under a missing or empty root, for those warnings. */
  cleanup: { available: boolean; pending: boolean; run: () => void };
  /** The refresh job being cancelled, if any. */
  cancellingJobID: string | undefined;
  /** Whether this library's scans are being cancelled. */
  cancellingScans: boolean;
  cancelJob: (jobID: string) => void;
  cancelScans: () => void;
  edit: () => void;
  remove: () => void;
}

interface Options {
  activeScans: ScanRun[];
  refreshJobs: AdminJob[];
  onEdit: (library: Library) => void;
  onDelete: (library: Library) => void;
  onRefresh: (library: Library) => void;
  onConfirmCleanup: (library: Library) => void;
}

/**
 * The per-library state and actions of the admin libraries list. The page
 * owns the dialogs; the mutations live here so the page and both layouts share
 * one pending state per action.
 */
export function useLibraryRows({
  activeScans,
  refreshJobs,
  onEdit,
  onDelete,
  onRefresh,
  onConfirmCleanup,
}: Options) {
  const t = useT();
  const scanMutation = useScanLibrary();
  const cancelScansMutation = useCancelLibraryScans();
  const cancelAdminJobMutation = useCancelAdminJob();
  const mountCheckMutation = useCheckLibraryMount();
  const refreshMutation = useRefreshLibraryMetadata();
  const confirmCleanupMutation = useConfirmEmptyRootCleanup();
  const [lastMountCheckByLibraryId, setLastMountCheckByLibraryId] = useState<
    Record<number, LibraryMountCheckResponse>
  >({});
  const mountCheckClearTimeoutsRef = useRef<Record<number, number>>({});

  useEffect(() => {
    const timeouts = mountCheckClearTimeoutsRef.current;
    return () => {
      // Cancel pending mount-check cleanup timers so unmount cannot clear state
      // after the page leaves.
      for (const timeoutID of Object.values(timeouts)) {
        window.clearTimeout(timeoutID);
      }
    };
  }, []);

  const activeRefreshJobsByLibraryId = useMemo(() => {
    const jobsByLibraryID = new Map<number, AdminJob>();
    for (const job of refreshJobs) {
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
  }, [refreshJobs]);

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

  function checkMount(libraryId: number) {
    mountCheckMutation.mutate(libraryId, {
      onSuccess: (result) => {
        setLastMountCheckByLibraryId((current) => ({
          ...current,
          [libraryId]: result,
        }));
        if (result.healthy) {
          toast.success(formatMountCheckMessage(t, result), { duration: MOUNT_CHECK_FEEDBACK_MS });
        } else {
          toast.error(formatMountCheckMessage(t, result), { duration: MOUNT_CHECK_FEEDBACK_MS });
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

  function rowFor(library: Library): LibraryRow {
    const id = library.id;
    const libraryScans = activeScansByLibraryId.get(id) ?? [];
    const runningScanCount = libraryScans.filter((scan) => scan.status === "running").length;
    const activeRefreshJob = activeRefreshJobsByLibraryId.get(id);
    const scansActive = libraryScans.length > 0;
    const isScanning = scanMutation.isPending && scanMutation.variables === id;
    const cancellingScans = cancelScansMutation.isPending && cancelScansMutation.variables === id;
    const cancellingJobID = cancelAdminJobMutation.isPending
      ? cancelAdminJobMutation.variables
      : undefined;
    const isRefreshStarting = refreshMutation.isPending && refreshMutation.variables?.id === id;
    const mountCheckResult = lastMountCheckByLibraryId[id];
    const cancelScans = () => cancelScansMutation.mutate(id);

    return {
      library,
      activeScans: libraryScans,
      runningScanCount,
      queuedScanCount: libraryScans.length - runningScanCount,
      activeRefreshJob,
      hasActiveWork: activeRefreshJob !== undefined || scansActive,
      hasScanWarning:
        library.scan_warning_code === "empty_root" ||
        library.scan_warning_code === "dead_root" ||
        library.scan_warning_code === "partial_walk",
      scan: {
        stop: scansActive,
        pending: isScanning,
        disabled: scansActive ? cancellingScans : isScanning,
        label: scansActive
          ? t("web.admin.libraries.list.stopScans")
          : t("web.admin.libraries.list.scanLibrary"),
        run: () => (scansActive ? cancelScans() : scanMutation.mutate(id)),
      },
      refresh: {
        stop: activeRefreshJob !== undefined,
        disabled: activeRefreshJob ? cancellingJobID === activeRefreshJob.id : isRefreshStarting,
        label: activeRefreshJob
          ? t("web.admin.libraries.list.stopRefresh")
          : t("web.admin.libraries.list.rescanMetadata"),
        run: () =>
          activeRefreshJob
            ? cancelAdminJobMutation.mutate(activeRefreshJob.id)
            : onRefresh(library),
      },
      mountCheck: {
        pending: mountCheckMutation.isPending && mountCheckMutation.variables === id,
        result: mountCheckResult,
        message: mountCheckResult ? formatMountCheckMessage(t, mountCheckResult) : undefined,
        run: () => checkMount(id),
      },
      cleanup: {
        available:
          library.scan_warning_code === "empty_root" || library.scan_warning_code === "dead_root",
        pending: confirmCleanupMutation.isPending && confirmCleanupMutation.variables === id,
        run: () => onConfirmCleanup(library),
      },
      cancellingJobID,
      cancellingScans,
      cancelJob: (jobID) => cancelAdminJobMutation.mutate(jobID),
      cancelScans,
      edit: () => onEdit(library),
      remove: () => onDelete(library),
    };
  }

  return {
    rowFor,
    activeScansByLibraryId,
    cancelScansMutation,
    refreshMutation,
    confirmCleanupMutation,
  };
}

export function formatMountCheckMessage(t: Translate, result: LibraryMountCheckResponse) {
  const failingRoots = result.roots.filter((root) => !root.reachable);
  const firstFailure = failingRoots[0];
  if (!result.healthy && firstFailure) {
    const values = { summary: result.summary, path: firstFailure.path };
    return firstFailure.error_message
      ? t("web.admin.libraries.mountCheck.failureWithError", {
          ...values,
          error: firstFailure.error_message,
        })
      : t("web.admin.libraries.mountCheck.failure", values);
  }
  return result.summary;
}
