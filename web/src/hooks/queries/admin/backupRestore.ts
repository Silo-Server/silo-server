import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { captureProfileRequestContext } from "@/api/client";
import { captureAdminAuthority } from "@/api/v2/adminAuthority";
import { getUploadRestoreSet, startUploadRestore } from "@/api/v2/adminBackupUpload";
import {
  cancelRestoreJob,
  getRestoreJob,
  getRestoreSet,
  listRestoreJobs,
  startRestore,
  type RestoreSelection,
} from "@/api/v2/adminBackupRestore";
import { backupsRootKey } from "@/hooks/queries/admin/backups";
import { adminKeys } from "@/hooks/queries/keys";

/** How often a running restore is read again. */
const RESTORE_POLL_MS = 2_000;

/** What a restore reads: a backup of this server, or an uploaded restore set. */
export interface RestoreSource {
  kind: "backup" | "upload";
  id: string;
}

function restoreRootKey() {
  return [...backupsRootKey(), "restore"] as const;
}

/** The preview of a source's restore set against this server; none while `source` is null. */
export function useRestoreSet(source: RestoreSource | null) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...restoreRootKey(), "set", source?.kind, source?.id],
    queryFn: () =>
      source?.kind === "upload"
        ? getUploadRestoreSet(captureAdminAuthority(), source.id)
        : getRestoreSet(captureAdminAuthority(), source?.id ?? ""),
    enabled: context !== null && source !== null && source.id !== "",
    // A preview is compared against the server as it is now; read it fresh
    // each time the page opens rather than restoring from a stale cache.
    staleTime: 0,
    gcTime: 0,
    retry: false,
  });
}

/** One restore job, read until it ends. */
export function useRestoreJob(jobId: string | null) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...restoreRootKey(), "job", jobId],
    queryFn: () => getRestoreJob(captureAdminAuthority(), jobId!),
    enabled: context !== null && jobId !== null,
    refetchInterval: (query) => (query.state.data?.terminal ? false : RESTORE_POLL_MS),
  });
}

/** Recent restore jobs, to find the one for a backup after a reload. */
export function useRestoreJobs() {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...restoreRootKey(), "jobs"],
    queryFn: () => listRestoreJobs(captureAdminAuthority()),
    enabled: context !== null,
    staleTime: 0,
  });
}

export function useStartRestore(source: RestoreSource) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ digest, selection }: { digest: string; selection: RestoreSelection }) =>
      source.kind === "upload"
        ? startUploadRestore(captureAdminAuthority(), source.id, digest, selection)
        : startRestore(captureAdminAuthority(), source.id, digest, selection),
    retry: false,
    onSettled: () => queryClient.invalidateQueries({ queryKey: adminKeys.backups() }),
  });
}

export function useCancelRestore() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (jobId: string) => cancelRestoreJob(captureAdminAuthority(), jobId),
    retry: false,
    onSettled: () => queryClient.invalidateQueries({ queryKey: restoreRootKey() }),
  });
}
