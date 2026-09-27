import { useEffect, useMemo, useRef, useState } from "react";
import { Play, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useSeasons, useSeasonEpisodes } from "@/hooks/queries/episodes";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { decodeThumbhash } from "@/lib/thumbhash";
import type { EpisodeListItem, Season } from "@/api/types";
import type { ItemMemberState } from "@/api/v2/watchTogetherMemberState";
import type { WatchTogetherRoomMember } from "@/lib/watchTogether";
import { DotsLegend, EpisodeStateDots } from "./EpisodeStateDots";
import { memberKey, memberTints } from "../members";
import type { PickerCard } from "./PickerRow";

export interface EpisodePick {
  episode: EpisodeListItem;
  series: PickerCard;
  seasonNumber: number;
}

/**
 * "Next up for N of you": the lowest episode in this season that is unseen or
 * in progress for the most members. Episodes the whole room has watched do
 * not count.
 */
export function nextUpForRoom(
  episodes: EpisodeListItem[],
  members: WatchTogetherRoomMember[],
  states: Map<string, ItemMemberState>,
): { episode: EpisodeListItem; count: number } | null {
  let best: { episode: EpisodeListItem; count: number } | null = null;
  const claimed = new Set<string>();
  for (const episode of episodes) {
    const state = states.get(episode.content_id);
    let count = 0;
    for (const member of members) {
      const key = memberKey(member);
      if (claimed.has(key)) continue;
      const s =
        state?.members.find((m) => `${m.user_id}:${m.profile_id}` === key)?.state ?? "unseen";
      if (s !== "watched") {
        count++;
        claimed.add(key);
      }
    }
    if (count > 0 && (!best || count > best.count)) best = { episode, count };
    if (claimed.size === members.length) break;
  }
  return best;
}

/** Members who have not seen an earlier episode in the season than the pick. */
export function spoilerRisk(
  pick: EpisodeListItem,
  episodes: EpisodeListItem[],
  members: WatchTogetherRoomMember[],
  states: Map<string, ItemMemberState>,
): { names: string[]; episodeNumber: number } | null {
  const earlier = episodes.filter((e) => e.episode_number < pick.episode_number);
  for (const episode of earlier) {
    const state = states.get(episode.content_id);
    const names = members
      .filter((member) => {
        const key = memberKey(member);
        const s =
          state?.members.find((m) => `${m.user_id}:${m.profile_id}` === key)?.state ?? "unseen";
        return s === "unseen";
      })
      .map((m) => m.display_name);
    if (names.length > 0 && names.length < members.length) {
      return { names, episodeNumber: episode.episode_number };
    }
  }
  return null;
}

/** The current profile's progress, separate from the room's episode dots. */
function seasonProgressLabel(season: Season) {
  const u = season.user_data;
  if (!u) return null;
  const seen = u.watched_count;
  const total = seen + u.unplayed_count;
  if (total === 0) return null;
  if (seen === 0 && u.in_progress_count === 0) return "unwatched";
  if (seen === total) return "all watched";
  return `${seen} of ${total} watched`;
}

function EpisodeStill({ episode }: { episode: EpisodeListItem }) {
  const [loaded, setLoaded] = useState(false);
  const thumbhash = episode.still_thumbhash ? decodeThumbhash(episode.still_thumbhash) : "";
  return (
    <div
      className="media-card-image bg-surface hidden aspect-video w-28 shrink-0 @xl:block @3xl:w-36"
      style={
        thumbhash ? { backgroundImage: `url(${thumbhash})`, backgroundSize: "cover" } : undefined
      }
    >
      {episode.still_url ? (
        <img
          src={episode.still_url}
          alt=""
          loading="lazy"
          onLoad={() => setLoaded(true)}
          className={`h-full w-full object-cover transition-opacity duration-300 ${loaded ? "opacity-100" : "opacity-0"}`}
        />
      ) : null}
    </div>
  );
}

