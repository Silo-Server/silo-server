import type { ProfileRequestContextSnapshot } from "@/api/client";
import { requireNotificationAuthority } from "./notifications";
import { v2, type V2Result } from "./request";

type DestinationPage<T> = { items: T[]; page?: { has_more: boolean; next_cursor?: string } };
async function destinationPages<T>(
  load: (cursor?: string) => Promise<DestinationPage<T>>,
  context: ProfileRequestContextSnapshot,
): Promise<T[]> {
  const items: T[] = [];
  const seen = new Set<string>();
  let cursor: string | undefined;
  for (let page = 0; page < 100; page++) {
    requireNotificationAuthority(context);
    const result = await load(cursor);
    requireNotificationAuthority(context);
    if (!Array.isArray(result.items) || !result.page || typeof result.page.has_more !== "boolean")
      throw new Error("Invalid notification destination page.");
    items.push(...result.items);
    if (!result.page.has_more) return items;
    const next = result.page.next_cursor;
    if (typeof next !== "string" || !next || seen.has(next))
      throw new Error("Invalid notification destination continuation.");
    seen.add(next);
    cursor = next;
  }
  throw new Error("Too many notification destinations to display. Reload the list.");
}

type WebPush = V2Result<"GET /api/v2/notifications/web-push/subscriptions">["items"][number];
type Webhook = V2Result<"GET /api/v2/notifications/webhooks">["items"][number];
type ServerChannel = V2Result<"GET /api/v2/admin/notifications/server-channels">["items"][number];

export function listNotificationWebPushSubscriptions(
  profileContext: ProfileRequestContextSnapshot,
) {
  return destinationPages<WebPush>(
    (cursor) =>
      v2("GET /api/v2/notifications/web-push/subscriptions", {
        profileContext,
        query: { cursor, limit: 100 },
      }),
    profileContext,
  );
}
export function listNotificationWebhooks(profileContext: ProfileRequestContextSnapshot) {
  return destinationPages<Webhook>(
    (cursor) =>
      v2("GET /api/v2/notifications/webhooks", { profileContext, query: { cursor, limit: 100 } }),
    profileContext,
  );
}
export function listNotificationServerChannels(profileContext: ProfileRequestContextSnapshot) {
  return destinationPages<ServerChannel>(
    (cursor) =>
      v2("GET /api/v2/admin/notifications/server-channels", {
        profileContext,
        query: { cursor, limit: 100 },
      }),
    profileContext,
  );
}

export async function deleteNotificationWebPushSubscription(
  id: string,
  profileContext: ProfileRequestContextSnapshot,
) {
  requireNotificationAuthority(profileContext);
  await v2("DELETE /api/v2/notifications/web-push/subscriptions/{id}", {
    path: { id },
    profileContext,
    retryAuthentication: false,
  });
  requireNotificationAuthority(profileContext);
}

export async function deleteNotificationWebhook(
  intent: { id: string; etag: string },
  profileContext: ProfileRequestContextSnapshot,
) {
  requireNotificationAuthority(profileContext);
  await v2("DELETE /api/v2/notifications/webhooks/{id}", {
    path: { id: intent.id },
    headers: { "If-Match": intent.etag },
    profileContext,
    retryAuthentication: false,
  });
  requireNotificationAuthority(profileContext);
}

export async function deleteNotificationServerChannel(
  id: string,
  profileContext: ProfileRequestContextSnapshot,
) {
  requireNotificationAuthority(profileContext);
  await v2("DELETE /api/v2/admin/notifications/server-channels/{id}", {
    path: { id },
    profileContext,
    retryAuthentication: false,
  });
  requireNotificationAuthority(profileContext);
}

export async function rotateNotificationWebhookSecret(
  id: string,
  profileContext: ProfileRequestContextSnapshot,
) {
  requireNotificationAuthority(profileContext);
  const result = await v2("POST /api/v2/notifications/webhooks/{id}/rotate-secret", {
    path: { id },
    profileContext,
    retryAuthentication: false,
  });
  requireNotificationAuthority(profileContext);
  return result;
}
