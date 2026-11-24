import type { WidgetRange } from "@/components/admin/dashboard/types";
import type { Translate } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";
import { formatDateTime, preferredDateLocale } from "@/lib/datetime";
import type { MessageKey } from "@/locales/keys";

export const INSIGHTS_TABS = [
  "playback",
  "titles",
  "library",
  "people",
  "devices",
  "bandwidth",
] as const;
export type InsightsTab = (typeof INSIGHTS_TABS)[number];

export const INSIGHTS_RANGES = ["7d", "30d", "90d", "1y", "all", "custom"] as const;
export type InsightsRange = (typeof INSIGHTS_RANGES)[number];

/** The Insights preset closest to a dashboard widget range; sub-week ranges open the 7-day view. */
export function widgetRangeToInsightsRange(range: WidgetRange): Exclude<InsightsRange, "custom"> {
  switch (range) {
    case "month":
      return "30d";
    case "quarter":
      return "90d";
    case "year":
      return "1y";
    case "all":
      return "all";
    default:
      return "7d";
  }
}

/** The dashboard widget range for an Insights preset; a custom range has none. */
export function insightsRangeToWidgetRange(range: InsightsRange): WidgetRange | undefined {
  switch (range) {
    case "7d":
      return "week";
    case "30d":
      return "month";
    case "90d":
      return "quarter";
    case "1y":
      return "year";
    case "all":
      return "all";
    default:
      return undefined;
  }
}

export interface InsightsFilterState {
  tab: InsightsTab;
  range: InsightsRange;
  fromDate: string;
  toDate: string;
  tz: string;
  libraryId: number | undefined;
  userId: number | undefined;
}

export function detectBrowserTimezone(): string {
  try {
    const resolved = Intl.DateTimeFormat().resolvedOptions().timeZone;
    return resolved && resolved.trim().length > 0 ? resolved : "UTC";
  } catch {
    return "UTC";
  }
}

