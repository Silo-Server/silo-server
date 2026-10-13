import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { captureProfileRequestContext, StaleApiRequestContextError } from "@/api/client";
import {
  ADMIN_DOWNLOAD_STORAGE_PAGE_SIZE,
  adminDownloadStorageRootKey,
  cleanUpAdminDownloadStorageLocation,
  deleteAdminDownloadStorageFiles,
  deleteAdminDownloadStorageUntrackedFiles,
  getAdminDownloadStorage,
  listAdminDownloadDevices,
  listAdminDownloadEntries,
  listAdminDownloadStorageEvents,
  listAdminDownloadStorageFiles,
  revokeAdminDownloads,
  type AdminDownloadDevicesQuery,
  type AdminDownloadRevokeTarget,
  type AdminDownloadStorageEventsQuery,
  type AdminDownloadStorageFilesQuery,
} from "@/api/v2/adminDownloadStorage";

// The download_storage.changed event invalidates these after clean-up or a
// revoke, and every (re)subscription to its channel re-reads them in case
// events were missed. The overview also polls, because the server re-measures
// directories every few minutes without publishing an event.
const STORAGE_STALE_TIME = 30_000;
const STORAGE_REFRESH_INTERVAL = 60_000;

function requireContext() {
  const context = captureProfileRequestContext();
  if (!context) throw new StaleApiRequestContextError();
  return context;
}

/** Server-wide prepared-download storage, per location. */
export function useAdminDownloadStorage() {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...adminDownloadStorageRootKey(context), "overview"],
    queryFn: () => getAdminDownloadStorage(requireContext()),
    enabled: context !== null,
    staleTime: STORAGE_STALE_TIME,
    refetchInterval: STORAGE_REFRESH_INTERVAL,
  });
}

function nextCursor(last: { page?: { has_more: boolean; next_cursor?: string } }) {
  return last.page?.has_more ? last.page.next_cursor : undefined;
}

/** Prepared files, one keyset page at a time. */
export function useAdminDownloadStorageFiles(
  query: Omit<AdminDownloadStorageFilesQuery, "cursor">,
) {
  const context = captureProfileRequestContext();
  return useInfiniteQuery({
    queryKey: [...adminDownloadStorageRootKey(context), "files", query],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      listAdminDownloadStorageFiles(requireContext(), {
        limit: ADMIN_DOWNLOAD_STORAGE_PAGE_SIZE,
        ...query,
        cursor: pageParam,
      }),
    getNextPageParam: nextCursor,
    enabled: context !== null,
    staleTime: STORAGE_STALE_TIME,
  });
}

/** Clean-up and revocation history, newest first. */
export function useAdminDownloadStorageEvents(
  query: Omit<AdminDownloadStorageEventsQuery, "cursor">,
) {
  const context = captureProfileRequestContext();
  return useInfiniteQuery({
    queryKey: [...adminDownloadStorageRootKey(context), "events", query],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      listAdminDownloadStorageEvents(requireContext(), {
        limit: ADMIN_DOWNLOAD_STORAGE_PAGE_SIZE,
        ...query,
        cursor: pageParam,
      }),
    getNextPageParam: nextCursor,
    enabled: context !== null,
    staleTime: STORAGE_STALE_TIME,
  });
}

/** Devices that hold managed downloads, across accounts. */
export function useAdminDownloadDevices(query: Omit<AdminDownloadDevicesQuery, "cursor">) {
  const context = captureProfileRequestContext();
  return useInfiniteQuery({
    queryKey: [...adminDownloadStorageRootKey(context), "devices", query],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      listAdminDownloadDevices(requireContext(), {
        limit: ADMIN_DOWNLOAD_STORAGE_PAGE_SIZE,
        ...query,
        cursor: pageParam,
      }),
    getNextPageParam: nextCursor,
    enabled: context !== null,
    staleTime: STORAGE_STALE_TIME,
  });
}

/** One device's downloads, read when its row is expanded. */
export function useAdminDownloadDeviceEntries(
  device: { userId: string; profileId: string; deviceId: string } | null,
) {
  const context = captureProfileRequestContext();
  return useInfiniteQuery({
    queryKey: [...adminDownloadStorageRootKey(context), "entries", device],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      listAdminDownloadEntries(requireContext(), {
        user_id: device?.userId,
        profile_id: device?.profileId,
        device_id: device?.deviceId,
        limit: ADMIN_DOWNLOAD_STORAGE_PAGE_SIZE,
        cursor: pageParam,
      }),
    getNextPageParam: nextCursor,
    enabled: context !== null && device !== null,
    staleTime: STORAGE_STALE_TIME,
  });
}

/** Re-reads every storage view once an action settles. */
function useInvalidateStorage() {
  const queryClient = useQueryClient();
  return () =>
    queryClient.invalidateQueries({
      queryKey: adminDownloadStorageRootKey(captureProfileRequestContext()),
    });
}

export function useDeleteAdminDownloadStorageFiles() {
  const invalidate = useInvalidateStorage();
  return useMutation({
    mutationFn: ({ ids, includeInUse }: { ids: string[]; includeInUse: boolean }) =>
      deleteAdminDownloadStorageFiles(requireContext(), ids, includeInUse),
    retry: false,
    onSettled: invalidate,
  });
}

export function useCleanUpAdminDownloadStorageLocation() {
  const invalidate = useInvalidateStorage();
  return useMutation({
    mutationFn: (location: string) =>
      cleanUpAdminDownloadStorageLocation(requireContext(), location),
    retry: false,
    onSettled: invalidate,
  });
}

export function useDeleteAdminDownloadStorageUntrackedFiles() {
  const invalidate = useInvalidateStorage();
  return useMutation({
    mutationFn: (location: string) =>
      deleteAdminDownloadStorageUntrackedFiles(requireContext(), location),
    retry: false,
    onSettled: invalidate,
  });
}

export function useRevokeAdminDownloads() {
  const invalidate = useInvalidateStorage();
  return useMutation({
    mutationFn: ({ target, reason }: { target: AdminDownloadRevokeTarget; reason: string }) =>
      revokeAdminDownloads(requireContext(), target, reason),
    retry: false,
    onSettled: invalidate,
  });
}
