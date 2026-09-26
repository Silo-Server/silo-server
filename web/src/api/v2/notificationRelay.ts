import type { ProfileRequestContextSnapshot } from "@/api/client";
import { requireNotificationAuthority } from "./notifications";
import { v2, type V2Result } from "./request";

export type NotificationRelayRegistration =
  V2Result<"POST /api/v2/admin/notifications/push/relay/register">;

export async function registerNotificationRelay(
  relayURL: string,
  profileContext: ProfileRequestContextSnapshot,
) {
  requireNotificationAuthority(profileContext);
  const result = await v2("POST /api/v2/admin/notifications/push/relay/register", {
    body: { relay_url: relayURL },
    profileContext,
    retryAuthentication: false,
  });
  requireNotificationAuthority(profileContext);
  return result;
}

export async function clearNotificationRelay(profileContext: ProfileRequestContextSnapshot) {
  requireNotificationAuthority(profileContext);
  await v2("DELETE /api/v2/admin/notifications/push/relay", {
    profileContext,
    retryAuthentication: false,
  });
  requireNotificationAuthority(profileContext);
}
