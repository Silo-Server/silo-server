import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type { AdminLogStream } from "@/api/types";
import type { AdminLogQuery } from "@/hooks/queries/admin/logs";
import { v2 } from "./request";

/**
 * Administrator log stream handshake.
 *
 * The bridge socket carried the bearer token in the URL. The v2 handshake
 * mints a single-use credential bound to the captured administrator login
 * session and profile proof, offers it as the second subprotocol, and puts
 * only the stream selection and filters in the URL.
 */
export const ADMIN_LOGS_SOCKET_PROTOCOL = "silo.admin-logs.v2";

export async function mintAdminLogsSocketTicket(authority: ProfileRequestContextSnapshot) {
  const check = () => {
    if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
  };
  check();
  // Reconnect may follow ordinary same-session token rotation: keep the
  // original authority fence but delegate the current token, never retry.
  const current = captureProfileRequestContext();
  if (!current) throw new StaleApiRequestContextError();
  const ticket = await v2("POST /api/v2/admin/logs/ws-ticket", {
    profileContext: current,
    retryAuthentication: false,
    signal: AbortSignal.timeout(10_000),
  }).catch((error: unknown) => {
    check();
    throw error;
  });
  check();
  if (
    ticket.protocol !== ADMIN_LOGS_SOCKET_PROTOCOL ||
    !/^[A-Za-z0-9_-]{43}$/.test(ticket.ticket)
  ) {
    throw new Error("Invalid log stream credential.");
  }
  return ticket;
}

export function buildAdminLogsSocketQuery(params: AdminLogQuery) {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === "") continue;
    search.set(key, String(value));
  }
  return search.toString();
}

/** The v2 socket URL: stream selection and filters only, never a credential. */
export function buildAdminLogsSocketUrl(
  stream: AdminLogStream,
  params: AdminLogQuery,
  location: Pick<Location, "protocol" | "host">,
) {
  const protocol = location.protocol === "https:" ? "wss:" : "ws:";
  const search = new URLSearchParams();
  search.set("stream", stream);
  for (const [key, value] of new URLSearchParams(buildAdminLogsSocketQuery(params)).entries()) {
    search.set(key, value);
  }
  return `${protocol}//${location.host}/api/v2/admin/logs/ws?${search.toString()}`;
}

/** The subprotocols to offer, in order; the server echoes only the first. */
export function adminLogsSocketProtocols(ticket: string): [string, string] {
  return [ADMIN_LOGS_SOCKET_PROTOCOL, `silo.ticket.${ticket}`];
}
