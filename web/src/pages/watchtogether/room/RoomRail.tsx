import { useState } from "react";
import { Check } from "lucide-react";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { useT, type MessageKey, type Translate } from "@/i18n";
import type { WatchTogetherRoomSnapshot } from "@/lib/watchTogether";
import type { ActivityEntry } from "../hooks/useActivityFeed";
import { MemberAvatar } from "./MemberAvatar";
import { memberKey, memberTints } from "./members";

function relativeTime(at: number, now: number, t: Translate) {
  const seconds = Math.max(0, Math.round((now - at) / 1000));
  if (seconds < 45) return t("web.watchTogether.activity.timeNow");
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return t("web.watchTogether.activity.timeMinutes", { minutes: minutes });
  return t("web.watchTogether.activity.timeHours", { hours: Math.round(minutes / 60) });
}

const modeText: Record<string, MessageKey> = {
  host_pick: "web.watchTogether.activity.modeHostPick",
  vote: "web.watchTogether.activity.modeVote",
};
const policyText: Record<string, MessageKey> = {
  host_only: "web.watchTogether.common.policyHostOnly",
  guest_play_pause: "web.watchTogether.common.policyGuestPlayPause",
};

function ActivityTitle({ contentId }: { contentId: string }) {
  const t = useT();
  const detail = useCatalogItemDetail(contentId);
  const item = detail.data;
  if (!item)
    return (
      <span className="text-muted-foreground">{t("web.watchTogether.activity.titleLoading")}</span>
    );
  const label =
    item.type === "episode" && item.series_title
      ? t("web.watchTogether.activity.episodeTitle", {
          series: item.series_title,
          season: String(item.season_number ?? "?"),
          episode: String(item.episode_number ?? "?"),
        })
      : item.title;
  return <span className="font-medium">{label}</span>;
}

function ActivityLine({ entry }: { entry: ActivityEntry }) {
  const t = useT();
  const name = (fallback = "") => {
    const text = entry.who ?? fallback;
    return text ? <span className="font-medium">{text}</span> : "";
  };
  const who = name();
  const title = entry.contentId ? <ActivityTitle contentId={entry.contentId} /> : "";
  const detail = <span className="font-medium">{entry.detail}</span>;
  const lookup = (table: Record<string, MessageKey>) => {
    const key = table[entry.detail ?? ""];
    return <span className="italic">{key ? t(key) : entry.detail}</span>;
  };
  switch (entry.kind) {
    case "joined":
      return <>{t("web.watchTogether.activity.joined", { who })}</>;
    case "left":
      return <>{t("web.watchTogether.activity.left", { who })}</>;
    case "ready":
      return <>{t("web.watchTogether.activity.ready", { who })}</>;
    case "unready":
      return <>{t("web.watchTogether.activity.unready", { who })}</>;
    case "staged":
      return <>{t("web.watchTogether.activity.staged", { who, title })}</>;
    case "unstaged":
      return <>{t("web.watchTogether.activity.unstaged", { who })}</>;
    case "started":
      return <>{t("web.watchTogether.activity.started", { who, title })}</>;
    case "stopped":
      return <>{t("web.watchTogether.activity.stopped", { who, title })}</>;
    case "mode":
      return <>{t("web.watchTogether.activity.mode", { who, mode: lookup(modeText) })}</>;
    case "policy":
      return <>{t("web.watchTogether.activity.policy", { who, policy: lookup(policyText) })}</>;
    case "playback":
      return (
        <>
          {entry.detail === "playing"
            ? t("web.watchTogether.activity.playbackResumed")
            : t("web.watchTogether.activity.playbackPaused")}
        </>
      );
    case "suggested":
      return (
        <>
          {t("web.watchTogether.activity.suggested", {
            who: name(t("web.watchTogether.common.someone")),
            title: detail,
          })}
        </>
      );
    case "unsuggested":
      return (
        <>
          {t("web.watchTogether.activity.unsuggested", {
            who: name(t("web.watchTogether.common.someone")),
            title: detail,
          })}
        </>
      );
    default:
      return null;
  }
}

