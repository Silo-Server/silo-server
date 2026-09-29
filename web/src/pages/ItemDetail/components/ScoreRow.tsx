import { Star } from "lucide-react";

interface ScoreRowProps {
  ratingImdb?: number | null;
  ratingRtCritic?: number | null;
  ratingRtAudience?: number | null;
  /** TMDB's average vote, for titles known only from TMDB. */
  ratingTmdb?: number | null;
  /** How many TMDB votes the average rests on. */
  tmdbVoteCount?: number | null;
}

const voteCountFormat = new Intl.NumberFormat(undefined, {
  notation: "compact",
  maximumFractionDigits: 1,
});

export default function ScoreRow({
  ratingImdb,
  ratingRtCritic,
  ratingRtAudience,
  ratingTmdb,
  tmdbVoteCount,
}: ScoreRowProps) {
  if (
    ratingImdb == null &&
    ratingRtCritic == null &&
    ratingRtAudience == null &&
    ratingTmdb == null
  ) {
    return null;
  }

  return (
    <div className="flex flex-wrap items-center gap-5">
      {ratingImdb != null && (
        <div className="flex items-center gap-1.5">
          <Star className="text-primary size-4 fill-current" />
          <span className="text-primary text-[15px] font-bold">{ratingImdb.toFixed(1)}</span>
          <span className="text-muted-foreground/50 text-xs">/10</span>
        </div>
      )}
      {ratingTmdb != null && (
        <div className="flex items-center gap-1.5">
          <Star className="text-primary size-4 fill-current" />
          <span className="text-primary text-[15px] font-bold">{ratingTmdb.toFixed(1)}</span>
          <span className="text-muted-foreground/50 text-xs">/10</span>
          <span className="text-muted-foreground text-xs">
            TMDB
            {tmdbVoteCount ? (
              <span className="tabular-nums"> · {voteCountFormat.format(tmdbVoteCount)} votes</span>
            ) : null}
          </span>
        </div>
      )}
      {ratingRtCritic != null && (
        <div className="flex items-center gap-1.5">
          <span className="text-sm">🍅</span>
          <span className="text-muted-foreground text-[13px] font-medium">{ratingRtCritic}%</span>
        </div>
      )}
      {ratingRtAudience != null && (
        <div className="flex items-center gap-1.5">
          <span className="text-sm">🍿</span>
          <span className="text-muted-foreground text-[13px] font-medium">{ratingRtAudience}%</span>
        </div>
      )}
    </div>
  );
}
