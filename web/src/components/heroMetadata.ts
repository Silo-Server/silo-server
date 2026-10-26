import type { DisplayRating, SectionItem } from "@/api/types";
import { primaryCardRating } from "@/components/ratings/ratings";

function isPositiveFinite(value: number | undefined | null): value is number {
  return value != null && Number.isFinite(value) && value > 0;
}

function resolveHeroRuntimeSeconds(item: SectionItem): number | null {
  if (isPositiveFinite(item.runtime)) {
    const runtimeSeconds = item.runtime * 60;
    if (isPositiveFinite(runtimeSeconds)) {
      return runtimeSeconds;
    }
  }
  if (isPositiveFinite(item.duration_seconds)) {
    return item.duration_seconds;
  }
  return null;
}

function formatRuntime(seconds: number | undefined | null): string | null {
  if (!isPositiveFinite(seconds)) return null;
  const minutes = Math.round(seconds / 60);
  if (minutes <= 0) return null;
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.floor(minutes / 60);
  const remaining = minutes % 60;
  return remaining === 0 ? `${hours}h` : `${hours}h ${remaining}m`;
}

export interface HeroMetadataEntry {
  key: string;
  label: string;
  /** Set on the rating entry, which renders as its source mark and score. */
  rating?: DisplayRating;
}

function isNonNegativeInteger(value: number | undefined | null): value is number {
  return value != null && Number.isInteger(value) && value >= 0;
}

export function formatHeroMetadata(item: SectionItem): HeroMetadataEntry[] {
  const entries: HeroMetadataEntry[] = [];
  const runtime = formatRuntime(resolveHeroRuntimeSeconds(item));
  const contentRating = item.content_rating?.trim().toUpperCase();

  if (item.type === "episode") {
    if (isNonNegativeInteger(item.season_number) && isNonNegativeInteger(item.episode_number)) {
      entries.push({
        key: "episode-identity",
        label: `S${item.season_number} · E${item.episode_number}`,
      });
    }
    if (runtime) entries.push({ key: "runtime", label: runtime });
    if (contentRating) entries.push({ key: "content-rating", label: contentRating });
    return entries;
  }

  if (Number.isInteger(item.year) && item.year > 0) {
    entries.push({ key: "year", label: String(item.year) });
  }
  if (runtime) entries.push({ key: "runtime", label: runtime });
  const rating = primaryCardRating(item);
  if (rating) {
    entries.push({ key: "rating", label: `${rating.name} ${rating.display}`, rating });
  }

  const genres = [...new Set((item.genres ?? []).map((genre) => genre.trim()).filter(Boolean))];
  genres.slice(0, 2).forEach((genre, index) => {
    entries.push({ key: `genre-${index}`, label: genre });
  });

  if (contentRating) entries.push({ key: "content-rating", label: contentRating });
  return entries;
}
