import type { MemberWatchState } from "@/api/v2/watchTogetherMemberState";
import type { WatchTogetherRoomMember } from "@/lib/watchTogether";
import { memberKey, type MemberTint } from "../members";

/**
 * One dot per room member for an episode: filled = watched, half = in
 * progress, hollow = unseen. Colours are the room's member tints so the
 * dots read the same as the avatars.
 */
export function EpisodeStateDots({
  members,
  tints,
  states,
}: {
  members: WatchTogetherRoomMember[];
  tints: Map<string, MemberTint>;
  states: MemberWatchState[] | undefined;
}) {
  const byMember = new Map(states?.map((s) => [`${s.user_id}:${s.profile_id}`, s.state]) ?? []);
  return (
    <span className="flex items-center gap-1" aria-label={describe(members, byMember)}>
      {members.map((member) => {
        const key = memberKey(member);
        const tint = tints.get(key);
        const state = byMember.get(key) ?? "unseen";
        const dot = tint?.dot ?? "bg-white/50";
        return (
          <span
            key={key}
            aria-hidden="true"
            className={`relative inline-block size-2 overflow-hidden rounded-full border ${
              state === "unseen" ? "border-white/35" : "border-transparent"
            } ${state === "watched" ? dot : ""}`}
            title={`${member.display_name}: ${label(state)}`}
          >
            {state === "in_progress" ? (
              <span className={`absolute inset-y-0 left-0 w-1/2 ${dot}`} />
            ) : null}
          </span>
        );
      })}
    </span>
  );
}

function label(state: MemberWatchState["state"]) {
  return state === "watched" ? "watched" : state === "in_progress" ? "in progress" : "unseen";
}

function describe(
  members: WatchTogetherRoomMember[],
  byMember: Map<string, MemberWatchState["state"]>,
) {
  return members
    .map((member) => `${member.display_name} ${label(byMember.get(memberKey(member)) ?? "unseen")}`)
    .join(", ");
}

export function DotsLegend() {
  return (
    <span className="text-muted-foreground flex items-center gap-3 text-[11px]">
      <span className="flex items-center gap-1">
        <span className="inline-block size-2 rounded-full bg-white/70" /> watched
      </span>
      <span className="flex items-center gap-1">
        <span className="relative inline-block size-2 overflow-hidden rounded-full border border-transparent">
          <span className="absolute inset-y-0 left-0 w-1/2 bg-white/70" />
        </span>{" "}
        in progress
      </span>
      <span className="flex items-center gap-1">
        <span className="inline-block size-2 rounded-full border border-white/35" /> unseen
      </span>
    </span>
  );
}
