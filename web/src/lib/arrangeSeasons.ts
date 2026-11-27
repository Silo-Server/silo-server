// Shared state shapes of the season arranger (ArrangeSeasonsDialog).
import type { CustomSeason, CustomSeasons } from "@/hooks/queries/episodeOrder";

export const SPECIALS = "specials";

export interface ArrangedSeason {
  /** Stable while the arranger is open; numbers follow the list order. */
  id: string;
  title: string;
  overview: string;
  /** An uploaded poster, which stays with the season wherever it moves. */
  poster?: CustomSeasonPosterUpload;
  episodeKeys: string[];
}

export interface CustomSeasonPosterUpload {
  ref: NonNullable<CustomSeason["poster"]>;
  url?: string;
}

export interface ProviderSeason {
  title: string;
  overview: string;
  posterUrl?: string;
}

export const withoutKeys = (keys: string[], removed: Set<string>) =>
  keys.filter((k) => !removed.has(k));

let seq = 0;
export const newId = () => `season-${Date.now().toString(36)}-${seq++}`;

export function arrangedOf(data: CustomSeasons): { seasons: ArrangedSeason[]; specials: string[] } {
  const seasons: ArrangedSeason[] = [];
  let specials: string[] = [];
  for (const season of data.seasons) {
    const keys = season.episodes.map((e) => e.key);
    if (season.number === 0) specials = keys;
    else
      seasons.push({
        id: newId(),
        title: season.title,
        overview: season.overview,
        poster: season.poster ? { ref: season.poster, url: season.poster_url } : undefined,
        episodeKeys: keys,
      });
  }
  return { seasons, specials };
}
