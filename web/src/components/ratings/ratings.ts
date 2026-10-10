import type { DisplayRating } from "@/api/types";

export type { DisplayRating };

function scoreOutOfTen(value: number | null | undefined): value is number {
  return value != null && Number.isFinite(value) && value > 0 && value <= 10;
}

/**
 * A score out of 10, such as a card's `rating_imdb`, with one decimal place.
 * Halves round away from zero (7.35 reads "7.4"), as the server's `display`
 * does, so a card never reads differently from its title page. `toFixed`
 * alone would round 7.35 down: its binary value is just under 7.35.
 */
export function formatOutOfTen(value: number): string {
  return (Math.round(value * 10) / 10).toFixed(1);
}

/** A score out of 100, such as a card's `rating_rt_critic`, as a percentage. */
export function formatPercent(value: number): string {
  return `${Math.round(value)}%`;
}

function outOfTen(source: "imdb" | "tmdb", value: number): DisplayRating {
  return {
    source,
    name: source === "imdb" ? "IMDb" : "TMDB",
    score: value * 10,
    display: formatOutOfTen(value),
  };
}

/** A TMDB score known only from TMDB, such as a title someone can request. */
export function tmdbRating(value: number | null | undefined): DisplayRating | null {
  return scoreOutOfTen(value) ? outOfTen("tmdb", value) : null;
}

/**
 * The one rating a card-sized summary (the home hero, Watch Tonight) shows:
 * IMDb, or TMDB when there is no IMDb score. Every Silo client follows the
 * same rule.
 */
export function primaryCardRating(item: {
  rating_imdb?: number | null;
  rating_tmdb?: number | null;
}): DisplayRating | null {
  if (scoreOutOfTen(item.rating_imdb)) return outOfTen("imdb", item.rating_imdb);
  return tmdbRating(item.rating_tmdb);
}
