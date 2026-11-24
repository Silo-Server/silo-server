import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";

import { captureProfileRequestContext } from "@/api/client";
import { captureAdminAuthority } from "@/api/v2/adminAuthority";
import { getUploadRestoreSet, startUploadRestore } from "@/api/v2/adminBackupUpload";
import {
  cancelRestoreJob,
  getRestoreJob,
  getRestoreSet,
  listRestoreJobs,
  startRestore,
  type RestoreJob,
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

/**
 * The preview of a source's restore set against this server; none while
 * `source` is null. `acceptUnauthenticated` opens a source this server can't
 * verify (an upload, or a backup under another key), which the admin has
 * confirmed.
 */
export function useRestoreSet(source: RestoreSource | null, acceptUnauthenticated = false) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...restoreRootKey(), "set", source?.kind, source?.id, acceptUnauthenticated],
    queryFn: () =>
      source?.kind === "upload"
        ? getUploadRestoreSet(captureAdminAuthority(), source.id, acceptUnauthenticated)
        : getRestoreSet(captureAdminAuthority(), source?.id ?? "", acceptUnauthenticated),
    enabled: context !== null && source !== null && source.id !== "",
    // A preview is compared against the server as it is now; read it fresh
    // each time the page opens rather than restoring from a stale cache.
    staleTime: 0,
    gcTime: 0,
    retry: false,
    // The chooser and review work from this plan and its digest: a refetch on
    // focus or reconnect could replace the plan under the admin, or turn a
    // failed read into an error screen. It is read again only when asked.
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
}

function restoreJobsKey() {
  return [...restoreRootKey(), "jobs"] as const;
}

function restoreJobKey(jobId: string | null) {
  return [...restoreRootKey(), "job", jobId] as const;
}

/** One restore job, read until it ends. */
export function useRestoreJob(jobId: string | null) {
  const context = captureProfileRequestContext();
  const queryClient = useQueryClient();
  const job = useQuery({
    queryKey: restoreJobKey(jobId),
    queryFn: () => getRestoreJob(captureAdminAuthority(), jobId!),
    enabled: context !== null && jobId !== null,
    refetchInterval: (query) => (query.state.data?.terminal ? false : RESTORE_POLL_MS),
  });
  const ended = job.data?.terminal ? job.data : null;
  // Once the job ends, put that in the jobs list and read the list again. A
  // list read sent before that can still be in flight, and a first read with
  // nothing cached yet is joined rather than replaced by a refetch, so drop it
  // first. Otherwise the list keeps saying running once this read leaves the
  // cache. The refetch joins a read the page has started since.
  useEffect(() => {
    if (!ended) return;
    const queryKey = restoreJobsKey();
    void queryClient.cancelQueries({ queryKey }).then(() => {
      queryClient.setQueryData<RestoreJob[]>(queryKey, (jobs) =>
        jobs?.map((item) => (item.id === ended.id ? ended : item)),
      );
      return queryClient.invalidateQueries({ queryKey }, { cancelRefetch: false });
    });
  }, [queryClient, ended]);
  return job;
}

/**
 * Recent restore jobs, to find the one for a backup after a reload. A job
 * whose own read says it ended shows as ended, even when this list was read
 * before that: the restore page opens a running job from this list, and would
 * otherwise send Choose again back to a finished run's report.
 */
export function useRestoreJobs() {
  const context = captureProfileRequestContext();
  const queryClient = useQueryClient();
  return useQuery({
    queryKey: restoreJobsKey(),
    queryFn: () => listRestoreJobs(captureAdminAuthority()),
    // Read on every render, not only when the list changes, so a job read
    // that lands after the list still counts.
    select: (jobs) =>
      jobs.map((job) => {
        if (job.terminal) return job;
        const read = queryClient.getQueryData<RestoreJob>(restoreJobKey(job.id));
        return read?.terminal ? read : job;
      }),
    enabled: context !== null,
    staleTime: 0,
  });
}

export function useStartRestore(source: RestoreSource) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      digest,
      selection,
      acceptUnauthenticated,
    }: {
      digest: string;
      selection: RestoreSelection;
      acceptUnauthenticated?: boolean;
    }) =>
      source.kind === "upload"
        ? startUploadRestore(
            captureAdminAuthority(),
            source.id,
            digest,
            selection,
            acceptUnauthenticated,
          )
        : startRestore(
            captureAdminAuthority(),
            source.id,
            digest,
            selection,
            acceptUnauthenticated,
          ),
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