export function SeriesDrilldown({
  series,
  members,
  memberStates,
  onEpisodeIds,
  actionLabel,
  initialSeason,
  picked = null,
  risk = null,
  replaces,
  busy = false,
  confirmLabel,
  onPick,
  onConfirm,
  onClose,
}: {
  series: PickerCard;
  members: WatchTogetherRoomMember[];
  memberStates: Map<string, ItemMemberState>;
  /** Reports the visible episode ids so the parent can fetch their member state. */
  onEpisodeIds: (ids: string[]) => void;
  actionLabel: string;
  initialSeason?: number;
  /** The episode awaiting confirmation, shown in the panel's own footer. */
  picked?: EpisodePick | null;
  risk?: { names: string[]; episodeNumber: number } | null;
  /** Title of what is currently staged, when confirming would replace it. */
  replaces?: string;
  busy?: boolean;
  confirmLabel?: string;
  onPick: (pick: EpisodePick) => void;
  onConfirm?: () => void;
  onClose: () => void;
}) {
  const detail = useCatalogItemDetail(series.content_id, series.library_id);
  const item = detail.data;
  const seasons = useSeasons(series.content_id);
  const seasonList = useMemo(() => seasons.data?.seasons ?? [], [seasons.data?.seasons]);
  const [season, setSeason] = useState<number | null>(initialSeason ?? null);
  useEffect(() => {
    if (season === null && seasonList.length > 0) {
      const first = seasonList.find((s) => !s.is_specials) ?? seasonList[0]!;
      setSeason(first.season_number);
    }
  }, [season, seasonList]);
  const episodesQuery = useSeasonEpisodes(
    season === null ? undefined : series.content_id,
    season ?? -1,
  );
  const episodes = useMemo(
    () => (episodesQuery.data?.episodes ?? []).filter((e) => e.files.length > 0),
    [episodesQuery.data?.episodes],
  );
  const ids = useMemo(() => episodes.map((e) => e.content_id), [episodes]);
  useEffect(() => {
    onEpisodeIds(ids);
  }, [ids, onEpisodeIds]);
  const tints = memberTints(members);
  // Until member state has loaded every episode looks unseen and the first
  // one would claim "next up for everyone"; wait for the real answer.
  const nextUp = useMemo(
    () => (memberStates.size > 0 ? nextUpForRoom(episodes, members, memberStates) : null),
    [episodes, members, memberStates],
  );
  // Bring the suggested episode into view once per season so a long season
  // opens where the room is, not at E1.
  const listRef = useRef<HTMLUListElement | null>(null);
  const scrolledForRef = useRef<string | null>(null);
  useEffect(() => {
    const target = nextUp?.episode.content_id;
    if (!target || scrolledForRef.current === target) return;
    scrolledForRef.current = target;
    const row = listRef.current?.querySelector<HTMLElement>(`[data-episode="${target}"]`);
    row?.scrollIntoView?.({ block: "center" });
  }, [nextUp?.episode.content_id]);

  const [backdropLoaded, setBackdropLoaded] = useState(false);
  const [posterLoaded, setPosterLoaded] = useState(false);
  const posterUrl = item?.poster_url || series.poster_url;
  const thumbhash =
    item?.poster_thumbhash || series.poster_thumbhash
      ? decodeThumbhash(item?.poster_thumbhash || series.poster_thumbhash!)
      : "";
  const meta: string[] = [];
  if (item?.year ?? series.year) meta.push(String(item?.year ?? series.year));
  if (item?.content_rating) meta.push(item.content_rating);
  if (item?.genres?.length) meta.push(item.genres.slice(0, 2).join(", "));
  meta.push(`${seasonList.length} ${seasonList.length === 1 ? "season" : "seasons"}`);
  const currentSeason = seasonList.find((s) => s.season_number === season);
  const pickLabel = (episode: EpisodeListItem) =>
    `${actionLabel} S${season ?? episode.season_number} E${episode.episode_number}`;

  return (
    <div className="surface-panel-subtle @container relative flex max-h-[min(78vh,56rem)] min-h-0 flex-col overflow-hidden rounded-xl">
      <div className="relative shrink-0">
        {item?.backdrop_url ? (
          <img
            src={item.backdrop_url}
            alt=""
            className={`absolute inset-0 h-full w-full object-cover transition-opacity duration-700 ${
              backdropLoaded ? "opacity-30" : "opacity-0"
            }`}
            onLoad={() => setBackdropLoaded(true)}
          />
        ) : null}
        <div className="from-surface via-surface/85 absolute inset-0 bg-gradient-to-t to-transparent" />
        <div className="relative flex items-end gap-4 p-4 pt-8">
          <div
            className="media-card-image aspect-[2/3] w-16 shrink-0 shadow-lg @xl:w-20"
            style={
              thumbhash
                ? { backgroundImage: `url(${thumbhash})`, backgroundSize: "cover" }
                : undefined
            }
          >
            {posterUrl ? (
              <img
                src={posterUrl}
                alt=""
                className={`h-full w-full object-cover transition-opacity duration-300 ${posterLoaded ? "opacity-100" : "opacity-0"}`}
                onLoad={() => setPosterLoaded(true)}
              />
            ) : null}
          </div>
          <div className="min-w-0 flex-1 pb-0.5">
            <p className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
              Candidate · series
            </p>
            <h3 className="line-clamp-2 text-base leading-tight font-semibold">{series.title}</h3>
            <p className="text-muted-foreground mt-1 text-xs">{meta.join(" · ")}</p>
            {item?.overview ? (
              <p className="text-muted-foreground mt-1 line-clamp-2 max-w-3xl text-xs leading-relaxed">
                {item.overview}
              </p>
            ) : detail.isLoading ? (
              <Skeleton className="mt-1.5 h-3 w-64 rounded" />
            ) : null}
          </div>
        </div>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          onClick={onClose}
          aria-label="Close series"
          className="absolute top-2 right-2 bg-black/30 hover:bg-black/50"
        >
          <X className="size-4" />
        </Button>
      </div>

      <div className="flex min-h-0 flex-1 flex-col @2xl:flex-row">
        {/* Seasons: a vertical list on wide screens, chips on narrow ones. */}
        <nav
          aria-label="Seasons"
          className="border-border/60 overlay-scroll flex shrink-0 gap-1.5 overflow-x-auto border-b px-3 py-2 @2xl:w-44 @2xl:flex-col @2xl:overflow-y-auto @2xl:border-r @2xl:border-b-0 @2xl:px-2 @2xl:py-3"
        >
          {seasonList.map((s) => {
            const active = season === s.season_number;
            const progress = seasonProgressLabel(s);
            return (
              <button
                key={s.season_number}
                type="button"
                onClick={() => setSeason(s.season_number)}
                aria-pressed={active}
                className={`flex shrink-0 flex-col rounded-lg px-3 py-1.5 text-left transition-colors @2xl:w-full ${
                  active
                    ? "bg-foreground text-background"
                    : "text-muted-foreground hover:text-foreground hover:bg-white/[0.05]"
                }`}
              >
                <span className="text-xs font-semibold whitespace-nowrap">
                  {s.is_specials ? "Specials" : `Season ${s.season_number}`}
                </span>
                {progress ? (
                  <span className={`text-[10px] ${active ? "text-background/70" : ""}`}>
                    You · {progress}
                  </span>
                ) : null}
              </button>
            );
          })}
        </nav>

        <div className="flex min-h-0 min-w-0 flex-1 flex-col">
          <div className="border-border/60 flex shrink-0 items-center justify-between gap-3 border-b px-4 py-2">
            <span className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
              {currentSeason
                ? currentSeason.is_specials
                  ? "Specials"
                  : `Season ${currentSeason.season_number}`
                : "Episodes"}
              {!episodesQuery.isLoading && episodes.length > 0
                ? ` · ${episodes.length} episodes`
                : ""}
            </span>
            {members.length > 0 ? (
              <span className="hidden @xl:block">
                <DotsLegend />
              </span>
            ) : null}
          </div>
          <ul
            ref={listRef}
            className="overlay-scroll flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto px-2 py-2"
          >
            {episodesQuery.isLoading
              ? Array.from({ length: 5 }).map((_, i) => (
                  <li key={i} className="bg-surface h-20 animate-pulse rounded-lg" />
                ))
              : episodes.map((episode) => {
                  const isNext = nextUp?.episode.content_id === episode.content_id;
                  const isPicked = picked?.episode.content_id === episode.content_id;
                  const pick = () =>
                    onPick({ episode, series, seasonNumber: season ?? episode.season_number });
                  return (
                    <li
                      key={episode.content_id}
                      data-episode={episode.content_id}
                      className={`group/row flex items-start gap-3 rounded-lg p-2 transition-colors ${
                        isPicked
                          ? "ring-primary/60 bg-white/[0.06] ring-1"
                          : isNext
                            ? "bg-white/[0.05]"
                            : "hover:bg-white/[0.03]"
                      }`}
                    >
                      <button
                        type="button"
                        onClick={pick}
                        aria-label={pickLabel(episode)}
                        className="flex min-w-0 flex-1 items-start gap-3 text-left"
                      >
                        <EpisodeStill episode={episode} />
                        <span className="min-w-0 flex-1">
                          {isNext && nextUp ? (
                            <span className="block text-[10px] font-semibold tracking-[0.16em] text-emerald-300/90 uppercase">
                              Next up for {nextUp.count} of you
                            </span>
                          ) : null}
                          <span className="flex items-baseline gap-2">
                            <span className="text-muted-foreground shrink-0 text-xs font-semibold tabular-nums">
                              E{episode.episode_number}
                            </span>
                            <span className="truncate text-sm font-medium">{episode.title}</span>
                            <span className="text-muted-foreground shrink-0 text-xs">
                              {episode.runtime ? `${episode.runtime}m` : ""}
                            </span>
                          </span>
                          {episode.overview ? (
                            <span className="text-muted-foreground mt-0.5 line-clamp-2 text-xs leading-relaxed">
                              {episode.overview}
                            </span>
                          ) : null}
                          <span className="mt-1.5 block">
                            <EpisodeStateDots
                              members={members}
                              tints={tints}
                              states={memberStates.get(episode.content_id)?.members}
                            />
                          </span>
                        </span>
                      </button>
                      <Button
                        type="button"
                        size="sm"
                        variant={isNext ? "default" : "outline"}
                        className={`mt-1 shrink-0 gap-1.5 ${isNext || isPicked ? "" : "pointer-reveal"}`}
                        onClick={pick}
                      >
                        {isNext ? <Play className="size-3" /> : null}
                        {actionLabel}
                      </Button>
                    </li>
                  );
                })}
            {!episodesQuery.isLoading && episodes.length === 0 ? (
              <li className="text-muted-foreground px-2 py-8 text-center text-sm">
                No playable episodes in this season.
              </li>
            ) : null}
          </ul>
        </div>
      </div>

      {picked && onConfirm ? (
        <div className="border-border/60 flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-t px-4 py-3">
          <div className="min-w-0 flex-1">
            <p className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
              Your pick
            </p>
            <p className="truncate text-sm font-semibold">
              S{picked.seasonNumber} E{picked.episode.episode_number} “{picked.episode.title}”
            </p>
            {risk ? (
              <p className="text-xs text-amber-300">
                {risk.names.join(" and ")} {risk.names.length === 1 ? "hasn't" : "haven't"} seen E
                {risk.episodeNumber}. Heads up.
              </p>
            ) : replaces ? (
              <p className="text-muted-foreground text-xs">Replaces {replaces}</p>
            ) : null}
          </div>
          <Button type="button" variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="button" onClick={onConfirm} disabled={busy}>
            {busy ? "Working…" : (confirmLabel ?? actionLabel)}
          </Button>
        </div>
      ) : null}
    </div>
  );
}
