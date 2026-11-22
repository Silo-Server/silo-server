import { formatLocale } from "@/lib/appLocale";
import { formatDateTime, preferredDateLocale } from "@/lib/datetime";

export const INSIGHTS_TABS = ["activity", "people", "devices", "bandwidth"] as const;
export type InsightsTab = (typeof INSIGHTS_TABS)[number];

export const INSIGHTS_RANGES = ["7d", "30d", "90d", "1y", "all", "custom"] as const;
export type InsightsRange = (typeof INSIGHTS_RANGES)[number];

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
  const tab: InsightsTab = INSIGHTS_TABS.includes(rawTab as InsightsTab)
    ? (rawTab as InsightsTab)
    : "activity";

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

export function buildInsightsWindowQueryParams(filters: InsightsFilterState) {
  return {
    range: filters.range,
    tz: filters.tz,
    ...(filters.range === "custom" ? { from_date: filters.fromDate, to_date: filters.toDate } : {}),
    ...(filters.libraryId ? { library_id: String(filters.libraryId) } : {}),
    ...(filters.userId ? { user_id: String(filters.userId) } : {}),
  };
}

export interface HistoryDrilldownParams {
  userId?: number | string;
  profileId?: string;
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
