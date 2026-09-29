import type { DisplayRating } from "@/api/types";

export type { DisplayRating };

/**
 * A phone-width title page shows at most this many ratings, the first ones in
 * the server's order, so the row stays on one line. Wider layouts show all of
 * them. Every Silo client follows the same rule.
 */
export const PHONE_RATING_LIMIT = 3;

function scoreOutOfTen(value: number | null | undefined): value is number {
  return value != null && Number.isFinite(value) && value > 0 && value <= 10;
}

function outOfTen(source: "imdb" | "tmdb", value: number): DisplayRating {
  return {
    source,
    name: source === "imdb" ? "IMDb" : "TMDB",
    score: value * 10,
    display: (Math.round(value * 10) / 10).toFixed(1),
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
