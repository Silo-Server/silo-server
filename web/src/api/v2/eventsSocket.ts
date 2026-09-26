import {
  captureProfileRequestContext,
  captureSessionIdentity,
  getAccessToken,
  isCapturedProfileAuthorityActive,
  isSessionIdentityCurrent,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2 } from "./request";

export async function mintEventsSocketTicket(profileContext: ProfileRequestContextSnapshot) {
  if (!isEventsAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  const ticket = await v2("POST /api/v2/events/ws-ticket", {
    profileContext,
    signal: AbortSignal.timeout(10_000),
  });
  if (!isEventsAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  if (ticket.protocol !== "silo.events.v2" || !/^[A-Za-z0-9_-]{43}$/.test(ticket.ticket)) {
    throw new Error("Invalid realtime handshake credential.");
  }
  return ticket;
}

export function captureEventsAuthority(): ProfileRequestContextSnapshot | null {
  const profile = captureProfileRequestContext();
  if (profile) return profile;
  const accessToken = getAccessToken();
  return accessToken
    ? { ...captureSessionIdentity(), accessToken, profileId: "", profileToken: null }
    : null;
}
export function isEventsAuthorityActive(authority: ProfileRequestContextSnapshot) {
  return authority.profileId
    ? isCapturedProfileAuthorityActive(authority)
    : isSessionIdentityCurrent(authority) &&
        getAccessToken() !== null &&
        captureProfileRequestContext() === null;
}
