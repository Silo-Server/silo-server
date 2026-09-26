import { useQuery } from "@tanstack/react-query";
import { ApiClientError } from "@/api/client";
import { V2ProblemError, V2TransportError } from "@/api/v2/request";
import { getWatchTogetherRoom } from "@/lib/watchTogether";
import { markRecentRoomEnded, useRecentRooms, type RecentRoom } from "./useRecentRooms";

const RECENT_WINDOW_MS = 30 * 60 * 1000;

export interface LiveRoomPresence {
  room_id: string;
  code: string;
  token: string;
  role: RecentRoom["role"];
  phase: "lobby" | "playing";
  selection_mode: "host_pick" | "vote";
}

/**
 * The most recent room this browser was in within the last half hour, if
 * it is still live. Drives "Suggest to <code>" on detail pages and the
 * sidebar dot. Verified once per minute; a room that has ended is marked so
 * the hub stops offering it.
 */
export function useLiveRoomPresence(): LiveRoomPresence | null {
  const recent = useRecentRooms();
  // The newest entry that has not ended. Whether it is recent enough is
  // decided inside the query, where reading the clock is allowed.
  const candidate = recent.find((entry) => !entry.ended) ?? null;
  const query = useQuery({
    queryKey: [
      "watch-party",
      "presence",
      candidate?.room_id ?? null,
      candidate?.last_seen_at ?? null,
    ],
    queryFn: async () => {
      if (!candidate) return null;
      if (Date.now() - Date.parse(candidate.last_seen_at) > RECENT_WINDOW_MS) return null;
      try {
        const response = await getWatchTogetherRoom(candidate.room_id, candidate.token);
        if (response.room.phase === "ended") {
          markRecentRoomEnded(candidate);
          return null;
        }
        return {
          room_id: candidate.room_id,
          code: response.room.code,
          token: candidate.token,
          role: candidate.role,
          phase: response.room.phase,
          selection_mode: response.room.selection_mode,
        } satisfies LiveRoomPresence;
      } catch (error) {
        if (
          (error instanceof ApiClientError ||
            error instanceof V2ProblemError ||
            error instanceof V2TransportError) &&
          [404, 409, 410].includes(error.status)
        ) {
          markRecentRoomEnded(candidate);
          return null;
        }
        throw error;
      }
    },
    enabled: candidate !== null,
    staleTime: 60_000,
    refetchInterval: 60_000,
    retry: false,
  });
  return candidate ? (query.data ?? null) : null;
}
