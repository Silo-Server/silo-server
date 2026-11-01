import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { captureProfileRequestContext } from "@/api/client";
import { captureAdminAuthority } from "@/api/v2/adminAuthority";
import { deleteBackupUpload, listBackupUploads } from "@/api/v2/adminBackupUpload";
import { backupsRootKey } from "@/hooks/queries/admin/backups";

export function backupUploadsKey() {
  return [...backupsRootKey(), "uploads"] as const;
}

/** Restore sets uploaded to this server, newest first; read only where uploads are available. */
export function useAdminBackupUploads(enabled: boolean) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: backupUploadsKey(),
    queryFn: () => listBackupUploads(captureAdminAuthority()),
    enabled: context !== null && enabled,
    staleTime: 10_000,
  });
}

export function useDeleteAdminBackupUpload() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => deleteBackupUpload(captureAdminAuthority(), id),
    retry: false,
    onSettled: () => queryClient.invalidateQueries({ queryKey: backupUploadsKey() }),
  });
}
