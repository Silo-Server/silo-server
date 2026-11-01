import type { ProfileRequestContextSnapshot } from "@/api/client";
import { requireAdminAuthority } from "./adminAuthority";
import { v2, type V2Body, type V2Result } from "./request";

export type AdminBackupCapabilities = V2Result<"GET /api/v2/admin/backups/capabilities">;
export type AdminBackupList = V2Result<"GET /api/v2/admin/backups">;
export type AdminBackup = AdminBackupList["items"][number];
export type AdminBackupKind = AdminBackup["kind"];
export type AdminBackupStatus = AdminBackup["status"];
export type AdminBackupWarning = AdminBackupList["warnings"][number];
export type AdminBackupOmitted = AdminBackup["restore_set_omitted"][number];
export type AdminBackupActive = NonNullable<AdminBackupList["active"]>;
export type AdminBackupLastRun = NonNullable<AdminBackupList["last_run"]>;
export type AdminBackupDownloadLink =
  V2Result<"POST /api/v2/admin/backups/{id}/download-links">["items"][number];
export type AdminBackupDestination = AdminBackupList["destination"];
/** full: every stored object as it is; restore-set: restore-set.tar alone, decrypted here. */
export type AdminBackupDownloadScope = "full" | "restore-set";
export type AdminBackupS3TestBody = V2Body<"POST /api/v2/admin/backups/s3/test">;
export type AdminBackupS3TestResult = V2Result<"POST /api/v2/admin/backups/s3/test">;

/** Runs `run` only while `context` is the active admin authority, and checks it again after. */
export async function readAsAdmin<T>(
  context: ProfileRequestContextSnapshot,
  run: () => Promise<T>,
): Promise<T> {
  requireAdminAuthority(context);
  const result = await run();
  requireAdminAuthority(context);
  return result;
}

export function getAdminBackupCapabilities(context: ProfileRequestContextSnapshot) {
  return readAsAdmin(context, () =>
    v2("GET /api/v2/admin/backups/capabilities", { profileContext: context }),
  );
}

export function listAdminBackups(context: ProfileRequestContextSnapshot) {
  return readAsAdmin(context, () => v2("GET /api/v2/admin/backups", { profileContext: context }));
}

/** Starts a manual backup; 409 while another backup or restore runs. Never retried. */
export function startAdminBackup(context: ProfileRequestContextSnapshot) {
  return readAsAdmin(context, () =>
    v2("POST /api/v2/admin/backups", { profileContext: context, retryAuthentication: false }),
  );
}

export function deleteAdminBackup(context: ProfileRequestContextSnapshot, id: string) {
  return readAsAdmin(context, () =>
    v2("DELETE /api/v2/admin/backups/{id}", {
      profileContext: context,
      path: { id },
      retryAuthentication: false,
    }),
  );
}

/**
 * Links valid for 15 minutes: every stored object of the backup (encrypted
 * ones as stored) for `full`, or restore-set.tar alone, decrypted by this
 * server, for `restore-set`.
 */
export async function createAdminBackupDownloadLinks(
  context: ProfileRequestContextSnapshot,
  id: string,
  scope: AdminBackupDownloadScope = "full",
): Promise<AdminBackupDownloadLink[]> {
  const result = await readAsAdmin(context, () =>
    v2("POST /api/v2/admin/backups/{id}/download-links", {
      profileContext: context,
      path: { id },
      body: { scope },
    }),
  );
  return result.items;
}

/** Reaches a bucket and writes and deletes a test object in it. Saves nothing. */
export function testAdminBackupS3(
  context: ProfileRequestContextSnapshot,
  body: AdminBackupS3TestBody,
) {
  return readAsAdmin(context, () =>
    v2("POST /api/v2/admin/backups/s3/test", {
      profileContext: context,
      body,
      retryAuthentication: false,
    }),
  );
}

/** Confirms a copy of the SECRET_KEY with `keyCheck` is kept elsewhere; 409 when the key changed. */
export function acknowledgeAdminBackupKey(
  context: ProfileRequestContextSnapshot,
  keyCheck: string,
) {
  return readAsAdmin(context, () =>
    v2("POST /api/v2/admin/backups/s3/acknowledge-key", {
      profileContext: context,
      body: { key_check: keyCheck },
      retryAuthentication: false,
    }),
  );
}