export function formatIsoLocalDate(date: Date): string {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

export function defaultCustomDates(): { fromDate: string; toDate: string } {
  const today = new Date();
  const from = new Date(today.getFullYear(), today.getMonth(), today.getDate() - 29);
  return {
    fromDate: formatIsoLocalDate(from),
    toDate: formatIsoLocalDate(today),
  };
}

function parsePositiveIntParam(raw: string | null): number | undefined {
  if (!raw) return undefined;
  const parsed = Number(raw);
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : undefined;
}

export function parseInsightsUrlState(
  searchParams: URLSearchParams,
  defaultTz: string,
): InsightsFilterState {
  const rawTab = searchParams.get("tab");
  const normalizedTab = rawTab === "activity" ? "playback" : rawTab;
  const tab: InsightsTab = INSIGHTS_TABS.includes(normalizedTab as InsightsTab)
    ? (normalizedTab as InsightsTab)
    : "playback";

  const rawRange = searchParams.get("range");
  const range: InsightsRange = INSIGHTS_RANGES.includes(rawRange as InsightsRange)
    ? (rawRange as InsightsRange)
    : "30d";

  const fallbackDates = defaultCustomDates();
  const fromDate = searchParams.get("from")?.trim() || fallbackDates.fromDate;
  const toDate = searchParams.get("to")?.trim() || fallbackDates.toDate;
  const tz = searchParams.get("tz")?.trim() || defaultTz;

  return {
    tab,
    range,
    fromDate,
    toDate,
    tz,
    libraryId: parsePositiveIntParam(searchParams.get("library_id")),
    userId: parsePositiveIntParam(searchParams.get("user_id")),
  };
}

/**
 * Query parameters for the page's shared window. `person` scopes the read to
 * one account (and optionally one of its profiles) in place of the page's
 * account filter, for the person detail panel.
 */
export function buildInsightsWindowQueryParams(
  filters: InsightsFilterState,
  options: { omitLibrary?: boolean; omitUser?: boolean; person?: InsightsPersonScope } = {},
) {
  const userId = options.person?.userId ?? (options.omitUser ? undefined : filters.userId);
  return {
    range: filters.range,
    tz: filters.tz,
    ...(filters.range === "custom" ? { from_date: filters.fromDate, to_date: filters.toDate } : {}),
    ...(!options.omitLibrary && filters.libraryId ? { library_id: String(filters.libraryId) } : {}),
    ...(userId ? { user_id: String(userId) } : {}),
    ...(options.person?.profileId ? { profile_id: options.person.profileId } : {}),
  };
}

export interface InsightsPersonScope {
  userId: number;
  profileId?: string;
}

/** The detail panel the URL opens: a title (`title`) or a person (`person`, optional `profile`). */
export interface InsightsDetailState {
  titleId: string | undefined;
  person: InsightsPersonScope | undefined;
}

export const INSIGHTS_DETAIL_PARAMS = ["title", "person", "profile"] as const;

export function parseInsightsDetailState(searchParams: URLSearchParams): InsightsDetailState {
  const titleId = searchParams.get("title")?.trim() || undefined;
  const personId = parsePositiveIntParam(searchParams.get("person"));
  const profileId = searchParams.get("profile")?.trim() || undefined;
  return {
    titleId,
    person: personId ? { userId: personId, profileId } : undefined,
  };
}

export interface HistoryDrilldownParams {
  userId?: number | string;
  profileId?: string;
  mediaItemId?: string;
  seriesId?: string;
  libraryId?: number | string;
  playMethod?: string;
  clientName?: string;
  devicePlatform?: string;
  sourceVideoCodec?: string;
  adaptationReason?: string;
  subtitleBurnIn?: boolean;
  streamLocation?: "local" | "remote";
  startedAfter?: string;
  startedBefore?: string;
}

export function buildHistoryDrilldownHref(params: HistoryDrilldownParams): string {
  const query = new URLSearchParams();
  if (params.userId) query.set("user_id", String(params.userId));
  if (params.profileId) query.set("profile_id", params.profileId);
  if (params.mediaItemId) query.set("media_item_id", params.mediaItemId);
  if (params.seriesId) query.set("series_id", params.seriesId);
  if (params.libraryId) query.set("library_id", String(params.libraryId));
  if (params.playMethod) query.set("play_method", params.playMethod);
  if (params.clientName) query.set("client_name", params.clientName);
  if (params.devicePlatform) query.set("device_platform", params.devicePlatform);
  if (params.sourceVideoCodec) query.set("source_video_codec", params.sourceVideoCodec);
  if (params.adaptationReason) query.set("adaptation_reason", params.adaptationReason);
  if (params.subtitleBurnIn !== undefined) {
    query.set("subtitle_burn_in", params.subtitleBurnIn ? "true" : "false");
  }
  if (params.streamLocation) query.set("stream_location", params.streamLocation);
  if (params.startedAfter) query.set("started_after", params.startedAfter);
  if (params.startedBefore) query.set("started_before", params.startedBefore);
  const qs = query.toString();
  return qs ? `/admin/history?${qs}` : "/admin/history";
}

/** Formats a [0, 1] ratio as a whole or single-decimal percentage string (no % sign). */
export function formatRatePercentNumber(rate: number | null | undefined): string {
  if (rate == null || !Number.isFinite(rate)) return "0";
  const pct = Math.max(0, Math.min(100, rate * 100));
  const decimals = pct > 0 && pct < 10 ? 1 : 0;
  return pct.toLocaleString(formatLocale(), {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  });
}

/** A plural count message such as "3 plays", with the number in the app locale's digits. */
export function formatInsightsCount(t: Translate, key: MessageKey, count: number): string {
  return t(key, { count, count_text: count.toLocaleString(formatLocale()) });
}

export function formatShortCalendarDate(isoOrDate: string | null | undefined): string {
  if (!isoOrDate) return "—";
  const isDateOnly = /^\d{4}-\d{2}-\d{2}$/.test(isoOrDate);
  const parsed = isDateOnly ? new Date(`${isoOrDate}T12:00:00Z`) : new Date(isoOrDate);
  if (Number.isNaN(parsed.getTime())) return isoOrDate;
  return parsed.toLocaleDateString(preferredDateLocale(), {
    month: "short",
    day: "numeric",
    year: "numeric",
    timeZone: isDateOnly ? "UTC" : undefined,
  });
}

export function formatShortTimestamp(iso: string | null | undefined): string {
  if (!iso) return "—";
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime())) return iso;
  return formatDateTime(parsed);
}

/** Enumerates every UTC calendar day YYYY-MM-DD between `firstDate` and `lastDate` inclusive. */
export function buildDailyUtcGrid(firstDate: string, lastDate: string): string[] {
  const start = Date.parse(`${firstDate}T00:00:00Z`);
  const end = Date.parse(`${lastDate}T00:00:00Z`);
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return [];
  const days: string[] = [];
  const stepMs = 86_400_000;
  const maxDays = 3665;
  for (let t = start, i = 0; t <= end && i < maxDays; t += stepMs, i++) {
    days.push(new Date(t).toISOString().slice(0, 10));
  }
  return days;
}

