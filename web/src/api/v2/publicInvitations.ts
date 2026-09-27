import { v2, V2TransportError } from "./request";
import type { components } from "./schema";

export type InvitationLookup = components["schemas"]["InvitationLookup"];
export type InvitationAcceptance = components["schemas"]["InvitationAcceptance"];

export async function lookupPublicInvitation(token: string, signal: AbortSignal) {
  const capability = await v2("GET /api/v2/invitations/capabilities", {
    signal,
    retryAuthentication: false,
  });
  if (capability.state !== "available") throw new Error("Invitation acceptance is unavailable.");
  const result = await v2("GET /api/v2/invitations/{token}", {
    path: { token },
    signal,
    retryAuthentication: false,
  });
  if (
    !result ||
    typeof result.email !== "string" ||
    typeof result.server_name !== "string" ||
    typeof result.inviter_name !== "string" ||
    typeof result.show_tour !== "boolean" ||
    typeof result.acceptance_available !== "boolean" ||
    typeof result.expires_at !== "string"
  ) {
    throw new V2TransportError("lookupInvitation", 200, "Invalid invitation response");
  }
  return result;
}

/** This effect is never replayed, including on an authentication failure. */
export async function acceptPublicInvitation(
  token: string,
  password: string,
  signal: AbortSignal,
): Promise<InvitationAcceptance> {
  const result = await v2("POST /api/v2/invitations/{token}/accept", {
    path: { token },
    body: { password },
    signal,
    retryAuthentication: false,
  });
  const invalid = () =>
    new V2TransportError("acceptInvitation", 201, "Invalid acceptance response");
  if (
    !result ||
    result.status !== "accepted" ||
    typeof result.username !== "string" ||
    !result.username
  )
    throw invalid();
  if (result.login_status === "sign_in_required") {
    if (result.tokens !== undefined) throw invalid();
    return result;
  }
  const pair = result.tokens;
  const user = pair?.user;
  if (
    result.login_status !== "signed_in" ||
    !pair ||
    !user ||
    typeof pair.access_token !== "string" ||
    !pair.access_token ||
    typeof pair.refresh_token !== "string" ||
    !pair.refresh_token ||
    !Number.isSafeInteger(pair.expires_in) ||
    pair.expires_in <= 0 ||
    typeof user.id !== "string" ||
    !/^[1-9]\d*$/.test(user.id) ||
    !Number.isSafeInteger(Number(user.id)) ||
    typeof user.username !== "string" ||
    user.username !== result.username ||
    typeof user.email !== "string" ||
    !["admin", "user"].includes(user.role) ||
    typeof user.download_allowed !== "boolean" ||
    !Array.isArray(user.permissions) ||
    !user.permissions.every((permission) => typeof permission === "string") ||
    user.impersonation != null
  )
    throw invalid();
  return result;
}
