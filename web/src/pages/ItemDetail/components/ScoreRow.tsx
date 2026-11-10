import { RatingEntry } from "@/components/ratings/RatingEntry";
import type { DisplayRating } from "@/components/ratings/ratings";
import { formatLocale } from "@/lib/appLocale";
import { useT } from "@/i18n";

interface ScoreRowProps {
  /** The ratings the server chose for this title, in display order. */
  ratings?: readonly DisplayRating[] | null;
  /** How many TMDB votes a request title's TMDB score rests on. */
  tmdbVoteCount?: number | null;
}

export default function ScoreRow({ ratings, tmdbVoteCount }: ScoreRowProps) {
  const t = useT();
  if (!ratings?.length) return null;

  return (
    <div className="text-primary flex flex-wrap items-center gap-x-5 gap-y-2">
      {ratings.map((rating) => (
        <span key={rating.source} className="inline-flex items-center gap-1.5">
          <RatingEntry rating={rating} />
          {rating.source === "tmdb" && tmdbVoteCount ? (
            <span className="text-muted-foreground text-xs tabular-nums">
              {t("web.detail.overview.tmdbVotes", {
                count: tmdbVoteCount,
                count_text: tmdbVoteCount.toLocaleString(formatLocale(), {
                  notation: "compact",
                  maximumFractionDigits: 1,
                }),
              })}
            </span>
          ) : null}
        </span>
      ))}
    </div>
  );
}
