import type { ProfileRequestContextSnapshot } from "@/api/client";
import { adminKeys } from "@/hooks/queries/keys";
import { adminAuthorityScope, requireAdminAuthority } from "./adminAuthority";
import { v2, type V2Query, type V2Result } from "./request";

export type AdminDownloadStorage = V2Result<"GET /api/v2/admin/downloads/storage">;
export type AdminDownloadStorageLocation = AdminDownloadStorage["locations"][number];
export type AdminDownloadStorageUsage = NonNullable<AdminDownloadStorageLocation["usage"]>;
export type AdminDownloadStorageFilesPage = V2Result<"GET /api/v2/admin/downloads/storage/files">;
export type AdminDownloadStorageFile = AdminDownloadStorageFilesPage["items"][number];
export type AdminDownloadStorageFilesQuery = V2Query<"GET /api/v2/admin/downloads/storage/files">;
export type AdminDownloadStorageEventsPage = V2Result<"GET /api/v2/admin/downloads/storage/events">;
export type AdminDownloadStorageEvent = AdminDownloadStorageEventsPage["items"][number];
export type AdminDownloadStorageEventsQuery = V2Query<"GET /api/v2/admin/downloads/storage/events">;
export type AdminDownloadDevicesPage = V2Result<"GET /api/v2/admin/downloads/devices">;
export type AdminDownloadDevice = AdminDownloadDevicesPage["items"][number];
export type AdminDownloadDevicesQuery = V2Query<"GET /api/v2/admin/downloads/devices">;
export type AdminDownloadEntriesPage = V2Result<"GET /api/v2/admin/downloads/entries">;
export type AdminDownloadEntry = AdminDownloadEntriesPage["items"][number];
export type AdminDownloadStorageDeleteResult =
  V2Result<"POST /api/v2/admin/downloads/storage/files/delete">["results"][number];
export type AdminDownloadRevokeResult = V2Result<"POST /api/v2/admin/downloads/revoke">;

/** Rows one page asks for. */
export const ADMIN_DOWNLOAD_STORAGE_PAGE_SIZE = 50;
/** Most file ids one delete request may carry. */
export const ADMIN_DOWNLOAD_STORAGE_DELETE_MAX_IDS = 500;
/** Most download ids one revoke request may carry. */
export const ADMIN_DOWNLOAD_REVOKE_MAX_IDS = 500;

export function adminDownloadStorageRootKey(context: ProfileRequestContextSnapshot | null) {
  return [...adminKeys.downloadStorage(), adminAuthorityScope(context)] as const;
}

async function read<T>(context: ProfileRequestContextSnapshot, run: () => Promise<T>): Promise<T> {
  requireAdminAuthority(context);
  const result = await run();
  requireAdminAuthority(context);
  return result;
}

export function getAdminDownloadStorage(context: ProfileRequestContextSnapshot) {
  return read(context, () =>
    v2("GET /api/v2/admin/downloads/storage", { profileContext: context }),
  );
}

export function listAdminDownloadStorageFiles(
  context: ProfileRequestContextSnapshot,
  query: AdminDownloadStorageFilesQuery,
) {
  return read(context, () =>
    v2("GET /api/v2/admin/downloads/storage/files", { profileContext: context, query }),
  );
}

export function listAdminDownloadStorageEvents(
  context: ProfileRequestContextSnapshot,
  query: AdminDownloadStorageEventsQuery,
) {
  return read(context, () =>
    v2("GET /api/v2/admin/downloads/storage/events", { profileContext: context, query }),
  );
}

export function listAdminDownloadDevices(
  context: ProfileRequestContextSnapshot,
  query: AdminDownloadDevicesQuery,
) {
  return read(context, () =>
    v2("GET /api/v2/admin/downloads/devices", { profileContext: context, query }),
  );
}

export function listAdminDownloadEntries(
  context: ProfileRequestContextSnapshot,
  query: V2Query<"GET /api/v2/admin/downloads/entries">,
) {
  return read(context, () =>
    v2("GET /api/v2/admin/downloads/entries", { profileContext: context, query }),
  );
}

/** Deletes prepared files; returns one result per distinct id. */
export async function deleteAdminDownloadStorageFiles(
  context: ProfileRequestContextSnapshot,
  ids: readonly string[],
  includeInUse: boolean,
): Promise<AdminDownloadStorageDeleteResult[]> {
  requireAdminAuthority(context);
  const results: AdminDownloadStorageDeleteResult[] = [];
  for (let start = 0; start < ids.length; start += ADMIN_DOWNLOAD_STORAGE_DELETE_MAX_IDS) {
    const response = await v2("POST /api/v2/admin/downloads/storage/files/delete", {
      profileContext: context,
      body: {
        ids: ids.slice(start, start + ADMIN_DOWNLOAD_STORAGE_DELETE_MAX_IDS),
        include_in_use: includeInUse,
      },
      retryAuthentication: false,
    });
    results.push(...response.results);
  }
  requireAdminAuthority(context);
  return results;
}

export function cleanUpAdminDownloadStorageLocation(
  context: ProfileRequestContextSnapshot,
  location: string,
) {
  return read(context, () =>
    v2("POST /api/v2/admin/downloads/storage/locations/{location}/cleanup", {
      profileContext: context,
      path: { location },
      retryAuthentication: false,
    }),
  );
}

export function deleteAdminDownloadStorageUntrackedFiles(
  context: ProfileRequestContextSnapshot,
  location: string,
) {
  return read(context, () =>
    v2("POST /api/v2/admin/downloads/storage/locations/{location}/untracked/delete", {
      profileContext: context,
      path: { location },
      retryAuthentication: false,
    }),
  );
}

/** What to revoke: download ids, or every download on one device. */
export type AdminDownloadRevokeTarget =
  | { ids: string[] }
  | { userId: string; profileId: string; deviceId: string; pauseMonitors: boolean };

/** Revokes downloads; ids go in batches of at most 500, the request limit. */
export async function revokeAdminDownloads(
  context: ProfileRequestContextSnapshot,
  target: AdminDownloadRevokeTarget,
  reason: string,
): Promise<AdminDownloadRevokeResult> {
  if (!("ids" in target)) {
    return read(context, () =>
      v2("POST /api/v2/admin/downloads/revoke", {
        profileContext: context,
        body: {
          user_id: target.userId,
          profile_id: target.profileId,
          device_id: target.deviceId,
          pause_monitors: target.pauseMonitors,
          reason,
        },
        retryAuthentication: false,
      }),
    );
  }
  const ids = target.ids;
  const send = (batch: string[]) =>
    v2("POST /api/v2/admin/downloads/revoke", {
      profileContext: context,
      body: { ids: batch, reason },
      retryAuthentication: false,
    });
  requireAdminAuthority(context);
  let total = await send(ids.slice(0, ADMIN_DOWNLOAD_REVOKE_MAX_IDS));
  for (
    let start = ADMIN_DOWNLOAD_REVOKE_MAX_IDS;
    start < ids.length;
    start += ADMIN_DOWNLOAD_REVOKE_MAX_IDS
  ) {
    const result = await send(ids.slice(start, start + ADMIN_DOWNLOAD_REVOKE_MAX_IDS));
    total = {
      ...total,
      revoked: total.revoked + result.revoked,
      bytes: total.bytes + result.bytes,
      paused_monitors: total.paused_monitors + result.paused_monitors,
      download_ids: [...total.download_ids, ...result.download_ids],
    };
  }
  requireAdminAuthority(context);
  return total;
}
