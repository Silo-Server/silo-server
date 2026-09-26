import { useCallback, useEffect, useState } from "react";
import { useOptionalAuth } from "@/hooks/useAuth";
import type { WatchTogetherRoomSnapshot } from "@/lib/watchTogether";

/**
 * Rooms this browser has been in recently, so the hub can offer "Rejoin" and
 * the detail page can offer "Suggest to <code>". Stored per account and
 * profile in localStorage; the room token inside is the same proof the URL
 * carries today and expires with it (24h), so entries are pruned on read.
 */
export const RECENT_ROOMS_KEY = "silo.watchParty.recentRooms.v1";
const MAX_RECENT = 8;
const MAX_AGE_MS = 24 * 60 * 60 * 1000;
const CHANGE_EVENT = "silo:watch-party-recent-rooms";

export interface RecentRoom {
  room_id: string;
  code: string;
  title?: string;
  token: string;
  user_id: number;
  profile_id: string;
  role: "host" | "guest";
  last_seen_at: string;
  ended?: boolean;
}

type RecentRoomIdentity = Pick<RecentRoom, "room_id" | "user_id" | "profile_id">;

export function recentRoomKey(room: RecentRoomIdentity): string {
  return JSON.stringify([room.user_id, room.profile_id, room.room_id]);
}

function readAll(): RecentRoom[] {
  try {
    const raw = localStorage.getItem(RECENT_ROOMS_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    const cutoff = Date.now() - MAX_AGE_MS;
    return parsed.filter(
      (entry): entry is RecentRoom =>
        !!entry &&
        typeof entry === "object" &&
        typeof (entry as RecentRoom).room_id === "string" &&
        typeof (entry as RecentRoom).token === "string" &&
        typeof (entry as RecentRoom).last_seen_at === "string" &&
        Date.parse((entry as RecentRoom).last_seen_at) > cutoff,
    );
  } catch {
    return [];
  }
}

function writeAll(entries: RecentRoom[]) {
  try {
    localStorage.setItem(RECENT_ROOMS_KEY, JSON.stringify(entries.slice(0, MAX_RECENT)));
  } catch {
    // Storage full or unavailable: recent rooms are a convenience, not state.
  }
  if (typeof window !== "undefined") window.dispatchEvent(new Event(CHANGE_EVENT));
}

export function rememberRecentRoom(input: {
  room: Pick<WatchTogetherRoomSnapshot, "room_id" | "code" | "self_role">;
  token: string;
  userId: number;
  profileId: string;
  title?: string;
}) {
  const now = new Date().toISOString();
  const key = recentRoomKey({
    room_id: input.room.room_id,
    user_id: input.userId,
    profile_id: input.profileId,
  });
  const entries = readAll();
  const rest = entries.filter((entry) => recentRoomKey(entry) !== key);
  const previous = entries.find((entry) => recentRoomKey(entry) === key);
  writeAll([
    {
      room_id: input.room.room_id,
      code: input.room.code,
      title: input.title ?? previous?.title,
      token: input.token,
      user_id: input.userId,
      profile_id: input.profileId,
      role: input.room.self_role,
      last_seen_at: now,
      ended: false,
    },
    ...rest,
  ]);
}

export function markRecentRoomEnded(room: RecentRoomIdentity) {
  const key = recentRoomKey(room);
  writeAll(
    readAll().map((entry) => (recentRoomKey(entry) === key ? { ...entry, ended: true } : entry)),
  );
}

export function forgetRecentRoom(room: RecentRoomIdentity) {
  const key = recentRoomKey(room);
  writeAll(readAll().filter((entry) => recentRoomKey(entry) !== key));
}

/** Recent rooms for the signed-in account and profile, newest first. */
export function useRecentRooms(): RecentRoom[] {
  const auth = useOptionalAuth();
  const userId = auth?.user?.id ?? null;
  const profileId = auth?.profile?.id ?? null;
  const select = useCallback(
    () =>
      userId === null || profileId === null
        ? []
        : readAll().filter((entry) => entry.user_id === userId && entry.profile_id === profileId),
    [userId, profileId],
  );
  const [rooms, setRooms] = useState<RecentRoom[]>(select);
  useEffect(() => {
    setRooms(select());
    const refresh = () => setRooms(select());
    window.addEventListener(CHANGE_EVENT, refresh);
    window.addEventListener("storage", refresh);
    return () => {
      window.removeEventListener(CHANGE_EVENT, refresh);
      window.removeEventListener("storage", refresh);
    };
  }, [select]);
  return rooms;
}
