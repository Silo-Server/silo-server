import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2 } from "./request";

export async function mintRoomSocketTicket(
  roomId: string,
  roomToken: string,
  authority: ProfileRequestContextSnapshot | null = captureProfileRequestContext(),
) {
  const check = () => {
    if (!authority || !isCapturedProfileAuthorityActive(authority))
      throw new StaleApiRequestContextError();
  };
  check();
  // Reconnect may follow ordinary same-session access-token rotation. Keep
  // the original authority fence, but delegate its current token, without
  // retrying a refused request or changing room proof.
  const currentAuthority = captureProfileRequestContext();
  if (!currentAuthority) throw new StaleApiRequestContextError();
  const ticket = await v2("POST /api/v2/watch-together/rooms/{room_id}/ws-ticket", {
    path: { room_id: roomId },
    headers: { "X-Room-Token": roomToken },
    profileContext: currentAuthority,
    retryAuthentication: false,
    signal: AbortSignal.timeout(10_000),
  }).catch((error: unknown) => {
    check();
    throw error;
  });
  check();
  if (ticket.protocol !== "silo.room.v2" || !/^[A-Za-z0-9_-]{43}$/.test(ticket.ticket))
    throw new Error("Invalid room socket credential.");
  return ticket;
}
export function roomSocketURL(roomId: string) {
  const url = new URL(
    `/api/v2/watch-together/rooms/${encodeURIComponent(roomId)}/ws`,
    window.location.origin,
  );
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  return url.toString();
}
