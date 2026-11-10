import type { MemberWatchState } from "@/api/v2/watchTogetherMemberState";
import { useT, type MessageKey, type Translate } from "@/i18n";
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
  const t = useT();
  const byMember = new Map(states?.map((s) => [`${s.user_id}:${s.profile_id}`, s.state]) ?? []);
  return (
    <span className="flex items-center gap-1" aria-label={describe(members, byMember, t)}>
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
            title={t("web.watchTogether.dots.memberTitle", {
              name: member.display_name,
              state: t(label(state)),
            })}
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

function label(state: MemberWatchState["state"]): MessageKey {
  return state === "watched"
    ? "web.watchTogether.dots.watched"
    : state === "in_progress"
      ? "web.watchTogether.dots.inProgress"
      : "web.watchTogether.dots.unseen";
}

function describe(
  members: WatchTogetherRoomMember[],
  byMember: Map<string, MemberWatchState["state"]>,
  t: Translate,
) {
  return members
    .map((member) =>
      t("web.watchTogether.dots.memberLabel", {
        name: member.display_name,
        state: t(label(byMember.get(memberKey(member)) ?? "unseen")),
      }),
    )
    .join(", ");
}

export function DotsLegend() {
  const t = useT();
  return (
    <span className="text-muted-foreground flex items-center gap-3 text-[11px]">
      <span className="flex items-center gap-1">
        <span className="inline-block size-2 rounded-full bg-white/70" />
        {t("web.watchTogether.dots.watched")}
      </span>
      <span className="flex items-center gap-1">
        <span className="relative inline-block size-2 overflow-hidden rounded-full border border-transparent">
          <span className="absolute inset-y-0 left-0 w-1/2 bg-white/70" />
        </span>
        {t("web.watchTogether.dots.inProgress")}
      </span>
      <span className="flex items-center gap-1">
        <span className="inline-block size-2 rounded-full border border-white/35" />
        {t("web.watchTogether.dots.unseen")}
      </span>
    </span>
  );
}
