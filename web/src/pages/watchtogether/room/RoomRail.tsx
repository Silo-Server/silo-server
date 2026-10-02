import { useState } from "react";
import { Check } from "lucide-react";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import type { WatchTogetherRoomSnapshot } from "@/lib/watchTogether";
import type { ActivityEntry } from "../hooks/useActivityFeed";
import { MemberAvatar } from "./MemberAvatar";
import { memberKey, memberTints } from "./members";

function relativeTime(at: number, now: number) {
  const seconds = Math.max(0, Math.round((now - at) / 1000));
  if (seconds < 45) return "now";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  return `${Math.round(minutes / 60)}h`;
}

const modeText: Record<string, string> = { host_pick: "host picks", vote: "everyone votes" };
const policyText: Record<string, string> = {
  host_only: "host controls playback",
  guest_play_pause: "guests can pause",
};

function ActivityTitle({ contentId }: { contentId: string }) {
  const detail = useCatalogItemDetail(contentId);
  const item = detail.data;
  if (!item) return <span className="text-muted-foreground">…</span>;
  const label =
    item.type === "episode" && item.series_title
      ? `${item.series_title} · S${item.season_number ?? "?"} E${item.episode_number ?? "?"}`
      : item.title;
  return <span className="font-medium">{label}</span>;
}

function ActivityLine({ entry }: { entry: ActivityEntry }) {
  const who = entry.who ? <span className="font-medium">{entry.who}</span> : null;
  switch (entry.kind) {
    case "joined":
      return <>{who} joined</>;
    case "left":
      return <>{who} left</>;
    case "ready":
      return <>{who} is ready</>;
    case "unready":
      return <>{who} is no longer ready</>;
    case "staged":
      return (
        <>
          {who} queued {entry.contentId ? <ActivityTitle contentId={entry.contentId} /> : null}
        </>
      );
    case "unstaged":
      return <>{who} cleared the queue</>;
    case "started":
      return (
        <>
          {who} started {entry.contentId ? <ActivityTitle contentId={entry.contentId} /> : null}
        </>
      );
    case "stopped":
      return (
        <>
          {who} stopped {entry.contentId ? <ActivityTitle contentId={entry.contentId} /> : null}
        </>
      );
    case "mode":
      return (
        <>
          {who} switched the room to{" "}
          <span className="italic">{modeText[entry.detail ?? ""] ?? entry.detail}</span>
        </>
      );
    case "policy":
      return (
        <>
          {who} set <span className="italic">{policyText[entry.detail ?? ""] ?? entry.detail}</span>
        </>
      );
    case "playback":
      return <>Playback {entry.detail === "playing" ? "resumed" : "paused"}</>;
    case "suggested":
      return (
        <>
          {who} suggested <span className="font-medium">{entry.detail}</span>
        </>
      );
    case "unsuggested":
      return (
        <>
          {who} removed <span className="font-medium">{entry.detail}</span>
        </>
      );
    default:
      return null;
  }
}

export function PeopleList({ room }: { room: WatchTogetherRoomSnapshot | null }) {
  const members = room?.members ?? [];
  const tints = memberTints(members);
  const playing = room?.phase === "playing";
  if (members.length === 0) {
    return (
      <p className="text-muted-foreground px-4 py-6 text-center text-sm">
        No one here yet. Share the invite to fill the room.
      </p>
    );
  }
  return (
    <ul className="flex flex-col">
      {members.map((member) => {
        const state = playing
          ? room?.playback_state === "playing"
            ? "Watching"
            : room?.playback_state === "waiting"
              ? "Buffering"
              : "Paused"
          : member.lobby_ready
            ? "Ready"
            : "Connected";
        return (
          <li key={memberKey(member)} className="flex items-center gap-3 px-4 py-2.5">
            <MemberAvatar name={member.display_name} tint={tints.get(memberKey(member))} />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 text-sm">
                <span className="truncate font-medium">{member.display_name}</span>
                {member.is_host ? (
                  <span className="text-muted-foreground rounded-full border border-white/10 px-1.5 py-0.5 text-[10px] font-semibold tracking-wide uppercase">
                    Host
                  </span>
                ) : null}
                {member.is_self ? (
                  <span className="text-muted-foreground text-xs">(you)</span>
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
  if (entries.length === 0) {
    return (
      <p className="text-muted-foreground px-4 py-6 text-center text-sm">
        Things people do in the room show up here.
      </p>
    );
  }
  return (
    <ul className="flex flex-col gap-3 px-4 py-3 text-sm">
      {entries.map((entry) => (
        <li key={entry.id} className="flex gap-3">
          <span className="text-muted-foreground w-8 shrink-0 text-xs tabular-nums">
            {relativeTime(entry.at, now)}
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
  return (
    <Tabs defaultValue="people" className="flex h-full min-h-0 flex-col gap-0">
      <TabsList variant="line" className="border-border/60 w-full justify-start border-b px-2">
        <TabsTrigger value="people">
          People
          {room ? (
            <span className="bg-surface-raised ml-1.5 rounded-full px-1.5 py-0.5 text-[10px] font-semibold">
              {room.member_count}
            </span>
          ) : null}
        </TabsTrigger>
        <TabsTrigger value="activity">Activity</TabsTrigger>
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
  const [open, setOpen] = useState(false);
  const latest = props.entries[0];
  return (
    <>
      <aside className="border-border/60 hidden min-h-0 w-80 shrink-0 border-l md:flex md:flex-col">
        <RoomRailBody {...props} />
      </aside>
      <div className="border-border/60 flex items-center gap-3 border-t px-4 py-2 md:hidden">
        <button
          type="button"
          onClick={() => setOpen(true)}
          className="flex min-w-0 flex-1 items-center gap-3 text-left text-sm"
          aria-label="Open people and activity"
        >
          <span className="font-semibold">People · {props.room?.member_count ?? 0}</span>
          <span className="text-muted-foreground truncate text-xs">
            {latest ? <ActivityLine entry={latest} /> : "Activity"}
          </span>
        </button>
      </div>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent side="bottom" className="h-[70vh] p-0">
          <SheetTitle className="sr-only">People and activity</SheetTitle>
          <RoomRailBody {...props} />
        </SheetContent>
      </Sheet>
    </>
  );
}