function isoCalendarDateInTz(iso: string, tz: string): string | null {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return null;
  try {
    const parts = new Intl.DateTimeFormat("en-CA", {
      timeZone: tz,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    }).formatToParts(date);
    const y = parts.find((p) => p.type === "year")?.value;
    const m = parts.find((p) => p.type === "month")?.value;
    const d = parts.find((p) => p.type === "day")?.value;
    return y && m && d ? `${y}-${m}-${d}` : null;
  } catch {
    return date.toISOString().slice(0, 10);
  }
}

/**
 * Enumerates every `label_date` (`YYYY-MM-DD`) bucket in `[fromIso, toIso)` in
 * `tz` at Postgres `date_trunc` granularity (`day`, ISO Monday `week`, or
 * `month`), so quiet periods render as zero-height columns instead of
 * compressing distant plays together.
 */
export function buildActivityBucketGrid(
  fromIso: string,
  toIso: string,
  tz: string,
  unit: "day" | "week" | "month",
): string[] {
  const fromDateStr = isoCalendarDateInTz(fromIso, tz);
  const endMs = Date.parse(toIso);
  const inclusiveToIso = Number.isFinite(endMs) ? new Date(endMs - 1).toISOString() : toIso;
  const toDateStr = isoCalendarDateInTz(inclusiveToIso, tz);
  if (!fromDateStr || !toDateStr) return [];

  const start = new Date(`${fromDateStr}T00:00:00Z`);
  const end = new Date(`${toDateStr}T00:00:00Z`);
  if (Number.isNaN(start.getTime()) || Number.isNaN(end.getTime()) || end < start) return [];

  if (unit === "week") {
    // Postgres date_trunc('week', ...) starts on Monday.
    const dow = (start.getUTCDay() + 6) % 7;
    start.setUTCDate(start.getUTCDate() - dow);
  } else if (unit === "month") {
    start.setUTCDate(1);
  }

  const out: string[] = [];
  const cursor = new Date(start.getTime());
  for (let i = 0; i < 4000 && cursor <= end; i++) {
    out.push(cursor.toISOString().slice(0, 10));
    if (unit === "day") {
      cursor.setUTCDate(cursor.getUTCDate() + 1);
    } else if (unit === "week") {
      cursor.setUTCDate(cursor.getUTCDate() + 7);
    } else {
      cursor.setUTCMonth(cursor.getUTCMonth() + 1, 1);
    }
  }
  return out;
}

/**
 * "Oct 26, 2026 – Nov 24, 2026" for a response window `[fromIso, toIso)`, as
 * calendar dates in the window's own time zone, not the browser's.
 */
export function formatInsightsWindowDates(
  fromIso: string,
  toIso: string,
  tz: string,
): { from: string; to: string } {
  const endMs = Date.parse(toIso);
  const lastIso = Number.isFinite(endMs) ? new Date(endMs - 1).toISOString() : toIso;
  return {
    from: formatShortCalendarDate(isoCalendarDateInTz(fromIso, tz) ?? fromIso),
    to: formatShortCalendarDate(isoCalendarDateInTz(lastIso, tz) ?? lastIso),
  };
}

/**
 * The Insights page URL with some parameters set or (null) removed, keeping the
 * rest: tab, window, filters, and any open detail panel.
 */
export function insightsHrefWith(
  current: URLSearchParams,
  patch: Record<string, string | null>,
): string {
  const next = new URLSearchParams(current);
  for (const [key, value] of Object.entries(patch)) {
    if (value === null || value === "") {
      next.delete(key);
    } else {
      next.set(key, value);
    }
  }
  const qs = next.toString();
  return qs ? `/admin/insights?${qs}` : "/admin/insights";
}

/** Formats a UTC calendar timestamp (`YYYY-MM-DD` midnight UTC) cleanly without browser-TZ shift. */
export function formatActivityBucketLabel(
  utcCalendarMs: number,
  unit: "day" | "week" | "month",
): string {
  const d = new Date(utcCalendarMs);
  if (Number.isNaN(d.getTime())) return "—";
  if (unit === "month") {
    return d.toLocaleDateString(preferredDateLocale(), {
      month: "short",
      year: "numeric",
      timeZone: "UTC",
    });
  }
  return d.toLocaleDateString(preferredDateLocale(), {
    month: "short",
    day: "numeric",
    timeZone: "UTC",
  });
}

export function formatHwAccelLabel(
  key: string,
  softwareLabel: string,
  unknownLabel: string,
): string {
  switch (key.toLowerCase()) {
    case "software":
    case "none":
      return softwareLabel;
    case "unknown":
    case "":
      return unknownLabel;
    case "vaapi":
      return "VAAPI";
    case "qsv":
      return "QSV";
    case "nvenc":
      return "NVENC";
    case "videotoolbox":
      return "VideoToolbox";
    default:
      return key.toUpperCase();
  }
}
