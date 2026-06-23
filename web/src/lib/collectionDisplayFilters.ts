import type { UserCollectionMediaFilter, UserCollectionWatchFilter } from "@/api/types";

export const COLLECTION_WATCH_FILTER_OPTIONS: Array<{
  value: UserCollectionWatchFilter;
  label: string;
}> = [
  { value: "all", label: "All" },
  { value: "unwatched", label: "Unwatched" },
  { value: "watched", label: "Watched" },
];

export const COLLECTION_MEDIA_FILTER_OPTIONS: Array<{
  value: UserCollectionMediaFilter;
  label: string;
}> = [
  { value: "all", label: "All" },
  { value: "movie", label: "Movies" },
  { value: "series", label: "Shows" },
];

export function normalizeCollectionWatchFilter(value: unknown): UserCollectionWatchFilter {
  return value === "unwatched" || value === "watched" ? value : "all";
}

export function normalizeCollectionMediaFilter(value: unknown): UserCollectionMediaFilter {
  return value === "movie" || value === "series" ? value : "all";
}

export function collectionWatchFilterLabel(value: UserCollectionWatchFilter): string {
  return COLLECTION_WATCH_FILTER_OPTIONS.find((option) => option.value === value)?.label ?? "All";
}

export function collectionMediaFilterLabel(value: UserCollectionMediaFilter): string {
  return COLLECTION_MEDIA_FILTER_OPTIONS.find((option) => option.value === value)?.label ?? "All";
}
