import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2 } from "./request";

/**
 * Owner-bound playback control handshake.
 *
 * The bridge socket put the bearer token in the URL and let any registration
 * replace the lane. The v2 handshake mints a single-use credential bound to
 * the captured login session, profile proof, playback session and (for a
 * session started through the v2 initial flow) the installation and the
 * session's control fence. The server admits the upgrade only for the same
 * owner and installation and closes the lane when either changes.
 */

export const PLAYBACK_CONTROL_SOCKET_PROTOCOL = "silo.playback-control.v2";

export type PlaybackControlSocketTicket = {
  ticket: string;
  expires_in: number;
  max_connection_seconds: number;
  protocol: string;
};

export async function mintPlaybackControlSocketTicket(
  sessionId: string,
  installationId: string | undefined,
  authority: ProfileRequestContextSnapshot | null = captureProfileRequestContext(),
): Promise<PlaybackControlSocketTicket> {
  const check = () => {
    if (!authority || !isCapturedProfileAuthorityActive(authority))
      throw new StaleApiRequestContextError();
  };
  check();
  // Reconnect may follow ordinary same-session access-token rotation: keep
  // the original authority fence but delegate the current token, and never
  // retry a refused mint.
  const current = captureProfileRequestContext();
  if (!current) throw new StaleApiRequestContextError();
  const ticket = await v2("POST /api/v2/playback/sessions/{session_id}/control/ws-ticket", {
    path: { session_id: sessionId },
    body: installationId ? { installation_id: installationId } : {},
    profileContext: current,
    retryAuthentication: false,
    signal: AbortSignal.timeout(10_000),
  }).catch((error: unknown) => {
    check();
    throw error;
  });
  check();
  if (
    ticket.protocol !== PLAYBACK_CONTROL_SOCKET_PROTOCOL ||
    !/^[A-Za-z0-9_-]{43}$/.test(ticket.ticket)
  ) {
    throw new Error("Invalid playback control credential.");
  }
  return ticket;
}

export function playbackControlSocketURL(sessionId: string, origin = window.location.origin) {
  const url = new URL(
    `/api/v2/playback/sessions/${encodeURIComponent(sessionId)}/control/ws`,
    origin,
  );
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  return url.toString();
}

/** The subprotocols to offer, in order: the protocol, then the credential. */
export function playbackControlSocketProtocols(ticket: string): string[] {
  return [PLAYBACK_CONTROL_SOCKET_PROTOCOL, `silo.ticket.${ticket}`];
}

export async function getPlaybackControlSocketCapabilities(
  authority: ProfileRequestContextSnapshot | null = captureProfileRequestContext(),
) {
  if (!authority || !isCapturedProfileAuthorityActive(authority))
    throw new StaleApiRequestContextError();
  const capabilities = await v2("GET /api/v2/playback/sessions/control/capabilities", {
    profileContext: authority,
  });
  if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
  return capabilities;
}
