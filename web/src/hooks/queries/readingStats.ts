import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api } from "@/api/client";
import { ebookKeys } from "./keys";

/**
 * The viewer's IANA timezone (e.g. "Europe/Amsterdam"), as reported by the
 * browser. Sent as the `tz` query param on reading-history and
 * reading-motivation fetches so the server can attribute streaks/totals to
 * the viewer's local calendar days rather than UTC. Falls back to "UTC" when
 * the runtime can't resolve one (e.g. some test/SSR environments).
 */
export function clientTimezone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone ?? "UTC";
}

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

function readingHistoryPath(from?: string, to?: string, tz?: string): string {
  const params = new URLSearchParams();
  if (from) params.set("from", from);
  if (to) params.set("to", to);
  if (tz) params.set("tz", tz);
  const query = params.toString();
  return query ? `/ebooks/reading-stats?${query}` : "/ebooks/reading-stats";
}

export async function fetchReadingHistory(
  from?: string,
  to?: string,
  tz?: string,
): Promise<ReadingHistory> {
  return api<ReadingHistory>(readingHistoryPath(from, to, tz));
}

export function useReadingHistory(from?: string, to?: string, tz?: string) {
  return useQuery({
    queryKey: ebookKeys.readingHistory(from, to, tz),
    queryFn: () => fetchReadingHistory(from, to, tz),
    staleTime: READING_HISTORY_STALE_TIME,
  });
}

// --- Reading motivation (streaks, goals, achievements, Reading DNA) ---

export interface ReadingMotivationStreak {
  current_days: number;
  longest_days: number;
  today_seconds: number;
  today_qualified: boolean;
}

export interface ReadingMotivationGoals {
  books_per_year: number | null;
  hours_per_year: number | null;
  books_finished_ytd: number;
  hours_ytd: number;
  books_on_track_for: number;
  hours_on_track_for: number;
}

export interface ReadingMotivationChallenge {
  target_seconds: number;
  month_seconds: number;
  percent: number;
}

export interface ReadingMotivationAchievement {
  id: string;
  category: string;
  name: string;
  description: string;
  achieved_at: string | null;
}

export interface ReadingMotivationGenre {
  name: string;
  seconds: number;
}

export interface ReadingMotivationAuthor {
  name: string;
  seconds: number;
}

export interface ReadingMotivationDNA {
  genres: ReadingMotivationGenre[];
  authors: ReadingMotivationAuthor[];
  diversity_score: number;
  avg_session_seconds: number;
  hours_by_bucket: Record<string, number>;
  projected_year_hours: number;
}

export interface ReadingMotivation {
  streak: ReadingMotivationStreak;
  goals: ReadingMotivationGoals;
  challenge: ReadingMotivationChallenge;
  achievements: ReadingMotivationAchievement[];
  dna: ReadingMotivationDNA;
}

const READING_MOTIVATION_STALE_TIME = 5 * 60 * 1000;

function readingMotivationPath(tz: string): string {
  const params = new URLSearchParams({ tz });
  return `/ebooks/reading-motivation?${params.toString()}`;
}

export async function fetchReadingMotivation(tz: string): Promise<ReadingMotivation> {
  return api<ReadingMotivation>(readingMotivationPath(tz));
}

export function useReadingMotivation() {
  const tz = clientTimezone();
  return useQuery({
    queryKey: ebookKeys.readingMotivation(tz),
    queryFn: () => fetchReadingMotivation(tz),
    staleTime: READING_MOTIVATION_STALE_TIME,
  });
}

export interface ReadingGoalsInput {
  books_per_year: number | null;
  hours_per_year: number | null;
}

// PUT semantics: both fields are always sent, even when only one changed —
// an absent field would clear the corresponding goal server-side (see
// HandlePutGoals), so callers must always send the current value of both.
export async function putReadingGoals(goals: ReadingGoalsInput): Promise<void> {
  await api<void>("/ebooks/reading-goals", {
    method: "PUT",
    body: JSON.stringify(goals),
  });
}

// Wraps putReadingGoals in the repo's mutation convention: on a successful
// save, invalidate reading-motivation so goals/progress refetch with the
// server's latest values. Callers use mutateAsync so a rejected save
// propagates back to them (see GoalsForm in MotivationSections.tsx, which
// relies on that rejection to avoid marking an unsaved value as saved).
export function useSaveReadingGoals() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (goals: ReadingGoalsInput) => putReadingGoals(goals),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ebookKeys.readingMotivation() });
    },
  });
}
