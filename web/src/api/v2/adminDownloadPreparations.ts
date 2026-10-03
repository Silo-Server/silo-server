import type { ProfileRequestContextSnapshot } from "@/api/client";
import { adminKeys } from "@/hooks/queries/keys";
import { adminAuthorityScope, requireAdminAuthority } from "./adminAuthority";
import { v2, type V2Result } from "./request";

export type AdminDownloadPreparationList = V2Result<"GET /api/v2/admin/downloads/preparations">;
export type AdminDownloadPreparation = AdminDownloadPreparationList["items"][number];
export type AdminDownloadPreparationProgress = NonNullable<AdminDownloadPreparation["progress"]>;

/** Items the server returns per read; counts always cover the whole queue. */
export const ADMIN_DOWNLOAD_PREPARATIONS_LIMIT = 200;

export function adminDownloadPreparationsKey(context: ProfileRequestContextSnapshot | null) {
  return [...adminKeys.downloadPreparations(), adminAuthorityScope(context)] as const;
}

export async function listAdminDownloadPreparations(
  context: ProfileRequestContextSnapshot,
): Promise<AdminDownloadPreparationList> {
  requireAdminAuthority(context);
  const list = await v2("GET /api/v2/admin/downloads/preparations", {
    profileContext: context,
    query: { limit: ADMIN_DOWNLOAD_PREPARATIONS_LIMIT },
  });
  requireAdminAuthority(context);
  return list;
}

/** Payload of a download_preparation.progress realtime event. */
export interface DownloadPreparationProgressEvent {
  id: string;
  progress: AdminDownloadPreparationProgress;
}

export function isDownloadPreparationProgressEvent(
  data: unknown,
): data is DownloadPreparationProgressEvent {
  if (!data || typeof data !== "object") return false;
  const { id, progress } = data as { id?: unknown; progress?: unknown };
  if (typeof id !== "string" || !progress || typeof progress !== "object") return false;
  const { encoded_seconds, duration_seconds, speed, updated_at } = progress as Record<
    string,
    unknown
  >;
  return (
    typeof encoded_seconds === "number" &&
    typeof duration_seconds === "number" &&
    typeof speed === "number" &&
    typeof updated_at === "string"
  );
}

/**
 * Applies a progress reading to a cached list. Returns null when the job is
 * not in the list (or not running), so the caller re-reads it instead.
 */
export function applyDownloadPreparationProgress(
  list: AdminDownloadPreparationList,
  event: DownloadPreparationProgressEvent,
): AdminDownloadPreparationList | null {
  const current = list.items.find((item) => item.id === event.id);
  if (!current || current.state !== "running") return null;
  const updated: AdminDownloadPreparation = {
    ...current,
    progress: event.progress,
    progress_unavailable: false,
  };
  return { ...list, items: list.items.map((item) => (item === current ? updated : item)) };
}
