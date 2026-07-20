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

export interface ReadingHistoryTotals {
  today_seconds: number;
  week_seconds: number;
  month_seconds: number;
  all_time_seconds: number;
}

export interface ReadingHistoryDay {
  date: string;
  seconds: number;
}

export interface ReadingHistoryBook {
  content_id: string;
  title: string;
  seconds: number;
  last_read_at: string;
}

export interface ReadingHistorySession {
  content_id: string;
  title: string;
  started_at: string;
  duration_seconds: number;
  start_fraction: number;
  end_fraction: number;
}

export interface ReadingHistory {
  totals: ReadingHistoryTotals;
  days: ReadingHistoryDay[];
  books: ReadingHistoryBook[];
  sessions: ReadingHistorySession[];
}

// History defaults to the server's built-in range (the last year) when no
// from/to bounds are given, so the reading stats page can render a full
// heatmap without the caller having to compute dates itself.
const READING_HISTORY_STALE_TIME = 5 * 60 * 1000;

function readingHistoryPath(from?: string, to?: string): string {
  const params = new URLSearchParams();
  if (from) params.set("from", from);
  if (to) params.set("to", to);
  const query = params.toString();
  return query ? `/ebooks/reading-stats?${query}` : "/ebooks/reading-stats";
}

export async function fetchReadingHistory(from?: string, to?: string): Promise<ReadingHistory> {
  return api<ReadingHistory>(readingHistoryPath(from, to));
}

export function useReadingHistory(from?: string, to?: string) {
  return useQuery({
    queryKey: ebookKeys.readingHistory(from, to),
    queryFn: () => fetchReadingHistory(from, to),
    staleTime: READING_HISTORY_STALE_TIME,
  });
}
