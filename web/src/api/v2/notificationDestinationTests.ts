import type { ProfileRequestContextSnapshot } from "@/api/client";
import { requireNotificationAuthority } from "./notifications";
import { v2 } from "./request";

export async function testNotificationDestination(
  kind: "webhook" | "server-channel",
  id: string,
  profileContext: ProfileRequestContextSnapshot,
) {
  requireNotificationAuthority(profileContext);
  const options = { path: { id }, profileContext, retryAuthentication: false };
  const result =
    kind === "webhook"
      ? await v2("POST /api/v2/notifications/webhooks/{id}/test", options)
      : await v2("POST /api/v2/admin/notifications/server-channels/{id}/test", options);
  requireNotificationAuthority(profileContext);
  return result;
}
