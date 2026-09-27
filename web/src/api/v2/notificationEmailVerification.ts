import {
  type ProfileRequestContextSnapshot,
  StaleApiRequestContextError,
  isCapturedProfileAuthorityActive,
} from "@/api/client";
import { randomUUID } from "@/lib/uuid";
import { notificationScope, requireNotificationAuthority } from "./notifications";
import { v2 } from "./request";

export type EmailVerificationIntent = Readonly<{
  authority: ProfileRequestContextSnapshot;
  scope: string;
  body: Readonly<{ verification_id: string; email: string }>;
}>;

export function captureEmailVerificationIntent(
  email: string,
  authority: ProfileRequestContextSnapshot | null,
  previous: EmailVerificationIntent | null,
): EmailVerificationIntent {
  if (!authority) throw new StaleApiRequestContextError();
  requireNotificationAuthority(authority);
  const normalized = email.trim();
  const scope = notificationScope(authority);
  if (
    previous?.scope === scope &&
    previous.body.email === normalized &&
    isCapturedProfileAuthorityActive(previous.authority)
  ) {
    return previous;
  }
  return Object.freeze({
    authority,
    scope,
    body: Object.freeze({ verification_id: randomUUID(), email: normalized }),
  });
}

export async function queueNotificationEmailVerification(intent: EmailVerificationIntent) {
  requireNotificationAuthority(intent.authority);
  const result = await v2("PUT /api/v2/notifications/email-preferences/address", {
    body: { ...intent.body },
    profileContext: intent.authority,
    retryAuthentication: false,
  });
  requireNotificationAuthority(intent.authority);
  if (result.verification_id !== intent.body.verification_id)
    throw new Error("Verification receipt does not match the submitted request.");
  return result;
}
