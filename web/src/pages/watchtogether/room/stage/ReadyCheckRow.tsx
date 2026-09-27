import { Check } from "lucide-react";
import type { WatchTogetherRoomSnapshot } from "@/lib/watchTogether";
import { MemberAvatar } from "../MemberAvatar";
import { memberKey, memberTints, guestReadyCount } from "../members";

export function ReadyCheckRow({ room }: { room: WatchTogetherRoomSnapshot }) {
  const members = room.members?.filter((member) => !member.is_host) ?? [];
  const tints = memberTints(room.members);
  const { ready, total } = guestReadyCount(room);
  if (total === 0) return null;
  return (
    <div className="surface-panel-subtle flex flex-wrap items-center gap-4 rounded-xl px-4 py-3">
      <div>
        <div className="text-sm font-semibold">Guest ready check</div>
        <div className="text-muted-foreground text-xs">
          {ready} of {total} ready
        </div>
      </div>
      <ul className="flex flex-wrap items-center gap-3">
        {members.map((member) => (
          <li key={memberKey(member)} className="flex items-center gap-1.5 text-sm">
            <MemberAvatar
              name={member.display_name}
              tint={tints.get(memberKey(member))}
              size="sm"
            />
            <span className={member.lobby_ready ? "" : "text-muted-foreground"}>
              {member.display_name}
            </span>
            {member.lobby_ready ? (
              <Check className="size-3.5 text-emerald-400" aria-label="ready" />
            ) : (
              <span
                aria-label="not ready"
                className="inline-block size-3 rounded-full border border-white/25"
              />
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
