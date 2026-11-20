import { v2, V2TransportError } from "./request";
import type { components } from "./schema";

export type PasswordResetLookup = components["schemas"]["PasswordResetLookup"];
export type PasswordResetCompletion = components["schemas"]["PasswordResetCompletion"];

export async function lookupPasswordReset(token: string, signal: AbortSignal) {
  const result = await v2("GET /api/v2/password-resets/{token}", {
    path: { token },
    signal,
    retryAuthentication: false,
  });
  if (
    !result ||
    typeof result.username !== "string" ||
    typeof result.server_name !== "string" ||
    typeof result.expires_at !== "string"
  ) {
    throw new V2TransportError("lookupPasswordReset", 200, "Invalid password reset response");
  }
  return result;
}

/** Spends the link. Never replayed, including on an authentication failure:
 * a completed reset cannot be repeated, and the new password is already set. */
export async function completePasswordReset(
  token: string,
  password: string,
  signal: AbortSignal,
): Promise<PasswordResetCompletion> {
  const result = await v2("POST /api/v2/password-resets/{token}/complete", {
    path: { token },
    body: { password },
    signal,
    retryAuthentication: false,
  });
  const invalid = () =>
    new V2TransportError("completePasswordReset", 200, "Invalid password reset response");
  if (
    !result ||
    result.status !== "completed" ||
    typeof result.username !== "string" ||
    !result.username
  )
    throw invalid();
  if (result.login_status === "sign_in_required") {
    if (result.tokens !== undefined) throw invalid();
    return result;
  }
  const pair = result.tokens;
  if (
    result.login_status !== "signed_in" ||
    !pair ||
    typeof pair.access_token !== "string" ||
    !pair.access_token ||
    typeof pair.refresh_token !== "string" ||
    !pair.refresh_token ||
    pair.user?.username !== result.username
  )
    throw invalid();
  return result;
}

/** Asks for a reset link for the account `login` names. The server answers
 * every accepted request alike, whether or not an account matched. */
export async function requestPasswordReset(login: string, signal: AbortSignal): Promise<void> {
  await v2("POST /api/v2/password-resets", {
    body: { login },
    signal,
    retryAuthentication: false,
  });
}
