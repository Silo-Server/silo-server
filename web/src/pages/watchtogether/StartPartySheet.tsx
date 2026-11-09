import { captureRoomCreationDraft } from "@/api/v2/watchTogetherCreate";
import { updateRoomPolicy } from "@/api/v2/watchTogetherPolicy";
import { stageRoomItem } from "@/api/v2/watchTogetherStage";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
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
import { useSettingValue } from "@/hooks/queries/settingValues";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { useSeasonEpisodes, useSeasons } from "@/hooks/queries/episodes";
import { useT, type MessageKey } from "@/i18n";
import { createWatchTogetherRoom, type WatchTogetherSelectionMode } from "@/lib/watchTogether";
import { copyWatchTogetherInvite } from "@/lib/watchTogetherActions";
import { decodeThumbhash } from "@/lib/thumbhash";
import { SETTING_KEYS } from "@/lib/settingsContract";
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
  title: MessageKey;
  /** The caption with a title chosen, and with none yet. */
  caption: { target: MessageKey; empty: MessageKey };
}[] = [
  {
    value: "host_pick",
    title: "web.watchTogether.common.hostPicks",
    caption: {
      target: "web.watchTogether.startSheet.hostPickCaptionTarget",
      empty: "web.watchTogether.startSheet.hostPickCaptionEmpty",
    },
  },
  {
    value: "vote",
    title: "watchParty.options.everyoneVotes",
    caption: {
      target: "web.watchTogether.startSheet.voteCaptionTarget",
      empty: "web.watchTogether.startSheet.voteCaptionEmpty",
    },
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
  const t = useT();
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
            {s.is_specials
              ? t("web.watchTogether.common.specials")
              : t("web.watchTogether.common.season", { number: String(s.season_number) })}
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
                {t("web.watchTogether.common.episodeNumber", {
                  number: String(episode.episode_number),
                })}
              </span>
              <span className="min-w-0 flex-1 truncate text-sm">{episode.title}</span>
              {episode.user_data?.played ? (
                <span className="text-muted-foreground text-[11px]">
                  {t("web.watchTogether.startSheet.watched")}
                </span>
              ) : null}
            </button>
          </li>
        ))}
        {!episodes.isLoading && playable.length === 0 ? (
          <li className="text-muted-foreground px-2 py-4 text-center text-sm">
            {t("web.watchTogether.startSheet.noEpisodes")}
          </li>
        ) : null}
      </ul>
      <Button type="button" variant="ghost" size="sm" onClick={onCancel} className="self-start">
        {t("web.watchTogether.startSheet.keepPick")}
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
  const t = useT();
  const navigate = useViewTransitionNavigate();
  const auth = useOptionalAuth();
  const [mode, setMode] = useState<WatchTogetherSelectionMode>("host_pick");
  const savedPolicy = useSettingValue<string>(SETTING_KEYS.WATCH_PARTY_GUEST_CONTROL_POLICY, {
    enabled: open,
  });
  // null follows the profile's saved choice, which the server applies to the
  // new room itself; the switch overrides it once the host flips it.
  const [guestsCanPauseChoice, setGuestsCanPause] = useState<boolean | null>(null);
  const guestsCanPause = guestsCanPauseChoice ?? savedPolicy.value === "guest_play_pause";
  const refetchSavedPolicy = savedPolicy.refetch;
  const [target, setTarget] = useState<StartPartyTarget | null>(initialTarget);
  const [choosing, setChoosing] = useState(false);
  const [busy, setBusy] = useState(false);
  // Detail pages pass a new initialTarget object on every render, so reset on
  // open or when the page's item changes, never on identity alone: a page
  // render must not undo the episode or switch choice the host made here.
  const initialTargetRef = useRef(initialTarget);
  useEffect(() => {
    initialTargetRef.current = initialTarget;
  });
  const initialTargetId = initialTarget?.content_id;
  useEffect(() => {
    if (open) {
      setTarget(initialTargetRef.current);
      setChoosing(false);
      setBusy(false);
    }
  }, [open, initialTargetId]);
  useEffect(() => {
    if (open) {
      setGuestsCanPause(null);
      // Re-read on every open: the cached value can be minutes old, and the
      // sheet writes what it shows back to the room and the profile.
      void refetchSavedPolicy({ cancelRefetch: false });
    }
  }, [open, refetchSavedPolicy]);

  const thumbhash = target?.poster_thumbhash
    ? decodeThumbhash(target.poster_thumbhash)
    : item.poster_thumbhash
      ? decodeThumbhash(item.poster_thumbhash)
      : "";
  const posterUrl = target?.poster_url ?? item.poster_url;

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
      // Make the room match the switch the host saw. An explicit choice is
      // also saved for the next party unless it already is. An untouched
      // switch whose saved value could not be read leaves the server's choice
      // and says what it was.
      const policy = guestsCanPause ? "guest_play_pause" : "host_only";
      const explicit = guestsCanPauseChoice !== null;
      const alreadySaved = savedPolicy.isSuccess && savedPolicy.value === policy;
      if (!explicit && !savedPolicy.isSuccess) {
        if (created.room.guest_control_policy !== policy)
          toast.info(
            created.room.guest_control_policy === "guest_play_pause"
              ? t("web.watchTogether.startSheet.policyGuestsCanPause")
              : t("web.watchTogether.startSheet.policyHostOnly"),
          );
      } else if (created.room.guest_control_policy !== policy || (explicit && !alreadySaved)) {
        const notApplied = () =>
          toast.error(
            guestsCanPause
              ? t("web.watchTogether.startSheet.policyFailed")
              : t("web.watchTogether.startSheet.policyHostOnlyFailed"),
          );
        const updated = await updateRoomPolicy(roomId, policy, authority!).catch(
          (error: unknown) => {
            if (error instanceof StaleApiRequestContextError) throw error;
            notApplied();
            return null;
          },
        );
        if (updated && updated.room.guest_control_policy !== policy) notApplied();
        if (!active()) return;
      }
      if (target && mode === "host_pick") {
        await stageRoomItem(
          roomId,
          { content_id: target.content_id, library_id: target.library_id },
          authority!,
        ).catch((error: unknown) => {
          if (error instanceof StaleApiRequestContextError) throw error;
          toast.error(t("web.watchTogether.startSheet.stageFailed"));
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
        toast.error(
          error instanceof Error ? error.message : t("web.watchTogether.startSheet.createFailed"),
        );
    } finally {
      setBusy(false);
    }
  }, [
    auth?.profile,
    auth?.user,
    guestsCanPause,
    guestsCanPauseChoice,
    mode,
    navigate,
    onOpenChange,
    savedPolicy.isSuccess,
    savedPolicy.value,
    t,
    target,
  ]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-w-md flex-col gap-4">
        <DialogHeader>
          <p className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
            {t("web.watchTogether.startSheet.eyebrow")}
          </p>
          <DialogTitle>{t("web.watchTogether.startSheet.title")}</DialogTitle>
          <DialogDescription className="sr-only">
            {t("web.watchTogether.startSheet.description")}
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
                ? item.season_count
                  ? t("web.watchTogether.startSheet.seriesSeasons", {
                      count: item.season_count,
                      count_text: String(item.season_count),
                    })
                  : t("web.watchTogether.common.series")
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
                  {t("web.watchTogether.startSheet.episode")}
                </div>
                <div className="truncate text-sm font-medium">
                  {target
                    ? target.subtitle
                      ? t("web.watchTogether.startSheet.episodeWithCode", {
                          code: target.subtitle,
                          title: target.title,
                        })
                      : t("web.watchTogether.startSheet.episodeTitle", { title: target.title })
                    : t("web.watchTogether.startSheet.chooseEpisode")}
                </div>
              </div>
              <Button type="button" variant="ghost" size="sm" onClick={() => setChoosing(true)}>
                {target
                  ? t("web.watchTogether.common.change")
                  : t("web.watchTogether.startSheet.choose")}
              </Button>
            </div>
          )
        ) : null}

        <div
          role="radiogroup"
          aria-label={t("web.watchTogether.startSheet.howToPick")}
          className="flex flex-col gap-2"
        >
          <div className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
            {t("web.watchTogether.startSheet.howToPick")}
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
                <div className="font-medium">{t(option.title)}</div>
                <div className="text-muted-foreground mt-0.5 text-xs">
                  {t(target ? option.caption.target : option.caption.empty)}
                </div>
              </button>
            );
          })}
        </div>

        <label className="flex items-center justify-between gap-3 text-sm">
          <span>
            <span className="font-medium">{t("web.watchTogether.startSheet.allowPause")}</span>
            <span className="text-muted-foreground block text-xs">
              {t("web.watchTogether.startSheet.allowPauseHint")}
            </span>
          </span>
          <Switch checked={guestsCanPause} onCheckedChange={setGuestsCanPause} />
        </label>

        <Button
          type="button"
          onClick={() => void create()}
          disabled={busy || choosing || savedPolicy.isFetching}
          className="h-11"
        >
          {busy
            ? t("web.watchTogether.startSheet.creating")
            : t("web.watchTogether.startSheet.create")}
        </Button>
        <p className="text-muted-foreground text-center text-xs">
          {t("web.watchTogether.startSheet.footnote")}
        </p>
      </DialogContent>
    </Dialog>
  );
}