export function PeopleList({ room }: { room: WatchTogetherRoomSnapshot | null }) {
  const t = useT();
  const members = room?.members ?? [];
  const tints = memberTints(members);
  const playing = room?.phase === "playing";
  if (members.length === 0) {
    return (
      <p className="text-muted-foreground px-4 py-6 text-center text-sm">
        {t("web.watchTogether.rail.peopleEmpty")}
      </p>
    );
  }
  return (
    <ul className="flex flex-col">
      {members.map((member) => {
        const state = playing
          ? room?.playback_state === "playing"
            ? t("web.watchTogether.rail.stateWatching")
            : room?.playback_state === "waiting"
              ? t("web.watchTogether.rail.stateBuffering")
              : t("web.watchTogether.rail.statePaused")
          : member.lobby_ready
            ? t("web.watchTogether.rail.stateReady")
            : t("web.watchTogether.rail.stateConnected");
        return (
          <li key={memberKey(member)} className="flex items-center gap-3 px-4 py-2.5">
            <MemberAvatar name={member.display_name} tint={tints.get(memberKey(member))} />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 text-sm">
                <span className="truncate font-medium">{member.display_name}</span>
                {member.is_host ? (
                  <span className="text-muted-foreground rounded-full border border-white/10 px-1.5 py-0.5 text-[10px] font-semibold tracking-wide uppercase">
                    {t("watchParty.seat.host")}
                  </span>
                ) : null}
                {member.is_self ? (
                  <span className="text-muted-foreground text-xs">
                    {t("web.watchTogether.rail.you")}
                  </span>
                ) : null}
              </div>
              <div className="text-muted-foreground mt-0.5 flex items-center gap-1.5 text-xs">
                {!playing && member.lobby_ready ? (
                  <Check className="size-3 text-emerald-400" aria-hidden="true" />
                ) : null}
                {state}
              </div>
            </div>
            <span
              aria-hidden="true"
              className={`size-2 rounded-full ${
                playing && room?.playback_state === "waiting" ? "bg-amber-400" : "bg-emerald-400"
              }`}
            />
          </li>
        );
      })}
    </ul>
  );
}

export function ActivityFeed({ entries, now }: { entries: ActivityEntry[]; now: number }) {
  const t = useT();
  if (entries.length === 0) {
    return (
      <p className="text-muted-foreground px-4 py-6 text-center text-sm">
        {t("web.watchTogether.rail.activityEmpty")}
      </p>
    );
  }
  return (
    <ul className="flex flex-col gap-3 px-4 py-3 text-sm">
      {entries.map((entry) => (
        <li key={entry.id} className="flex gap-3">
          <span className="text-muted-foreground w-8 shrink-0 text-xs tabular-nums">
            {relativeTime(entry.at, now, t)}
          </span>
          <span className="min-w-0 flex-1">
            <ActivityLine entry={entry} />
          </span>
        </li>
      ))}
    </ul>
  );
}

export function RoomRailBody({
  room,
  entries,
  now,
  footer,
}: {
  room: WatchTogetherRoomSnapshot | null;
  entries: ActivityEntry[];
  now: number;
  footer?: string;
}) {
  const t = useT();
  return (
    <Tabs defaultValue="people" className="flex h-full min-h-0 flex-col gap-0">
      <TabsList variant="line" className="border-border/60 w-full justify-start border-b px-2">
        <TabsTrigger value="people">
          {t("web.watchTogether.rail.people")}
          {room ? (
            <span className="bg-surface-raised ms-1.5 rounded-full px-1.5 py-0.5 text-[10px] font-semibold">
              {room.member_count}
            </span>
          ) : null}
        </TabsTrigger>
        <TabsTrigger value="activity">{t("web.watchTogether.rail.activity")}</TabsTrigger>
      </TabsList>
      <TabsContent value="people" className="overlay-scroll min-h-0 flex-1 overflow-y-auto">
        <PeopleList room={room} />
      </TabsContent>
      <TabsContent value="activity" className="overlay-scroll min-h-0 flex-1 overflow-y-auto">
        <ActivityFeed entries={entries} now={now} />
      </TabsContent>
      {footer ? (
        <div className="border-border/60 mt-auto border-t px-4 py-3">
          <p className="text-muted-foreground text-xs">{footer}</p>
        </div>
      ) : null}
    </Tabs>
  );
}

/**
 * The rail: a side column on wide screens, a bottom sheet with a peek handle
 * on narrow ones. Same content either way.
 */
export function RoomRail(props: {
  room: WatchTogetherRoomSnapshot | null;
  entries: ActivityEntry[];
  now: number;
  footer?: string;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const latest = props.entries[0];
  return (
    <>
      <aside className="border-border/60 hidden min-h-0 w-80 shrink-0 border-s md:flex md:flex-col">
        <RoomRailBody {...props} />
      </aside>
      <div className="border-border/60 flex items-center gap-3 border-t px-4 py-2 md:hidden">
        <button
          type="button"
          onClick={() => setOpen(true)}
          className="flex min-w-0 flex-1 items-center gap-3 text-start text-sm"
          aria-label={t("web.watchTogether.rail.openLabel")}
        >
          <span className="font-semibold">
            {t("web.watchTogether.rail.peopleCount", {
              count: props.room?.member_count ?? 0,
            })}
          </span>
          <span className="text-muted-foreground truncate text-xs">
            {latest ? <ActivityLine entry={latest} /> : t("web.watchTogether.rail.activity")}
          </span>
        </button>
      </div>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent side="bottom" className="h-[70vh] p-0">
          <SheetTitle className="sr-only">{t("web.watchTogether.rail.sheetTitle")}</SheetTitle>
          <RoomRailBody {...props} />
        </SheetContent>
      </Sheet>
    </>
  );
}
