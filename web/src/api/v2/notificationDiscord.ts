import type { ProfileRequestContextSnapshot } from "@/api/client";
import { requireNotificationAuthority } from "./notifications";
import { v2 } from "./request";

export async function testNotificationDiscord(profileContext: ProfileRequestContextSnapshot) {
  requireNotificationAuthority(profileContext);
  const result = await v2("POST /api/v2/admin/notifications/discord/test", {
    profileContext,
    retryAuthentication: false,
  });
  requireNotificationAuthority(profileContext);
  return result;
}

export async function beginNotificationDiscordLink(profileContext: ProfileRequestContextSnapshot) {
  requireNotificationAuthority(profileContext);
  const result = await v2("POST /api/v2/notifications/discord/link/init", {
    profileContext,
    retryAuthentication: false,
  });
  requireNotificationAuthority(profileContext);
  return result;
}

export async function unlinkNotificationDiscord(profileContext: ProfileRequestContextSnapshot) {
  requireNotificationAuthority(profileContext);
  await v2("DELETE /api/v2/notifications/discord-link", {
    profileContext,
    retryAuthentication: false,
  });
  requireNotificationAuthority(profileContext);
}
