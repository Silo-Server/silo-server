import { useMemo, useRef } from "react";
import type { WatchTogetherRoomSnapshot, WatchTogetherSuggestion } from "@/lib/watchTogether";
import type { WatchTogetherConnectionState } from "@/components/watchtogether/ConnectionStatusDot";
import { memberKey } from "../room/members";

/**
 * A client-side activity feed derived by diffing room snapshots. The server
 * has no event log; every entry here is something this client observed
 * change. The first snapshot after the socket connects is a baseline and
 * produces no entries, so a reload does not announce everyone "joining".
 */
export type ActivityKind =
  | "joined"
  | "left"
  | "ready"
  | "unready"
  | "staged"
  | "unstaged"
  | "started"
  | "stopped"
  | "mode"
  | "policy"
  | "playback"
  | "suggested"
  | "unsuggested";

export interface ActivityEntry {
  id: string;
  at: number;
  kind: ActivityKind;
  /** Display name of the member the entry is about, when there is one. */
  who?: string;
  /** Free text detail: a title, a mode, a playback state. */
  detail?: string;
  /** Content id, for entries the UI resolves to a title lazily. */
  contentId?: string;
}

const MAX_ENTRIES = 50;

function suggesterName(
  room: WatchTogetherRoomSnapshot | null,
  suggestion: WatchTogetherSuggestion,
) {
  return (
    room?.members?.find(
      (member) =>
        member.user_id === suggestion.suggester_user_id &&
        member.profile_id === suggestion.suggester_profile_id,
    )?.display_name ?? "Someone"
  );
}

export function diffSnapshots(
  prev: WatchTogetherRoomSnapshot,
  next: WatchTogetherRoomSnapshot,
  at: number,
): ActivityEntry[] {
  const out: ActivityEntry[] = [];
  let seq = 0;
  const push = (entry: Omit<ActivityEntry, "id" | "at">) =>
    out.push({ ...entry, id: `${at}-${seq++}`, at });

  const prevMembers = new Map((prev.members ?? []).map((m) => [memberKey(m), m]));
  const nextMembers = new Map((next.members ?? []).map((m) => [memberKey(m), m]));
  for (const [key, member] of nextMembers) {
    const before = prevMembers.get(key);
    if (!before) {
      push({ kind: "joined", who: member.display_name });
      continue;
    }
    if (!before.lobby_ready && member.lobby_ready)
      push({ kind: "ready", who: member.display_name });
    if (before.lobby_ready && !member.lobby_ready && next.phase === "lobby")
      push({ kind: "unready", who: member.display_name });
  }
  for (const [key, member] of prevMembers) {
    if (!nextMembers.has(key)) push({ kind: "left", who: member.display_name });
  }

  const host = next.members?.find((m) => m.is_host)?.display_name;
  if (prev.selection_mode !== next.selection_mode) {
    push({ kind: "mode", who: host, detail: next.selection_mode });
  }
  if (prev.guest_control_policy !== next.guest_control_policy) {
    push({ kind: "policy", who: host, detail: next.guest_control_policy });
  }
  if (prev.phase === "lobby" && next.phase === "playing") {
    push({ kind: "started", who: host, contentId: next.selected_content_id });
  } else if (prev.phase === "playing" && next.phase === "lobby") {
    push({ kind: "stopped", who: host, contentId: prev.selected_content_id });
  } else if (next.phase === "lobby" && prev.selected_content_id !== next.selected_content_id) {
    if (next.selected_content_id) {
      push({ kind: "staged", who: host, contentId: next.selected_content_id });
    } else if (prev.selection_mode === next.selection_mode) {
      push({ kind: "unstaged", who: host });
    }
  }
  if (
    next.phase === "playing" &&
    prev.phase === "playing" &&
    prev.playback_state !== next.playback_state &&
    (next.playback_state === "playing" || next.playback_state === "paused")
  ) {
    push({ kind: "playback", detail: next.playback_state });
  }
  return out;
}

export function diffSuggestions(
  room: WatchTogetherRoomSnapshot | null,
  prev: WatchTogetherSuggestion[],
  next: WatchTogetherSuggestion[],
  at: number,
): ActivityEntry[] {
  const out: ActivityEntry[] = [];
  let seq = 0;
  const prevIDs = new Set(prev.map((s) => s.id));
  const nextIDs = new Set(next.map((s) => s.id));
  for (const suggestion of next) {
    if (!prevIDs.has(suggestion.id)) {
      out.push({
        id: `${at}-s${seq++}`,
        at,
        kind: "suggested",
        who: suggesterName(room, suggestion),
        detail: suggestion.title,
        contentId: suggestion.content_id,
      });
    }
  }
  for (const suggestion of prev) {
    if (!nextIDs.has(suggestion.id)) {
      out.push({
        id: `${at}-s${seq++}`,
        at,
        kind: "unsuggested",
        who: suggesterName(room, suggestion),
        detail: suggestion.title,
        contentId: suggestion.content_id,
      });
    }
  }
  return out;
}

interface FeedLog {
  roomId: string | null;
  connected: boolean;
  room: WatchTogetherRoomSnapshot | null;
  suggestions: WatchTogetherSuggestion[] | null;
  entries: ActivityEntry[];
}

/**
 * Folds one (room, suggestions, connection) observation into the log. Pure
 * over its inputs and the previous log, so the hook derives entries during
 * render and keeps no effect-driven state.
 */
export function foldActivity(
  log: FeedLog,
  room: WatchTogetherRoomSnapshot | null,
  suggestions: WatchTogetherSuggestion[],
  connectionState: WatchTogetherConnectionState,
  at: number,
): FeedLog {
  if (!room) return log;
  let next = log;
  if (log.roomId !== room.room_id) {
    next = { roomId: room.room_id, connected: false, room: null, suggestions: null, entries: [] };
  }
  const connected = connectionState === "connected";
  // Before the socket is up (the initial REST read) observations only seed
  // the baseline; the first connected observation is the baseline itself.
  if (!connected || !next.connected) {
    return { ...next, connected, room, suggestions };
  }
  let entries = next.entries;
  if (next.room && next.room !== room) {
    const added = diffSnapshots(next.room, room, at);
    if (added.length > 0) entries = [...added.reverse(), ...entries].slice(0, MAX_ENTRIES);
  }
  if (next.suggestions && next.suggestions !== suggestions) {
    const added = diffSuggestions(room, next.suggestions, suggestions, at);
    if (added.length > 0) entries = [...added.reverse(), ...entries].slice(0, MAX_ENTRIES);
  }
  return { ...next, connected, room, suggestions, entries };
}

const emptyLog: FeedLog = {
  roomId: null,
  connected: false,
  room: null,
  suggestions: null,
  entries: [],
};

export function useActivityFeed(
  room: WatchTogetherRoomSnapshot | null,
  suggestions: WatchTogetherSuggestion[],
  connectionState: WatchTogetherConnectionState,
  now: () => number = Date.now,
): ActivityEntry[] {
  // The log is a fold over every observation this component has rendered
  // with. The ref carries the previous fold; the memo reruns only when an
  // input changes, so the clock is read once per change, never per render.
  const logRef = useRef<FeedLog>(emptyLog);
  const log = useMemo(
    () => foldActivity(logRef.current, room, suggestions, connectionState, now()),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [room, suggestions, connectionState],
  );
  logRef.current = log;
  return log.entries;
}
