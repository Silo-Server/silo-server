import type { WatchTogetherRoomMember, WatchTogetherRoomSnapshot } from "@/lib/watchTogether";

/**
 * One colour per member, assigned by position in the room's member list
 * (host first, then by name). The same palette drives avatars, ready ticks
 * and the picker's per-episode dots so "who" stays learnable across surfaces.
 */
export const MEMBER_TINTS = [
  {
    bg: "bg-indigo-500/25",
    solidBg: "bg-indigo-700",
    text: "text-indigo-200",
    dot: "bg-indigo-400",
  },
  { bg: "bg-rose-500/25", solidBg: "bg-rose-700", text: "text-rose-200", dot: "bg-rose-400" },
  { bg: "bg-teal-500/25", solidBg: "bg-teal-700", text: "text-teal-200", dot: "bg-teal-400" },
  { bg: "bg-amber-500/25", solidBg: "bg-amber-700", text: "text-amber-200", dot: "bg-amber-400" },
  {
    bg: "bg-violet-500/25",
    solidBg: "bg-violet-700",
    text: "text-violet-200",
    dot: "bg-violet-400",
  },
  { bg: "bg-sky-500/25", solidBg: "bg-sky-700", text: "text-sky-200", dot: "bg-sky-400" },
  { bg: "bg-lime-500/25", solidBg: "bg-lime-700", text: "text-lime-200", dot: "bg-lime-400" },
  {
    bg: "bg-orange-500/25",
    solidBg: "bg-orange-700",
    text: "text-orange-200",
    dot: "bg-orange-400",
  },
] as const;

export type MemberTint = (typeof MEMBER_TINTS)[number];

export function memberKey(member: Pick<WatchTogetherRoomMember, "user_id" | "profile_id">) {
  return `${member.user_id}:${member.profile_id}`;
}

export function memberInitials(name: string) {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) return parts[0]!.slice(0, 2).toUpperCase();
  return (parts[0]![0]! + parts[parts.length - 1]![0]!).toUpperCase();
}

/** Tint lookup keyed by member, stable for the room's member ordering. */
export function memberTints(members: WatchTogetherRoomMember[] | undefined) {
  const map = new Map<string, MemberTint>();
  (members ?? []).forEach((member, index) => {
    map.set(memberKey(member), MEMBER_TINTS[index % MEMBER_TINTS.length]!);
  });
  return map;
}

export function selfMember(room: WatchTogetherRoomSnapshot | null) {
  return room?.members?.find((member) => member.is_self) ?? null;
}

export function hostMember(room: WatchTogetherRoomSnapshot | null) {
  return room?.members?.find((member) => member.is_host) ?? null;
}

export function guestReadyCount(room: WatchTogetherRoomSnapshot | null) {
  const members = room?.members?.filter((member) => !member.is_host) ?? [];
  return {
    ready: members.filter((member) => member.lobby_ready).length,
    total: members.length,
  };
}
