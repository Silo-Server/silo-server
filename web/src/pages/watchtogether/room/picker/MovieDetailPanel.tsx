import { useState } from "react";
import { Bookmark, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { decodeThumbhash } from "@/lib/thumbhash";
import { formatRuntimeMinutes } from "@/lib/mediaFormat";
import type { ItemMemberState, MemberWatchState } from "@/api/v2/watchTogetherMemberState";
import type { WatchTogetherRoomMember } from "@/lib/watchTogether";
import { MemberAvatar } from "../MemberAvatar";
import { memberKey, memberTints } from "../members";
import type { PickerCard } from "./PickerRow";

function stateLabel(state: MemberWatchState | undefined) {
  if (!state || state.state === "unseen") return "Hasn't seen it";
  if (state.state === "watched") return "Watched";
  if (state.position_seconds !== undefined && state.duration_seconds) {
    const pct = Math.round((state.position_seconds / state.duration_seconds) * 100);
    return `${pct}% in`;
  }
  return "In progress";
}

/**
 * Who in the room has seen this. Reads like a guest list, one line per
 * member, so the host can tell at a glance whether a pick is a rewatch for
 * some and a first viewing for others.
 */
export function MemberWatchList({
  members,
  state,
  loading,
}: {
  members: WatchTogetherRoomMember[];
  state: ItemMemberState | undefined;
  loading: boolean;
}) {
  const tints = memberTints(members);
  const byMember = new Map(state?.members.map((s) => [`${s.user_id}:${s.profile_id}`, s]) ?? []);
  const seen = members.filter((m) => byMember.get(memberKey(m))?.state === "watched").length;
  const started = members.filter((m) => {
    const state = byMember.get(memberKey(m))?.state;
    return state === "watched" || state === "in_progress";
  }).length;
  return (
    <section className="flex flex-col gap-2">
      <div className="flex items-baseline justify-between">
        <h4 className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
          Who's seen it
        </h4>
        {!loading && members.length > 0 ? (
          <span className="text-muted-foreground text-[11px]">
            {started === 0
              ? "new for everyone"
              : seen === members.length
                ? "a rewatch for everyone"
                : seen === 0
                  ? `${started} of ${members.length} started`
                  : `${seen} of ${members.length}`}
          </span>
        ) : null}
      </div>
      <ul className="flex flex-col gap-1.5">
        {members.map((member) => {
          const key = memberKey(member);
          const s = byMember.get(key);
          return (
            <li key={key} className="flex items-center gap-2.5 text-sm">
              <MemberAvatar name={member.display_name} tint={tints.get(key)} size="sm" />
              <span className="min-w-0 flex-1 truncate">{member.display_name}</span>
              {loading ? (
                <Skeleton className="h-3 w-16 rounded" />
              ) : (
                <span className="text-muted-foreground flex items-center gap-1.5 text-xs">
                  {s?.on_watchlist ? (
                    <Bookmark className="size-3" aria-label="On their watchlist" />
                  ) : null}
                  {stateLabel(s)}
                </span>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

/**
 * The right pane for a movie: backdrop, poster, the facts that decide a
 * movie night (runtime, rating, genres), the overview, and who has seen it.
 */
export function MovieDetailPanel({
  card,
  members,
  memberState,
  memberStateLoading,
  actionLabel,
  replaces,
  busy,
  onConfirm,
  onClose,
}: {
  card: PickerCard;
  members: WatchTogetherRoomMember[];
  memberState: ItemMemberState | undefined;
  memberStateLoading: boolean;
  actionLabel: string;
  /** Title of what is currently staged, when confirming would replace it. */
  replaces?: string;
  busy: boolean;
  onConfirm: () => void;
  onClose: () => void;
}) {
  const detail = useCatalogItemDetail(card.content_id, card.library_id);
  const item = detail.data;
  const [backdropLoaded, setBackdropLoaded] = useState(false);
  const [posterLoaded, setPosterLoaded] = useState(false);
  const posterUrl = item?.poster_url || card.poster_url;
  const thumbhash =
    item?.poster_thumbhash || card.poster_thumbhash
      ? decodeThumbhash(item?.poster_thumbhash || card.poster_thumbhash!)
      : "";
  const meta: string[] = [];
  if (item?.year ?? card.year) meta.push(String(item?.year ?? card.year));
  if (item?.runtime) meta.push(formatRuntimeMinutes(item.runtime));
  if (item?.content_rating) meta.push(item.content_rating);
  const ratings: string[] = [];
  if (item?.rating_imdb) ratings.push(`IMDb ${item.rating_imdb.toFixed(1)}`);
  if (item?.rating_tmdb) ratings.push(`TMDB ${item.rating_tmdb.toFixed(1)}`);
  if (item?.rating_rt_critic) ratings.push(`RT ${Math.round(item.rating_rt_critic)}%`);

  return (
    <div className="surface-panel-subtle relative flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl">
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
        <div className="relative flex items-end gap-4 p-4 pt-10">
          <div
            className="media-card-image aspect-[2/3] w-20 shrink-0 shadow-lg"
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
                className={`h-full w-full object-cover transition-opacity duration-300 ${
                  posterLoaded ? "opacity-100" : "opacity-0"
                }`}
                onLoad={() => setPosterLoaded(true)}
              />
            ) : null}
          </div>
          <div className="min-w-0 flex-1 pb-0.5">
            <p className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
              Candidate
            </p>
            <h3 className="line-clamp-2 text-base leading-tight font-semibold">{card.title}</h3>
            {meta.length > 0 ? (
              <p className="text-muted-foreground mt-1 text-xs">{meta.join(" · ")}</p>
            ) : detail.isLoading ? (
              <Skeleton className="mt-1.5 h-3 w-28 rounded" />
            ) : null}
            {item?.genres?.length ? (
              <p className="text-muted-foreground mt-0.5 truncate text-xs">
                {item.genres.slice(0, 3).join(", ")}
              </p>
            ) : null}
          </div>
        </div>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          onClick={onClose}
          aria-label="Close details"
          className="absolute top-2 right-2 bg-black/30 hover:bg-black/50"
        >
          <X className="size-4" />
        </Button>
      </div>

      <div className="overlay-scroll flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-4">
        {ratings.length > 0 ? (
          <div className="flex flex-wrap gap-1.5">
            {ratings.map((r) => (
              <span
                key={r}
                className="bg-surface-raised text-muted-foreground rounded-full px-2 py-0.5 text-[11px] font-medium"
              >
                {r}
              </span>
            ))}
          </div>
        ) : null}
        {item?.tagline ? (
          <p className="text-muted-foreground text-xs italic">{item.tagline}</p>
        ) : null}
        {detail.isLoading ? (
          <div className="flex flex-col gap-1.5">
            <Skeleton className="h-3 w-full rounded" />
            <Skeleton className="h-3 w-11/12 rounded" />
            <Skeleton className="h-3 w-2/3 rounded" />
          </div>
        ) : item?.overview ? (
          <p className="text-muted-foreground line-clamp-5 text-sm leading-relaxed">
            {item.overview}
          </p>
        ) : null}
        <MemberWatchList members={members} state={memberState} loading={memberStateLoading} />
      </div>

      <div className="border-border/60 flex shrink-0 flex-wrap items-center justify-end gap-2 border-t px-4 py-3">
        {replaces ? (
          <span className="text-muted-foreground mr-auto text-xs">Replaces {replaces}</span>
        ) : null}
        <Button type="button" variant="ghost" onClick={onClose} disabled={busy}>
          Cancel
        </Button>
        <Button type="button" onClick={onConfirm} disabled={busy}>
          {busy ? "Working…" : actionLabel}
        </Button>
      </div>
    </div>
  );
}
