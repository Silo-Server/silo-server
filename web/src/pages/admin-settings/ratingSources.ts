export const EXTRA_RATING_SOURCES_KEY = "catalog.extra_rating_sources";

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
