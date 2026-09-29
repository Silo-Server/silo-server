export const EXTRA_RATING_SOURCES_KEY = "catalog.extra_rating_sources";

/**
 * The rating sources an administrator can show in addition to IMDb and TMDB,
 * which are always shown. The order matches the server's display order.
 * Each needs a metadata provider that reports it, such as MDBList.
 */
export const OPTIONAL_RATING_SOURCES = [
  { source: "rt_critic", label: "Rotten Tomatoes critics" },
  { source: "rt_audience", label: "Rotten Tomatoes audience" },
  { source: "metacritic", label: "Metacritic" },
  { source: "metacritic_user", label: "Metacritic users" },
  { source: "letterboxd", label: "Letterboxd" },
  { source: "trakt", label: "Trakt" },
  { source: "rogerebert", label: "Roger Ebert" },
  { source: "myanimelist", label: "MyAnimeList" },
  { source: "mdblist", label: "MDBList score" },
] as const;

export function parseRatingSources(value: string | undefined): string[] {
  return (value ?? "")
    .split(",")
    .map((entry) => entry.trim())
    .filter(Boolean);
}

/**
 * Turns one source on or off, keeping every other entry, including sources a
 * plugin declared that this page does not list.
 */
export function toggleRatingSource(value: string | undefined, source: string, on: boolean): string {
  const rest = parseRatingSources(value).filter((entry) => entry !== source);
  return (on ? [...rest, source] : rest).join(",");
}
