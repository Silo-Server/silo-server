import { captureRoomCreationDraft } from "@/api/v2/watchTogetherCreate";
import { updateRoomPolicy } from "@/api/v2/watchTogetherPolicy";
import { stageRoomItem } from "@/api/v2/watchTogetherStage";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { isCapturedProfileAuthorityActive, StaleApiRequestContextError } from "@/api/client";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Switch } from "@/components/ui/switch";
import { useOptionalAuth } from "@/hooks/useAuth";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { useSeasonEpisodes, useSeasons } from "@/hooks/queries/episodes";
import { createWatchTogetherRoom, type WatchTogetherSelectionMode } from "@/lib/watchTogether";
import { copyWatchTogetherInvite } from "@/lib/watchTogetherActions";
import { decodeThumbhash } from "@/lib/thumbhash";
import type { EpisodeListItem, ItemDetail } from "@/api/types";
import { rememberRecentRoom } from "./hooks/useRecentRooms";

/** What the sheet will stage: a movie or one episode. */
export interface StartPartyTarget {
  content_id: string;
  title: string;
  subtitle?: string;
  poster_url?: string;
  poster_thumbhash?: string;
  library_id?: number;
}

export interface StartPartySheetProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The page's item: the movie, or the series/season/episode the target belongs to. */
  item: ItemDetail;
  /** The concrete item to stage; for a series this is the next-up episode when known. */
  initialTarget: StartPartyTarget | null;
  /** The series to drill into when the user wants a different episode. */
  seriesId?: string;
  initialSeasonNumber?: number;
}

const modeOptions: {
  value: WatchTogetherSelectionMode;
  title: string;
  caption: (what: string) => string;
}[] = [
  {
    value: "host_pick",
    title: "Host picks",
    caption: (what) => `${what} is up next. You start it when everyone is here.`,
  },
  {
    value: "vote",
    title: "Everyone votes",
    caption: (what) => `${what} is the first suggestion. Anyone can add and vote.`,
  },
];

function EpisodeChooser({
  seriesId,
  initialSeasonNumber,
  onPick,
  onCancel,
}: {
  seriesId: string;
  initialSeasonNumber?: number;
  onPick: (episode: EpisodeListItem, seasonNumber: number) => void;
  onCancel: () => void;
}) {
  const seasons = useSeasons(seriesId);
  const seasonList = useMemo(() => seasons.data?.seasons ?? [], [seasons.data?.seasons]);
  const [season, setSeason] = useState<number | null>(initialSeasonNumber ?? null);
  useEffect(() => {
    if (season === null && seasonList.length > 0) {
      setSeason((seasonList.find((s) => !s.is_specials) ?? seasonList[0]!).season_number);
    }
  }, [season, seasonList]);
  const episodes = useSeasonEpisodes(season === null ? undefined : seriesId, season ?? -1);
  const playable = (episodes.data?.episodes ?? []).filter((e) => e.files.length > 0);
  return (
    <div className="surface-panel-subtle flex flex-col gap-3 rounded-xl p-3">
      <div className="flex flex-wrap gap-1.5">
        {seasonList.map((s) => (
          <button
            key={s.season_number}
            type="button"
            onClick={() => setSeason(s.season_number)}
            aria-pressed={season === s.season_number}
            className={`rounded-full px-3 py-1 text-xs font-medium ${
              season === s.season_number
                ? "bg-foreground text-background"
                : "bg-surface-raised text-muted-foreground hover:text-foreground"
            }`}
          >
            {s.is_specials ? "Specials" : `Season ${s.season_number}`}
          </button>
        ))}
      </div>
      <ul className="overlay-scroll flex max-h-64 flex-col gap-0.5 overflow-y-auto">
        {playable.map((episode) => (
          <li key={episode.content_id}>
            <button
              type="button"
              onClick={() => onPick(episode, season ?? episode.season_number)}
              className="flex w-full items-center gap-3 rounded-lg px-2 py-2 text-left hover:bg-white/[0.04]"
            >
              <span className="text-muted-foreground w-7 shrink-0 text-xs font-semibold tabular-nums">
                E{episode.episode_number}
              </span>
              <span className="min-w-0 flex-1 truncate text-sm">{episode.title}</span>
              {episode.user_data?.played ? (
                <span className="text-muted-foreground text-[11px]">watched</span>
              ) : null}
            </button>
          </li>
        ))}
        {!episodes.isLoading && playable.length === 0 ? (
          <li className="text-muted-foreground px-2 py-4 text-center text-sm">
            No playable episodes here.
          </li>
        ) : null}
      </ul>
      <Button type="button" variant="ghost" size="sm" onClick={onCancel} className="self-start">
        Keep the current pick
      </Button>
    </div>
  );
}

