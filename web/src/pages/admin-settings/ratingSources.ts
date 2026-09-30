import type { QueryClient } from "@tanstack/react-query";

import {
  RATING_POLICY_SETTLE_MS,
  refreshRatingChoice,
} from "@/hooks/queries/ratingsSurfaceRefresh";

export { RATING_POLICY_SETTLE_MS };

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

interface RatingSourceEntry {
  source: string;
  label: string;
  always_shown: boolean;
  provider?: string;
}

/**
 * The ratings an administrator can turn on, grouped by the metadata plugin
 * that declares them, in the order the server lists them. IMDb and TMDB are
 * always shown, so they are left out.
 */
export function groupRatingSourcesByPlugin<T extends RatingSourceEntry>(
  sources: readonly T[],
): { provider: string; sources: T[] }[] {
  const groups = new Map<string, T[]>();
  for (const source of sources) {
    if (source.always_shown || !source.provider) continue;
    const group = groups.get(source.provider) ?? [];
    group.push(source);
    groups.set(source.provider, group);
  }
  return Array.from(groups, ([provider, entries]) => ({ provider, sources: entries }));
}

/**
 * The sources the setting turns on that no listed source declares, such as
 * one whose plugin was disabled, in setting order. They show nothing until a
 * plugin declares them again, and are listed so an administrator can turn
 * them off.
 */
export function undeclaredRatingSources(
  turnedOn: readonly string[],
  sources: readonly RatingSourceEntry[],
): string[] {
  const listed = new Set(sources.map((entry) => entry.source));
  return Array.from(new Set(turnedOn)).filter((source) => !listed.has(source));
}

/**
 * Saves the settings form and, when the rating choice was among the changes,
 * refreshes every cached rating surface (title pages, cards, the ratings
 * capability) now and again once the server's cached copy has expired.
 */
export async function saveAndRefreshRatings(
  form: { isDirty: (key: string) => boolean; save: () => Promise<void> },
  queryClient: QueryClient,
  schedule?: (run: () => void, ms: number) => unknown,
): Promise<void> {
  const ratingsChanged = form.isDirty(EXTRA_RATING_SOURCES_KEY);
  await form.save();
  if (ratingsChanged) refreshRatingChoice(queryClient, schedule);
}
