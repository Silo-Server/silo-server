import { useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";
import type {
  AdminDownloadsStats,
  AdminPlaybackActivity,
  AdminTimeseries,
  AdminTopActivity,
} from "@/api/types";
import { adminKeys } from "../keys";

/**
 * Read hooks for the admin dashboard's aggregate insight endpoints.
 *
 * None of them carries a `refetchInterval` on purpose: the dashboard page owns
 * pacing through its visibility-gated 60s loop (and the manual Refresh button),
 * so a per-hook timer would only add fetches while the tab is hidden. The stale
 * times sit just under that loop so a widget mounting mid-cycle serves from
 * cache instead of firing a second request for the same window.
 */

/** Just under the dashboard's 60s refresh cadence. */
const SAMPLED_SERIES_STALE_TIME = 55_000;
/** Seven-day rollups move slowly; the server caches them for 5 minutes too. */
const TOP_ACTIVITY_STALE_TIME = 5 * 60_000;

export const DEFAULT_INSIGHT_HOURS = 24;
export const DEFAULT_TOP_ACTIVITY_DAYS = 7;
export const DEFAULT_DOWNLOADS_TOP_LIMIT = 10;

// Mirrors the server-side clamps (internal/api/handlers/admin_stats_*.go). The
// client applies them before building the key so two requests the server would
// answer identically share one cache entry instead of splitting into a key the
// response does not actually match.
const MIN_HOURS = 1;
// 744 hours is 31 days: the sampler's retention window, and the widest window
// the endpoints answer.
const MAX_HOURS = 744;
const MIN_DAYS = 1;
const MAX_DAYS = 30;
const MIN_DOWNLOADS_TOP_LIMIT = 1;
const MAX_DOWNLOADS_TOP_LIMIT = 25;

function clampWindow(value: number, min: number, max: number, fallback: number): number {
  if (!Number.isFinite(value)) {
    return fallback;
  }
  return Math.min(Math.max(Math.trunc(value), min), max);
}

/** Hours window accepted by the timeseries and playback-activity endpoints. */
export function normalizeInsightHours(hours: number = DEFAULT_INSIGHT_HOURS): number {
  return clampWindow(hours, MIN_HOURS, MAX_HOURS, DEFAULT_INSIGHT_HOURS);
}

/** Days window accepted by the top-activity endpoint. */
export function normalizeTopActivityDays(days: number = DEFAULT_TOP_ACTIVITY_DAYS): number {
  return clampWindow(days, MIN_DAYS, MAX_DAYS, DEFAULT_TOP_ACTIVITY_DAYS);
}

/** Top-list size accepted by the downloads stats endpoint. */
export function normalizeDownloadsTopLimit(limit: number = DEFAULT_DOWNLOADS_TOP_LIMIT): number {
  return clampWindow(
    limit,
    MIN_DOWNLOADS_TOP_LIMIT,
    MAX_DOWNLOADS_TOP_LIMIT,
    DEFAULT_DOWNLOADS_TOP_LIMIT,
  );
}

export function adminTimeseriesPath(hours: number): string {
  return `/admin/stats/timeseries?hours=${normalizeInsightHours(hours)}`;
}

export function adminPlaybackActivityPath(hours: number): string {
  return `/admin/stats/playback-activity?hours=${normalizeInsightHours(hours)}`;
}

export function adminTopActivityPath(days: number): string {
  return `/admin/stats/top-activity?days=${normalizeTopActivityDays(days)}`;
}

export function adminDownloadsStatsPath(limit: number): string {
  return `/admin/stats/downloads?limit=${normalizeDownloadsTopLimit(limit)}`;
}

/** Minute-resolution stream counts and egress for the last `hours`. */
export function useAdminTimeseries(hours: number = DEFAULT_INSIGHT_HOURS) {
  const window = normalizeInsightHours(hours);
  return useQuery({
    queryKey: adminKeys.dashboardTimeseries(window),
    queryFn: () => api<AdminTimeseries>(adminTimeseriesPath(window)),
    staleTime: SAMPLED_SERIES_STALE_TIME,
  });
}

/** Hourly playback counts by method, plus reliability and profile scalars. */
export function useAdminPlaybackActivity(hours: number = DEFAULT_INSIGHT_HOURS) {
  const window = normalizeInsightHours(hours);
  return useQuery({
    queryKey: adminKeys.playbackActivity(window),
    queryFn: () => api<AdminPlaybackActivity>(adminPlaybackActivityPath(window)),
    staleTime: SAMPLED_SERIES_STALE_TIME,
  });
}

/** Most-played titles and most-active profiles over the last `days`. */
export function useAdminTopActivity(days: number = DEFAULT_TOP_ACTIVITY_DAYS) {
  const window = normalizeTopActivityDays(days);
  return useQuery({
    queryKey: adminKeys.topActivity(window),
    queryFn: () => api<AdminTopActivity>(adminTopActivityPath(window)),
    staleTime: TOP_ACTIVITY_STALE_TIME,
  });
}

/** Offline-download aggregate: who keeps what downloaded, and how much. */
export function useAdminDownloadsStats(limit: number = DEFAULT_DOWNLOADS_TOP_LIMIT) {
  const topLimit = normalizeDownloadsTopLimit(limit);
  return useQuery({
    queryKey: adminKeys.downloadsStats(topLimit),
    queryFn: () => api<AdminDownloadsStats>(adminDownloadsStatsPath(topLimit)),
    staleTime: SAMPLED_SERIES_STALE_TIME,
  });
}
