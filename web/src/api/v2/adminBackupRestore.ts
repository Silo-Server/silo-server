import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { components } from "@/api/v2/schema";
import { readAsAdmin } from "./adminBackups";
import { v2 } from "./request";

export type RestoreSet = components["schemas"]["AdminBackupRestoreSet"];
export type RestoreSection = RestoreSet["sections"][number];
export type RestoreSectionId = RestoreSection["id"];
export type RestoreItem = RestoreSection["items"][number];
export type RestoreSetting = NonNullable<RestoreItem["settings"]>[number];
export type RestoreSelection = components["schemas"]["AdminBackupRestoreSelection"];
export type RestoreJob = components["schemas"]["AdminTaskJob"];
export type RestoreReport = components["schemas"]["AdminBackupRestoreReport"];
export type RestoreReportItem = RestoreReport["items"][number];
export type RestoreReason = NonNullable<RestoreItem["reason"]>;
export type RestoreErrorCode = NonNullable<RestoreReport["error_code"]>;
export type RestoreAccount = components["schemas"]["AdminBackupRestoreAccount"];
export type RestoreProfile = components["schemas"]["AdminBackupRestoreProfile"];
export type RestorePolicyDifference = components["schemas"]["AdminBackupRestorePolicyDifference"];
export type RestoreDataTypeId = components["schemas"]["AdminBackupRestoreDataType"]["id"];
export type RestoreReportAccount = components["schemas"]["AdminBackupRestoreReportAccount"];

/** The admin job kind of a selective restore. */
const RESTORE_JOB_KIND = "backup_restore";

/** The job id at the end of a `Location: /api/v2/admin/jobs/{id}` header. */
export function jobIdFromLocation(location: string | null | undefined): string | null {
  const match = location ? /\/admin\/jobs\/([^/?#]+)/.exec(location) : null;
  return match?.[1] ? decodeURIComponent(match[1]) : null;
}

/**
 * Reading a restore set decrypts and parses it and compares every account with
 * this server, which takes longer than a normal read on a large backup.
 */
export const RESTORE_SET_TIMEOUT_MS = 180_000;

export function getRestoreSet(context: ProfileRequestContextSnapshot, id: string) {
  return readAsAdmin(context, () =>
    v2("GET /api/v2/admin/backups/{id}/restore-set", {
      profileContext: context,
      path: { id },
      timeoutMs: RESTORE_SET_TIMEOUT_MS,
    }),
  );
}

/** Queues the restore; 409 when the lock is held, a restore runs, or the digest changed. */
export function startRestore(
  context: ProfileRequestContextSnapshot,
  id: string,
  digest: string,
  selection: RestoreSelection,
) {
  return readAsAdmin(context, () =>
    v2("POST /api/v2/admin/backups/{id}/restore", {
      profileContext: context,
      path: { id },
      body: { digest, selection },
      retryAuthentication: false,
    }),
  );
}

export function getRestoreJob(context: ProfileRequestContextSnapshot, id: string) {
  return readAsAdmin(context, () =>
    v2("GET /api/v2/admin/jobs/{id}", { profileContext: context, path: { id } }),
  );
}

/** The newest selective restore jobs, newest first. */
export async function listRestoreJobs(context: ProfileRequestContextSnapshot) {
  const page = await readAsAdmin(context, () =>
    v2("GET /api/v2/admin/jobs", {
      profileContext: context,
      query: { kind: RESTORE_JOB_KIND, limit: 20 },
    }),
  );
  return page.items;
}

export function cancelRestoreJob(context: ProfileRequestContextSnapshot, id: string) {
  return readAsAdmin(context, () =>
    v2("POST /api/v2/admin/jobs/{id}/cancel", {
      profileContext: context,
      path: { id },
      retryAuthentication: false,
    }),
  );
}
