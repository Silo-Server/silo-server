import { useState } from "react";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { ArrowUpRight } from "lucide-react";
import type { Season } from "@/api/types";
import { useItemEpisodes } from "@/hooks/queries/episodes";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import EpisodeRow from "@/components/EpisodeRow";
import CardPlayOverlay from "@/components/CardPlayOverlay";

interface SeasonAccordionProps {
  seasons: Season[];
}

export default function SeasonAccordion({ seasons }: SeasonAccordionProps) {
  const sorted = seasons.slice().sort((a, b) => a.season_number - b.season_number);

  const [activeSeason, setActiveSeason] = useState(sorted[sorted.length - 1]?.content_id ?? "");

  if (seasons.length === 0) return null;

  const current = sorted.find((s) => s.content_id === activeSeason) ?? sorted[0]!;
  const currentTitle =
    current.is_specials || current.season_number === 0
      ? "Specials"
      : current.title || `Season ${current.season_number}`;

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <h2 className="text-[20px] font-bold">Episodes</h2>
          <span className="text-muted-foreground text-base">{current.episode_count} Episodes</span>
        </div>
        <Button asChild variant="outline" size="sm">
          <ViewTransitionLink to={`/item/${current.content_id}`}>
            Season Page
            <ArrowUpRight className="h-4 w-4" />
          </ViewTransitionLink>
        </Button>
      </div>

      {/* Season metadata (poster + title) */}
      <div className="flex items-start justify-between gap-4">
        <div className="flex min-w-0 items-center gap-3">
          {current.poster_url && (
            <div className="group/media relative h-14 w-10 shrink-0">
              <ViewTransitionLink to={`/item/${current.content_id}`}>
                <img
                  src={current.poster_url}
                  alt={currentTitle}
                  className="h-14 w-10 rounded-md object-cover"
                />
              </ViewTransitionLink>
              {current.play_content_id ? (
                <CardPlayOverlay
                  contentId={current.play_content_id}
                  title={currentTitle}
                  type="episode"
                  size="compact"
                />
              ) : null}
            </div>
          )}
          <ViewTransitionLink to={`/item/${current.content_id}`} className="min-w-0">
            <div className="text-sm font-medium">{currentTitle}</div>
            {current.air_date && (
              <div className="text-muted-foreground text-xs">{current.air_date}</div>
            )}
            {current.overview && (
              <div className="text-muted-foreground mt-1 line-clamp-2 text-xs">
                {current.overview}
              </div>
            )}
            {current.user_data && (
              <div className="text-muted-foreground mt-1 flex flex-wrap gap-2 text-[11px]">
                <span>{current.user_data.watched_count} watched</span>
                <span>{current.user_data.unplayed_count} left</span>
                {current.user_data.in_progress_count > 0 && (
                  <span>{current.user_data.in_progress_count} in progress</span>
                )}
              </div>
            )}
          </ViewTransitionLink>
        </div>
      </div>

      {/* Season pill tabs */}
      <div
        role="tablist"
        className="border-border bg-surface inline-flex gap-1 rounded-lg border p-1"
      >
        {sorted.map((season) => {
          const isActive = activeSeason === season.content_id;
          return (
            <button
              key={season.content_id}
              role="tab"
              id={`season-tab-${season.content_id}`}
              aria-selected={isActive}
              onClick={() => setActiveSeason(season.content_id)}
              className={`cursor-pointer rounded-md px-5 py-2 text-[13px] font-medium transition-colors duration-150 ${
                isActive ? "text-primary bg-accent" : "text-muted-foreground hover:text-foreground"
              }`}
            >
              {season.is_specials || season.season_number === 0
                ? "Specials"
                : season.title && season.title !== `Season ${season.season_number}`
                  ? season.title
                  : `Season ${season.season_number}`}
            </button>
          );
        })}
      </div>

      {/* Episode list for active season */}
      <div role="tabpanel" aria-labelledby={`season-tab-${current.content_id}`}>
        <SeasonPanel key={current.content_id} seasonId={current.content_id} />
      </div>
    </div>
  );
}

function SeasonPanel({ seasonId }: { seasonId: string | undefined }) {
  const { data, isLoading } = useItemEpisodes(seasonId);
  const episodes = data?.episodes ?? [];

  if (isLoading) {
    return (
      <div className="flex flex-col gap-3">
        {[1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-[72px] w-full rounded-lg" />
        ))}
      </div>
    );
  }

  if (episodes.length === 0) {
    return (
      <div className="border-border text-muted-foreground bg-surface rounded-lg border p-5 text-sm">
        No episodes are available for this season yet.
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-2.5">
      {episodes.map((ep) => (
        <EpisodeRow key={ep.content_id} episode={ep} />
      ))}
    </div>
  );
}
