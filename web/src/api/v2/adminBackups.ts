import type { ProfileRequestContextSnapshot } from "@/api/client";
import { requireAdminAuthority } from "./adminAuthority";
import { v2, type V2Result } from "./request";

export type AdminBackupCapabilities = V2Result<"GET /api/v2/admin/backups/capabilities">;
export type AdminBackupList = V2Result<"GET /api/v2/admin/backups">;
export type AdminBackup = AdminBackupList["items"][number];
export type AdminBackupKind = AdminBackup["kind"];
export type AdminBackupStatus = AdminBackup["status"];
export type AdminBackupWarning = AdminBackupList["warnings"][number];
export type AdminBackupActive = NonNullable<AdminBackupList["active"]>;
export type AdminBackupLastRun = NonNullable<AdminBackupList["last_run"]>;
export type AdminBackupDownloadLink =
  V2Result<"POST /api/v2/admin/backups/{id}/download-links">["items"][number];
export type AdminBackupDestination = AdminBackupList["destination"];
/** full: every stored object as it is; restore-set: restore-set.tar alone. */
export type AdminBackupDownloadScope = "full" | "restore-set";

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
 * Links valid for 15 minutes: every stored object of the backup for `full`,
 * or restore-set.tar alone for `restore-set`.
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