/**
 * "Start a party with this" from a title's page. One click creates the room,
 * applies the pause policy, stages the item, copies the invite and lands in
 * the room. Every step runs under the authority captured at the click; a
 * replaced profile aborts silently rather than finishing in the wrong session.
 */
export function StartPartySheet({
  open,
  onOpenChange,
  item,
  initialTarget,
  seriesId,
  initialSeasonNumber,
}: StartPartySheetProps) {
  const navigate = useViewTransitionNavigate();
  const auth = useOptionalAuth();
  const [mode, setMode] = useState<WatchTogetherSelectionMode>("host_pick");
  const [guestsCanPause, setGuestsCanPause] = useState(false);
  const [target, setTarget] = useState<StartPartyTarget | null>(initialTarget);
  const [choosing, setChoosing] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) {
      setTarget(initialTarget);
      setChoosing(false);
      setBusy(false);
    }
  }, [open, initialTarget]);

  const thumbhash = target?.poster_thumbhash
    ? decodeThumbhash(target.poster_thumbhash)
    : item.poster_thumbhash
      ? decodeThumbhash(item.poster_thumbhash)
      : "";
  const posterUrl = target?.poster_url ?? item.poster_url;
  const what = target ? "This" : "Nothing";

  const create = useCallback(async () => {
    const draft = captureRoomCreationDraft(mode);
    const authority = draft.authority;
    const active = () => !!authority && isCapturedProfileAuthorityActive(authority);
    if (!active()) return;
    setBusy(true);
    try {
      const created = await createWatchTogetherRoom(draft);
      if (!active() || !created.room_access_token) return;
      const roomId = created.room.room_id;
      if (guestsCanPause) {
        await updateRoomPolicy(roomId, "guest_play_pause", authority!).catch((error: unknown) => {
          if (error instanceof StaleApiRequestContextError) throw error;
          toast.error("Room created, but guests can't pause yet. Change it from the room.");
        });
        if (!active()) return;
      }
      if (target && mode === "host_pick") {
        await stageRoomItem(
          roomId,
          { content_id: target.content_id, library_id: target.library_id },
          authority!,
        ).catch((error: unknown) => {
          if (error instanceof StaleApiRequestContextError) throw error;
          toast.error("Room created, but that item couldn't be queued. Pick it from the room.");
        });
        if (!active()) return;
      }
      if (auth?.user && auth.profile) {
        rememberRecentRoom({
          room: created.room,
          token: created.room_access_token,
          userId: auth.user.id,
          profileId: auth.profile.id,
          title: target ? [target.title, target.subtitle].filter(Boolean).join(" · ") : undefined,
        });
      }
      await copyWatchTogetherInvite(created.room.invite_path, created.room.code);
      if (!active()) return;
      const href = `/rooms/${encodeURIComponent(roomId)}?room_token=${encodeURIComponent(created.room_access_token)}`;
      // In vote mode the item becomes the first suggestion from inside the
      // room, where the suggestion draft has the room proof it needs.
      navigate(href, {
        replace: false,
        state:
          target && mode === "vote"
            ? {
                suggestFirst: {
                  content_id: target.content_id,
                  content_type: item.type === "movie" ? "movie" : "episode",
                  title: target.title,
                  subtitle: target.subtitle ?? "",
                  poster_url: target.poster_url ?? "",
                },
              }
            : undefined,
      });
      onOpenChange(false);
    } catch (error) {
      if (error instanceof StaleApiRequestContextError) return;
      if (active())
        toast.error(error instanceof Error ? error.message : "Could not create the room");
    } finally {
      setBusy(false);
    }
  }, [auth?.profile, auth?.user, guestsCanPause, mode, navigate, onOpenChange, target]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-w-md flex-col gap-4">
        <DialogHeader>
          <p className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
            Watch Together
          </p>
          <DialogTitle>Start a party with this</DialogTitle>
          <DialogDescription className="sr-only">
            Create a watch party with this title queued.
          </DialogDescription>
        </DialogHeader>

        <div className="flex items-center gap-3">
          <div
            className="media-card-image aspect-[2/3] w-14 shrink-0 overflow-hidden"
            style={
              thumbhash
                ? { backgroundImage: `url(${thumbhash})`, backgroundSize: "cover" }
                : undefined
            }
          >
            {posterUrl ? (
              <img src={posterUrl} alt="" className="h-full w-full object-cover" />
            ) : null}
          </div>
          <div className="min-w-0">
            <div className="truncate font-semibold">{item.title}</div>
            <div className="text-muted-foreground truncate text-xs">
              {item.type === "series"
                ? `Series${item.season_count ? ` · ${item.season_count} seasons` : ""}`
                : item.type === "season"
                  ? item.title
                  : item.year
                    ? String(item.year)
                    : ""}
            </div>
          </div>
        </div>

        {seriesId ? (
          choosing ? (
            <EpisodeChooser
              seriesId={seriesId}
              initialSeasonNumber={initialSeasonNumber}
              onPick={(episode, seasonNumber) => {
                setTarget({
                  content_id: episode.content_id,
                  title: episode.title,
                  subtitle: `S${seasonNumber} E${episode.episode_number}`,
                  poster_url: item.poster_url,
                  poster_thumbhash: item.poster_thumbhash,
                });
                setChoosing(false);
              }}
              onCancel={() => setChoosing(false)}
            />
          ) : (
            <div className="surface-panel-subtle flex items-center gap-3 rounded-xl px-3 py-2.5">
              <div className="min-w-0 flex-1">
                <div className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
                  Episode
                </div>
                <div className="truncate text-sm font-medium">
                  {target
                    ? `${target.subtitle ? `${target.subtitle} ` : ""}"${target.title}"`
                    : "Choose an episode"}
                </div>
              </div>
              <Button type="button" variant="ghost" size="sm" onClick={() => setChoosing(true)}>
                {target ? "Change" : "Choose"}
              </Button>
            </div>
          )
        ) : null}

        <div role="radiogroup" aria-label="How to pick" className="flex flex-col gap-2">
          <div className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
            How to pick
          </div>
          {modeOptions.map((option) => {
            const selected = mode === option.value;
            return (
              <button
                key={option.value}
                type="button"
                role="radio"
                aria-checked={selected}
                onClick={() => setMode(option.value)}
                className={`rounded-lg border px-3 py-2.5 text-left text-sm transition-colors ${
                  selected
                    ? "border-foreground/50 bg-accent"
                    : "text-muted-foreground hover:bg-muted/60"
                }`}
              >
                <div className="font-medium">{option.title}</div>
                <div className="text-muted-foreground mt-0.5 text-xs">{option.caption(what)}</div>
              </button>
            );
          })}
        </div>

        <label className="flex items-center justify-between gap-3 text-sm">
          <span>
            <span className="font-medium">Allow guests to pause</span>
            <span className="text-muted-foreground block text-xs">
              Otherwise only you control playback
            </span>
          </span>
          <Switch checked={guestsCanPause} onCheckedChange={setGuestsCanPause} />
        </label>

        <Button
          type="button"
          onClick={() => void create()}
          disabled={busy || choosing}
          className="h-11"
        >
          {busy ? "Creating…" : "Create party & copy invite"}
        </Button>
        <p className="text-muted-foreground text-center text-xs">
          You'll land in the room. Nothing plays until you start it. The room can see what members
          are mid-way through.
        </p>
      </DialogContent>
    </Dialog>
  );
}
