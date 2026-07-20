import { useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";
import { ebookKeys } from "./keys";

// Pace/time-left aren't yet meaningful until enough reading heartbeats have
// landed for the book, so the server returns null rather than a misleading
// estimate.
export type BookReadingStats = {
  pace_fraction_per_hour: number | null;
  time_left_seconds: number | null;
  book_seconds: number;
};

// Aligned with the footer's refetch cadence: pace estimates move slowly
// enough that polling faster would just add load without a visible change.
const READING_STATS_STALE_TIME = 5 * 60 * 1000;

function readingStatsPath(contentID: string): string {
  return `/ebooks/${encodeURIComponent(contentID)}/reading-stats`;
}

export async function fetchBookReadingStats(contentID: string): Promise<BookReadingStats> {
  return api<BookReadingStats>(readingStatsPath(contentID));
}

export function useBookReadingStats(contentId: string | undefined) {
  return useQuery({
    queryKey: ebookKeys.readingStats(contentId),
    queryFn: () => fetchBookReadingStats(contentId!),
    enabled: !!contentId,
    staleTime: READING_STATS_STALE_TIME,
    refetchInterval: READING_STATS_STALE_TIME,
  });
}
