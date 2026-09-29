import { RatingEntry } from "@/components/ratings/RatingEntry";
import { PHONE_RATING_LIMIT, type DisplayRating } from "@/components/ratings/ratings";
import { cn } from "@/lib/utils";

interface ScoreRowProps {
  /** The ratings the server chose for this title, in display order. */
  ratings?: readonly DisplayRating[] | null;
  /** How many TMDB votes a request title's TMDB score rests on. */
  tmdbVoteCount?: number | null;
}

const voteCountFormat = new Intl.NumberFormat(undefined, {
  notation: "compact",
  maximumFractionDigits: 1,
});

export default function ScoreRow({ ratings, tmdbVoteCount }: ScoreRowProps) {
  if (!ratings?.length) return null;

  return (
    <div className="text-primary flex flex-wrap items-center gap-x-5 gap-y-2">
      {ratings.map((rating, index) => (
        <span
          key={rating.source}
          className={cn(
            "inline-flex items-center gap-1.5",
            // Phone widths show the first three only, so the row stays on one line.
            index >= PHONE_RATING_LIMIT && "max-sm:hidden",
          )}
        >
          <RatingEntry rating={rating} />
          {rating.source === "tmdb" && tmdbVoteCount ? (
            <span className="text-muted-foreground text-xs tabular-nums">
              {voteCountFormat.format(tmdbVoteCount)} votes
            </span>
          ) : null}
        </span>
      ))}
    </div>
  );
}
