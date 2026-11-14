import type { ItemDetail } from "@/api/types";
import type { MessageKey } from "@/i18n";

// MetadataField enum values matching internal/metadata/types.go
export const FIELD_NAME = 0;
export const FIELD_OVERVIEW = 1;
const FIELD_GENRES = 2;
const FIELD_STUDIOS = 3;
const FIELD_RATING = 6;
const FIELD_TAGS = 8;
export const FIELD_RUNTIME = 7;
const FIELD_CONTENT_RATING = 9;
export const FIELD_IMAGES = 10;
const FIELD_AIR_SCHEDULE = 11;
export const FIELD_RELEASE_DATES = 13;

export const AIR_TIMEZONES = [
  "America/New_York",
  "America/Chicago",
  "America/Denver",
  "America/Los_Angeles",
  "Europe/London",
  "Europe/Paris",
  "Asia/Tokyo",
  "Asia/Seoul",
  "Australia/Sydney",
];

// Example values shown as placeholders: ID and time zone formats, not text to translate.
export const EXAMPLE_TIMEZONE = "America/New_York";
export const EXAMPLE_IMDB_ID = "tt...";

export type Section = "general" | "dates" | "tags" | "ids" | "images";

export const SECTIONS: { key: Section; label: MessageKey; types: ItemDetail["type"][] }[] = [
  {
    key: "general",
    label: "web.item.editMetadata.sectionGeneral",
    types: ["movie", "series", "season", "episode", "audiobook"],
  },
  {
    key: "dates",
    label: "web.item.editMetadata.sectionDates",
    types: ["movie", "series", "season", "episode", "audiobook"],
  },
  {
    key: "tags",
    label: "web.item.editMetadata.sectionTags",
    types: ["movie", "series", "audiobook"],
  },
  {
    key: "ids",
    label: "web.item.editMetadata.sectionIds",
    types: ["movie", "series", "season", "episode"],
  },
  // Episodes are excluded: the image search endpoint only returns series-level
  // posters/backdrops/logos, and episodes store stills — there is nothing an
  // episode apply could legitimately show or persist from this dialog.
  {
    key: "images",
    label: "web.item.editMetadata.sectionImages",
    types: ["movie", "series", "season"],
  },
];

// Maps form field names to their MetadataField lock value.
export const FIELD_LOCK_MAP: Record<string, number> = {
  title: FIELD_NAME,
  sort_title: FIELD_NAME,
  original_title: FIELD_NAME,
  // The server keeps the tagline under the overview lock.
  tagline: FIELD_OVERVIEW,
  overview: FIELD_OVERVIEW,
  genres: FIELD_GENRES,
  studios: FIELD_STUDIOS,
  networks: FIELD_STUDIOS,
  countries: FIELD_TAGS,
  rating_imdb: FIELD_RATING,
  rating_tmdb: FIELD_RATING,
  rating_rt_critic: FIELD_RATING,
  rating_rt_audience: FIELD_RATING,
  runtime: FIELD_RUNTIME,
  content_rating: FIELD_CONTENT_RATING,
  air_time: FIELD_AIR_SCHEDULE,
  air_timezone: FIELD_AIR_SCHEDULE,
  year: FIELD_RELEASE_DATES,
  release_date: FIELD_RELEASE_DATES,
  first_air_date: FIELD_RELEASE_DATES,
  last_air_date: FIELD_RELEASE_DATES,
};

export function initFormState(item: ItemDetail) {
  return {
    title: item.title ?? "",
    sort_title: item.sort_title ?? "",
    original_title: item.original_title ?? "",
    overview: item.overview ?? "",
    tagline: item.tagline ?? "",
    content_rating: item.content_rating ?? "",
    year: item.year ?? 0,
    runtime: item.runtime ?? 0,
    genres: item.genres ?? [],
    studios: item.studios ?? [],
    networks: item.networks ?? [],
    countries: item.countries ?? [],
    release_date: item.release_date ?? "",
    first_air_date: item.first_air_date ?? "",
    last_air_date: item.last_air_date ?? "",
    air_time: item.air_time ?? "",
    air_timezone: item.air_timezone ?? "",
    air_date: item.air_date ?? "",
    rating_imdb: item.rating_imdb,
    rating_tmdb: item.rating_tmdb,
    rating_rt_critic: item.rating_rt_critic,
    rating_rt_audience: item.rating_rt_audience,
    imdb_id: item.imdb_id ?? "",
    tmdb_id: item.tmdb_id ?? "",
    tvdb_id: item.tvdb_id ?? "",
    season_number: item.season_number ?? undefined,
    episode_number: item.episode_number ?? undefined,
  };
}
