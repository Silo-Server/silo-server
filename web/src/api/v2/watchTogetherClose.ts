import {
  type ProfileRequestContextSnapshot,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { v2 } from "./request";

export async function closeRoom(roomId: string, authority: ProfileRequestContextSnapshot | null) {
  if (!authority || !isCapturedProfileAuthorityActive(authority))
    throw new StaleApiRequestContextError();
  await v2("DELETE /api/v2/watch-together/rooms/{room_id}", {
    path: { room_id: roomId },
    profileContext: authority,
    retryAuthentication: false,
  });
  if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
}
