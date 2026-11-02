import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { captureProfileRequestContext } from "@/api/client";
import { adminAuthorityScope, captureAdminAuthority } from "@/api/v2/adminAuthority";
import {
  acknowledgeAdminBackupKey,
  createAdminBackupDownloadLinks,
  deleteAdminBackup,
  getAdminBackupCapabilities,
  listAdminBackups,
  startAdminBackup,
  testAdminBackupS3,
  type AdminBackupDownloadScope,
  type AdminBackupList,
  type AdminBackupS3TestBody,
} from "@/api/v2/adminBackups";
import { adminKeys } from "@/hooks/queries/keys";

/** How often the list is read again while a backup runs. */
const RUNNING_POLL_MS = 3_000;

/** The root of every backup query, scoped to the admin authority. */
export function backupsRootKey() {
  return [...adminKeys.backups(), adminAuthorityScope()] as const;
}

/** Whether a backup is being taken now, on any server. */
export function backupRunning(list: AdminBackupList | undefined): boolean {
  return Boolean(
    list?.active?.operation === "backup" ||
    list?.items.some((item) => item.status === "in_progress"),
  );
}

export function useAdminBackupCapabilities() {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...backupsRootKey(), "capabilities"],
    queryFn: () => getAdminBackupCapabilities(captureAdminAuthority()),
    enabled: context !== null,
    staleTime: 60_000,
  });
}

export function useAdminBackups() {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...backupsRootKey(), "list"],
    queryFn: () => listAdminBackups(captureAdminAuthority()),
    enabled: context !== null,
    staleTime: 10_000,
    // Anything holding the lock (a backup, a delete, a restore) changes the
    // list when it ends, so keep reading while it is held.
    refetchInterval: (query) => {
      const list = query.state.data;
      return list?.busy || list?.active || backupRunning(list) ? RUNNING_POLL_MS : false;
    },
  });
}

function useInvalidateBackups() {
  const queryClient = useQueryClient();
  return () => queryClient.invalidateQueries({ queryKey: backupsRootKey() });
}

export function useStartAdminBackup() {
  const invalidate = useInvalidateBackups();
  return useMutation({
    mutationFn: () => startAdminBackup(captureAdminAuthority()),
    retry: false,
    onSettled: invalidate,
  });
}

export function useDeleteAdminBackup() {
  const invalidate = useInvalidateBackups();
  return useMutation({
    mutationFn: (id: string) => deleteAdminBackup(captureAdminAuthority(), id),
    retry: false,
    onSettled: invalidate,
  });
}

export function useAdminBackupDownloadLinks() {
  const invalidate = useInvalidateBackups();
  return useMutation({
    mutationFn: ({ id, scope = "full" }: { id: string; scope?: AdminBackupDownloadScope }) =>
      createAdminBackupDownloadLinks(captureAdminAuthority(), id, scope),
    retry: false,
    // A backup deleted elsewhere answers 404; refresh the list so the row goes,
    // without holding the error until the refetch finishes.
    onError: () => void invalidate(),
  });
}

export function useTestAdminBackupS3() {
  return useMutation({
    mutationFn: (body: AdminBackupS3TestBody) => testAdminBackupS3(captureAdminAuthority(), body),
    retry: false,
  });
}

/** Confirms the SECRET_KEY copy; the list's key_confirmed and warnings change with it. */
export function useAcknowledgeAdminBackupKey() {
  const invalidate = useInvalidateBackups();
  return useMutation({
    mutationFn: (keyCheck: string) => acknowledgeAdminBackupKey(captureAdminAuthority(), keyCheck),
    retry: false,
    onSettled: invalidate,
  });
}
