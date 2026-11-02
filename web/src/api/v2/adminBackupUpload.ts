import {
  fetchWithSession,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "../client";
import type { components } from "@/api/v2/schema";
import { RESTORE_SET_TIMEOUT_MS, type RestoreSelection } from "./adminBackupRestore";
import { requireAdminAuthority } from "./adminAuthority";
import { readAsAdmin } from "./adminBackups";
import { decodeV2Response, v2, V2_CLIENT_HEADERS } from "./request";

export type AdminBackupUpload = components["schemas"]["AdminBackupUpload"];
export type UploadRejectionReason = components["schemas"]["AdminBackupUploadRejection"]["reason"];

export async function listBackupUploads(context: ProfileRequestContextSnapshot) {
  const page = await readAsAdmin(context, () =>
    v2("GET /api/v2/admin/backups/uploads", { profileContext: context }),
  );
  return page.items;
}

/** Opens an upload; 409 upload_limit_reached at three unfinished uploads, 413 above the limit. */
export function createBackupUpload(
  context: ProfileRequestContextSnapshot,
  sizeBytes: number,
  signal?: AbortSignal,
) {
  return readAsAdmin(context, () =>
    v2("POST /api/v2/admin/backups/uploads", {
      profileContext: context,
      body: { size_bytes: sizeBytes },
      retryAuthentication: false,
      signal,
    }),
  );
}

/**
 * One part as application/octet-stream, through the same captured session
 * boundary as v2 JSON. Sending a part again replaces it, so a failed part is
 * simply sent again.
 */
export async function putBackupUploadPart(
  context: ProfileRequestContextSnapshot,
  uploadId: string,
  part: number,
  bytes: Blob,
  signal?: AbortSignal,
): Promise<AdminBackupUpload> {
  if (!isCapturedProfileAuthorityActive(context)) throw new StaleApiRequestContextError();
  requireAdminAuthority(context);
  const key = "PUT /api/v2/admin/backups/uploads/{upload_id}/parts/{part}" as const;
  const { res } = await fetchWithSession(
    `/api/v2/admin/backups/uploads/${encodeURIComponent(uploadId)}/parts/${part}`,
    {
      method: "PUT",
      headers: {
        ...V2_CLIENT_HEADERS,
        Accept: "application/json",
        "Content-Type": "application/octet-stream",
        Authorization: `Bearer ${context.accessToken}`,
        "X-Profile-Id": context.profileId,
        "X-Profile-Token": context.profileToken ?? "",
      },
      body: bytes,
      signal,
    },
    // An upload can outlast the access token: a 401 refreshes it and sends
    // the part again, still bound to the captured account and profile.
    context,
    true,
  );
  const decoded = await decodeV2Response(key, res);
  if (!isCapturedProfileAuthorityActive(context)) throw new StaleApiRequestContextError();
  return decoded;
}

/** Checks the received file; answers `complete` or `rejected`. 409 while the backup lock is held. */
export function completeBackupUpload(
  context: ProfileRequestContextSnapshot,
  uploadId: string,
  signal?: AbortSignal,
) {
  return readAsAdmin(context, () =>
    v2("POST /api/v2/admin/backups/uploads/{upload_id}/complete", {
      profileContext: context,
      path: { upload_id: uploadId },
      signal,
    }),
  );
}

export function deleteBackupUpload(context: ProfileRequestContextSnapshot, uploadId: string) {
  return readAsAdmin(context, () =>
    v2("DELETE /api/v2/admin/backups/uploads/{upload_id}", {
      profileContext: context,
      path: { upload_id: uploadId },
    }),
  );
}

export function getUploadRestoreSet(context: ProfileRequestContextSnapshot, uploadId: string) {
  return readAsAdmin(context, () =>
    v2("GET /api/v2/admin/backups/uploads/{upload_id}/restore-set", {
      profileContext: context,
      path: { upload_id: uploadId },
      timeoutMs: RESTORE_SET_TIMEOUT_MS,
    }),
  );
}

export function startUploadRestore(
  context: ProfileRequestContextSnapshot,
  uploadId: string,
  digest: string,
  selection: RestoreSelection,
) {
  return readAsAdmin(context, () =>
    v2("POST /api/v2/admin/backups/uploads/{upload_id}/restore", {
      profileContext: context,
      path: { upload_id: uploadId },
      body: { digest, selection },
      retryAuthentication: false,
    }),
  );
}
