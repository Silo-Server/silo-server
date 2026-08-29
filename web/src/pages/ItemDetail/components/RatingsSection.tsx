import { ChevronLeft, ChevronRight, Star, ThumbsDown, ThumbsUp } from "lucide-react";
import type { ReactNode } from "react";
import type { CommunityRatingEntry, CommunityRatingReaction } from "@/api/types";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { useCommunityRatings, useSetCommunityRatingReaction } from "@/hooks/queries/ratings";
import { useCarouselEmbla } from "@/hooks/useCarouselEmbla";

const ratingDateFormatter = new Intl.DateTimeFormat(undefined, {
  month: "long",
  day: "numeric",
  year: "numeric",
});

export default function RatingsSection({ itemId }: { itemId: string }) {
  const { data, isLoading, isError } = useCommunityRatings(itemId);
  const reactionMutation = useSetCommunityRatingReaction(itemId);
  const { emblaRef, canScrollPrev, canScrollNext, scrollPrev, scrollNext } = useCarouselEmbla();

  const setReaction = (entry: CommunityRatingEntry, reaction: CommunityRatingReaction) => {
    if (reactionMutation.isPending) return;
    reactionMutation.mutate({
      ratingKey: entry.key,
      reaction: entry.viewer_reaction === reaction ? null : reaction,
    });
  };

  const ratings = data?.ratings ?? [];

  return (
    <section aria-labelledby="household-ratings-heading">
      <div className="mb-5 flex flex-wrap items-baseline justify-between gap-2">
        <h2 id="household-ratings-heading" className="text-xl font-semibold tracking-tight">
          Ratings
        </h2>
        {data?.average_rating != null && (
          <p className="text-muted-foreground text-xs tabular-nums">
            {data.average_rating.toFixed(1)} average from {data.vote_count}{" "}
            {data.vote_count === 1 ? "rating" : "ratings"}
          </p>
        )}
      </div>

      {ratings.length === 0 ? (
        <div className="household-rating-card border-border/55 text-muted-foreground flex h-24 items-center justify-center rounded-xl border px-5 text-sm">
          {isLoading ? "Loading ratings…" : isError ? "Ratings unavailable" : "No ratings yet"}
        </div>
      ) : (
        <div className="group/carousel relative">
          {canScrollPrev && (
            <button
              type="button"
              onClick={scrollPrev}
              className="from-background/95 absolute top-0 bottom-0 left-0 z-10 flex h-11 w-11 cursor-default items-center justify-center self-center bg-gradient-to-r to-transparent opacity-0 transition-opacity duration-200 group-hover/carousel:opacity-100 focus-visible:opacity-100"
              aria-label="Scroll ratings left"
            >
              <ChevronLeft className="text-foreground h-6 w-6" />
            </button>
          )}

          <div
            ref={emblaRef}
            className="embla__viewport -mt-1 overflow-hidden pt-1"
            tabIndex={0}
            aria-label="Ratings carousel"
            onKeyDown={(event) => {
              if (event.key === "ArrowLeft") {
                scrollPrev();
              } else if (event.key === "ArrowRight") {
                scrollNext();
              }
            }}
          >
            <ul role="list" className="embla__container flex list-none gap-3">
              {ratings.map((entry) => (
                <li key={entry.key} className="embla__slide shrink-0">
                  <RatingCard
                    entry={entry}
                    disabled={reactionMutation.isPending}
                    onReact={(reaction) => setReaction(entry, reaction)}
                  />
                </li>
              ))}
            </ul>
          </div>

          {canScrollNext && (
            <button
              type="button"
              onClick={scrollNext}
              className="from-background/95 absolute top-0 right-0 bottom-0 z-10 flex h-11 w-11 cursor-default items-center justify-center self-center bg-gradient-to-l to-transparent opacity-0 transition-opacity duration-200 group-hover/carousel:opacity-100 focus-visible:opacity-100"
              aria-label="Scroll ratings right"
            >
              <ChevronRight className="text-foreground h-6 w-6" />
            </button>
          )}
        </div>
      )}
    </section>
  );
}

