/**
 * TMDB's genre lists. Request routing matches on genre IDs, not names, because
 * names follow the server's configured TMDB language while the IDs never
 * change. Movies and series use separate lists that share some IDs (16 is
 * Animation in both) and differ elsewhere (series fold Action and Adventure
 * into 10759).
 */
export interface TmdbGenre {
  id: number;
  name: string;
}

export const TMDB_MOVIE_GENRES: readonly TmdbGenre[] = [
  { id: 28, name: "Action" },
  { id: 12, name: "Adventure" },
  { id: 16, name: "Animation" },
  { id: 35, name: "Comedy" },
  { id: 80, name: "Crime" },
  { id: 99, name: "Documentary" },
  { id: 18, name: "Drama" },
  { id: 10751, name: "Family" },
  { id: 14, name: "Fantasy" },
  { id: 36, name: "History" },
  { id: 27, name: "Horror" },
  { id: 10402, name: "Music" },
  { id: 9648, name: "Mystery" },
  { id: 10749, name: "Romance" },
  { id: 878, name: "Science Fiction" },
  { id: 10770, name: "TV Movie" },
  { id: 53, name: "Thriller" },
  { id: 10752, name: "War" },
  { id: 37, name: "Western" },
];

export const TMDB_SERIES_GENRES: readonly TmdbGenre[] = [
  { id: 10759, name: "Action & Adventure" },
  { id: 16, name: "Animation" },
  { id: 35, name: "Comedy" },
  { id: 80, name: "Crime" },
  { id: 99, name: "Documentary" },
  { id: 18, name: "Drama" },
  { id: 10751, name: "Family" },
  { id: 10762, name: "Kids" },
  { id: 9648, name: "Mystery" },
  { id: 10763, name: "News" },
  { id: 10764, name: "Reality" },
  { id: 10765, name: "Sci-Fi & Fantasy" },
  { id: 10766, name: "Soap" },
  { id: 10767, name: "Talk" },
  { id: 10768, name: "War & Politics" },
  { id: 37, name: "Western" },
];

export function tmdbGenresFor(mediaType: "movie" | "series"): readonly TmdbGenre[] {
  return mediaType === "series" ? TMDB_SERIES_GENRES : TMDB_MOVIE_GENRES;
}

/**
 * The English name of a genre ID. A title's facts can carry a genre from the
 * other media type's list, so both are searched before giving up.
 */
export function tmdbGenreName(id: number, mediaType: "movie" | "series"): string {
  const own = tmdbGenresFor(mediaType).find((genre) => genre.id === id);
  if (own) return own.name;
  const other = tmdbGenresFor(mediaType === "series" ? "movie" : "series").find(
    (genre) => genre.id === id,
  );
  return other?.name ?? `Genre ${id}`;
}
