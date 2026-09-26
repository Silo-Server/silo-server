import {
  captureAdminAuthority,
  adminAuthorityScope,
  requireAdminAuthority,
} from "./adminAuthority";
import type {
  AppNotification,
  NotificationPreferences,
  NotificationReasonFlags,
} from "@/api/types";
import { v2, type V2Result } from "./request";
export const captureNotificationAuthority = captureAdminAuthority;
export const notificationScope = adminAuthorityScope;
export const requireNotificationAuthority = requireAdminAuthority;
function notification(
  row: V2Result<"GET /api/v2/notifications">["items"][number],
): AppNotification {
  const libraryID = row.library_id === undefined ? undefined : Number(row.library_id);
  if (libraryID !== undefined && (!Number.isSafeInteger(libraryID) || libraryID <= 0))
    throw new Error("Unsupported library ID in notification.");
  const reasonFlags: NotificationReasonFlags = {};
  const value = row.reason_flags;
  if (value && typeof value === "object") {
    const raw = value as Record<string, unknown>;
    for (const key of ["favorite", "watchlist", "continue_watching", "next_up"] as const)
      if (key in raw && typeof raw[key] === "boolean") reasonFlags[key] = raw[key];
    for (const key of ["request_id", "media_type", "title", "reason"] as const)
      if (key in raw && typeof raw[key] === "string") reasonFlags[key] = raw[key];
    for (const key of ["tmdb_id", "year"] as const)
      if (key in raw && typeof raw[key] === "number") reasonFlags[key] = raw[key];
  }
  return { ...row, library_id: libraryID, reason_flags: reasonFlags };
}
export async function listNotifications(
  status: "all" | "unread",
  cursor?: string,
  profileContext = captureNotificationAuthority(),
) {
  requireNotificationAuthority(profileContext);
  const body = await v2("GET /api/v2/notifications", {
    query: { status, limit: 25, cursor },
    profileContext,
  });
  requireNotificationAuthority(profileContext);
  if (
    !Array.isArray(body.items) ||
    !body.page ||
    typeof body.page.has_more !== "boolean" ||
    (body.page.has_more &&
      (typeof body.page.next_cursor !== "string" ||
        !body.page.next_cursor ||
        body.page.next_cursor === cursor)) ||
    (!body.page.has_more && body.page.next_cursor) ||
    typeof body.read_cutoff !== "string" ||
    !body.read_cutoff
  )
    throw new Error("Invalid notification page. Reload notifications.");
  return {
    notifications: body.items.map(notification),
    next_cursor: body.page.has_more ? body.page.next_cursor : undefined,
    read_cutoff: body.read_cutoff,
  };
}
export async function unreadNotificationCount(profileContext = captureNotificationAuthority()) {
  requireNotificationAuthority(profileContext);
  const body = await v2("GET /api/v2/notifications/unread-count", { profileContext });
  requireNotificationAuthority(profileContext);
  return body.count;
}
export async function notificationCapabilities(profileContext = captureNotificationAuthority()) {
  requireNotificationAuthority(profileContext);
  const body = await v2("GET /api/v2/notifications/capabilities", { profileContext });
  requireNotificationAuthority(profileContext);
  return body;
}
export async function notificationPreferences(profileContext = captureNotificationAuthority()) {
  requireNotificationAuthority(profileContext);
  const body = await v2("GET /api/v2/notifications/preferences", { profileContext });
  requireNotificationAuthority(profileContext);
  return body;
}
export async function updateNotificationPreferences(
  input: Partial<NotificationPreferences>,
  profileContext = captureNotificationAuthority(),
) {
  requireNotificationAuthority(profileContext);
  const body = await v2("PUT /api/v2/notifications/preferences", {
    body: {
      enabled: input.enabled,
      notify_favorites: input.notify_favorites,
      notify_watchlist: input.notify_watchlist,
      notify_continue_watching: input.notify_continue_watching,
      notify_next_up: input.notify_next_up,
    },
    profileContext,
    retryAuthentication: false,
  });
  requireNotificationAuthority(profileContext);
  return body;
}
export async function markNotificationRead(
  id: string,
  profileContext = captureNotificationAuthority(),
) {
  requireNotificationAuthority(profileContext);
  await v2("POST /api/v2/notifications/{id}/read", {
    path: { id },
    profileContext,
    retryAuthentication: false,
  });
  requireNotificationAuthority(profileContext);
}
export async function markAllNotificationsRead(
  through: string,
  profileContext = captureNotificationAuthority(),
) {
  requireNotificationAuthority(profileContext);
  if (!through) throw new Error("Reload notifications before marking them read.");
  await v2("POST /api/v2/notifications/read-all", {
    body: { through },
    profileContext,
    retryAuthentication: false,
  });
  requireNotificationAuthority(profileContext);
}
