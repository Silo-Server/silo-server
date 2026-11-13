import { t, type MessageKey, type TextValues } from "@/i18n";

/**
 * TMDB's genre lists. Request routing matches on genre IDs, not names, because
 * names follow the server's configured TMDB language while the IDs never
 * change. Movies and series use separate lists that share some IDs (16 is
 * Animation in both) and differ elsewhere (series fold Action and Adventure
 * into 10759).
 */
export interface TmdbGenre {
  id: number;
  nameKey: MessageKey;
  /** The genre's name in the interface language, read when used. */
  readonly name: string;
}

/** Formats a message: `t` for a name shown, `englishText` for a name the client saves. */
type Text = (id: MessageKey, values?: TextValues) => string;

function genre(id: number, nameKey: MessageKey): TmdbGenre {
  return {
    id,
    nameKey,
    get name() {
      return t(nameKey);
    },
  };
}

export const TMDB_MOVIE_GENRES: readonly TmdbGenre[] = [
  genre(28, "web.common.genres.action"),
  genre(12, "web.common.genres.adventure"),
  genre(16, "web.common.genres.animation"),
  genre(35, "web.common.genres.comedy"),
  genre(80, "web.common.genres.crime"),
  genre(99, "web.common.genres.documentary"),
  genre(18, "web.common.genres.drama"),
  genre(10751, "web.common.genres.family"),
  genre(14, "web.common.genres.fantasy"),
  genre(36, "web.common.genres.history"),
  genre(27, "web.common.genres.horror"),
  genre(10402, "web.common.genres.music"),
  genre(9648, "web.common.genres.mystery"),
  genre(10749, "web.common.genres.romance"),
  genre(878, "web.common.genres.scienceFiction"),
  genre(10770, "web.common.genres.tvMovie"),
  genre(53, "web.common.genres.thriller"),
  genre(10752, "web.common.genres.war"),
  genre(37, "web.common.genres.western"),
];

export const TMDB_SERIES_GENRES: readonly TmdbGenre[] = [
  genre(10759, "web.common.genres.actionAdventure"),
  genre(16, "web.common.genres.animation"),
  genre(35, "web.common.genres.comedy"),
  genre(80, "web.common.genres.crime"),
  genre(99, "web.common.genres.documentary"),
  genre(18, "web.common.genres.drama"),
  genre(10751, "web.common.genres.family"),
  genre(10762, "web.common.genres.kids"),
  genre(9648, "web.common.genres.mystery"),
  genre(10763, "web.common.genres.news"),
  genre(10764, "web.common.genres.reality"),
  genre(10765, "web.common.genres.sciFiFantasy"),
  genre(10766, "web.common.genres.soap"),
  genre(10767, "web.common.genres.talk"),
  genre(10768, "web.common.genres.warPolitics"),
  genre(37, "web.common.genres.western"),
];

export function tmdbGenresFor(mediaType: "movie" | "series"): readonly TmdbGenre[] {
  return mediaType === "series" ? TMDB_SERIES_GENRES : TMDB_MOVIE_GENRES;
}

/**
 * The name of a genre ID, in the interface language or, with `englishText`,
 * in English for a name the client saves. A title's facts can carry a genre
 * from the other media type's list, so both are searched before giving up.
 */
export function tmdbGenreName(id: number, mediaType: "movie" | "series", tx: Text = t): string {
  const own = tmdbGenresFor(mediaType).find((genre) => genre.id === id);
  if (own) return tx(own.nameKey);
  const other = tmdbGenresFor(mediaType === "series" ? "movie" : "series").find(
    (genre) => genre.id === id,
  );
  return other ? tx(other.nameKey) : tx("web.common.genres.unknown", { id: String(id) });
}
