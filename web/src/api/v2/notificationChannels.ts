import type { ProfileRequestContextSnapshot } from "@/api/client";
import { requireNotificationAuthority } from "./notifications";
import { v2, type V2Body } from "./request";

export async function getNotificationEmailPreferences(
  profileContext: ProfileRequestContextSnapshot,
) {
  requireNotificationAuthority(profileContext);
  const result = await v2("GET /api/v2/notifications/email-preferences", { profileContext });
  requireNotificationAuthority(profileContext);
  return result;
}
export async function updateNotificationEmailPreferences(
  body: V2Body<"PUT /api/v2/notifications/email-preferences">,
  profileContext: ProfileRequestContextSnapshot,
) {
  requireNotificationAuthority(profileContext);
  const result = await v2("PUT /api/v2/notifications/email-preferences", {
    body: { mode: body.mode },
    profileContext,
    retryAuthentication: false,
  });
  requireNotificationAuthority(profileContext);
  return result;
}
export async function getNotificationDiscordPreferences(
  profileContext: ProfileRequestContextSnapshot,
) {
  requireNotificationAuthority(profileContext);
  const result = await v2("GET /api/v2/notifications/discord-preferences", { profileContext });
  requireNotificationAuthority(profileContext);
  return result;
}
export async function updateNotificationDiscordPreferences(
  body: V2Body<"PUT /api/v2/notifications/discord-preferences">,
  profileContext: ProfileRequestContextSnapshot,
) {
  requireNotificationAuthority(profileContext);
  const result = await v2("PUT /api/v2/notifications/discord-preferences", {
    body: { mode: body.mode },
    profileContext,
    retryAuthentication: false,
  });
  requireNotificationAuthority(profileContext);
  return result;
}

export async function clearNotificationEmailAddress(profileContext: ProfileRequestContextSnapshot) {
  requireNotificationAuthority(profileContext);
  const result = await v2("DELETE /api/v2/notifications/email-preferences/address", {
    profileContext,
    retryAuthentication: false,
  });
  requireNotificationAuthority(profileContext);
  return result;
}