function RatingCard({
  entry,
  disabled,
  onReact,
}: {
  entry: CommunityRatingEntry;
  disabled: boolean;
  onReact: (reaction: CommunityRatingReaction) => void;
}) {
  const fallback = Array.from(entry.display_name)[0]?.toUpperCase() ?? "?";

  return (
    <article className="household-rating-card border-border/55 flex h-[210px] w-[240px] flex-col rounded-xl border px-5 py-5 shadow-sm sm:w-[280px]">
      <div className="flex min-h-0 flex-1 flex-col items-center text-center">
        <Avatar className="ring-border/50 mb-2 size-12 ring-1">
          {entry.avatar_url ? <AvatarImage src={entry.avatar_url} alt="" loading="lazy" /> : null}
          <AvatarFallback className="bg-primary/15 text-primary font-bold">
            {fallback}
          </AvatarFallback>
        </Avatar>
        <div className="flex max-w-full items-center justify-center gap-1.5">
          <span className="text-foreground min-w-0 truncate text-sm font-semibold">
            {entry.display_name}
          </span>
          {entry.is_viewer ? (
            <span className="border-primary/35 bg-primary/12 text-primary shrink-0 rounded-full border px-1.5 py-0.5 text-[10px] leading-none font-semibold">
              You
            </span>
          ) : null}
        </div>
        <time className="text-muted-foreground mt-0.5 text-xs" dateTime={entry.rated_at}>
          {formatRatingDate(entry.rated_at)}
        </time>
        <RatingStars rating={entry.rating} />
      </div>

      <div className="mt-3 flex items-center justify-start gap-1.5">
        <ReactionButton
          label="Helpful"
          count={entry.up_count}
          selected={entry.viewer_reaction === "up"}
          disabled={disabled}
          onClick={() => onReact("up")}
        >
          <ThumbsUp aria-hidden="true" className="size-4" />
        </ReactionButton>
        <ReactionButton
          label="Not helpful"
          count={entry.down_count}
          selected={entry.viewer_reaction === "down"}
          disabled={disabled}
          onClick={() => onReact("down")}
        >
          <ThumbsDown aria-hidden="true" className="size-4" />
        </ReactionButton>
      </div>
    </article>
  );
}

function RatingStars({ rating }: { rating: number }) {
  return (
    <div
      className="mt-3 flex items-center justify-center gap-1"
      aria-label={`${rating} out of 5 stars`}
    >
      {Array.from({ length: 5 }, (_, index) => {
        const filled = index < rating;
        return (
          <Star
            key={index}
            aria-hidden="true"
            className={
              filled ? "size-5 fill-yellow-400 text-yellow-400" : "text-muted-foreground/45 size-5"
            }
            strokeWidth={1.5}
          />
        );
      })}
    </div>
  );
}

function ReactionButton({
  label,
  count,
  selected,
  disabled,
  onClick,
  children,
}: {
  label: string;
  count: number;
  selected: boolean;
  disabled: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      aria-label={`${label}: ${count}`}
      aria-pressed={selected}
      disabled={disabled}
      onClick={onClick}
      onPointerDown={(event) => event.stopPropagation()}
      className={`focus-visible:ring-ring inline-flex h-8 min-w-11 items-center justify-center gap-1.5 rounded-full border px-2.5 text-xs font-medium tabular-nums outline-none focus-visible:ring-2 enabled:cursor-pointer disabled:cursor-default ${
        selected
          ? "border-primary/45 bg-primary/15 text-primary"
          : "border-border/55 bg-background/75 text-muted-foreground enabled:hover:border-border enabled:hover:text-foreground"
      }`}
    >
      {children}
      <span>{count}</span>
    </button>
  );
}

function formatRatingDate(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return ratingDateFormatter.format(date);
}
