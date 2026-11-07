import { t, type MessageKey } from "@/i18n";
import { formatRelativeTime } from "@/lib/date";
import { formatDate, formatTime, preferredDateLocale } from "@/lib/datetime";
import { activityMethodMeta, formatDecisionLabel } from "@/pages/adminActivityPresentation";
import { formatDecimal } from "@/lib/numberFormat";

function validDate(iso: string | null | undefined): Date | null {
  if (!iso) return null;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? null : date;
}

/** Watch time as hours and minutes: 66000 → "18h 20m", 2700 → "45m", 0 → "0m". */
export function formatWatchTime(seconds: number): string {
  const total = Number.isFinite(seconds) ? Math.max(0, Math.floor(seconds)) : 0;
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  return hours > 0
    ? t("web.admin.users.detail.common.watchTime.hoursMinutes", {
        hours: hours,
        minutes: minutes,
      })
    : t("web.admin.users.detail.common.watchTime.minutes", { minutes: minutes });
}

/** A month and day, "Sep 26"; "" when there is no date. */
export function formatShortDate(iso: string | null | undefined): string {
  const date = validDate(iso);
  if (!date) return "";
  return date.toLocaleDateString(preferredDateLocale(), { month: "short", day: "numeric" });
}

function sameDay(a: Date, b: Date) {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  );
}

/** A day and clock time: "Today, 09:02", "Yesterday, 21:14", or "Sep 26, 18:02". */
export function formatDayTime(iso: string): string {
  const date = validDate(iso);
  if (!date) return "";
  const now = new Date();
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  const time = formatTime(date);
  if (sameDay(date, now)) return t("web.admin.users.detail.common.dayTime.today", { time });
  if (sameDay(date, yesterday)) {
    return t("web.admin.users.detail.common.dayTime.yesterday", { time });
  }
  return t("web.admin.users.detail.common.dayTime.date", { date: formatShortDate(iso), time });
}

/**
 * How long ago something was last seen ("2h ago"), or its date after a month
 * ("Aug 30, 2026", like the page's other dates); "—" when it never was.
 */
export function formatLastSeen(iso: string | null | undefined): string {
  return (
    formatRelativeTime(iso, {
      absoluteAfterDays: 30,
      absolute: (date) => formatDate(date, "medium"),
    }) ?? "—"
  );
}

/** How much of a play was watched, 0..1. A completed play counts as all of it. */
export function watchedFraction(
  watched: number,
  duration: number | null,
  completed: boolean,
): number {
  if (completed) return 1;
  if (duration === null || !(duration > 0) || !Number.isFinite(watched)) return 0;
  return Math.min(1, Math.max(0, watched / duration));
}

/** "S02E04" for an episode; "" when either number is unknown. */
export function episodeCode(
  season: number | null | undefined,
  episode: number | null | undefined,
): string {
  if (season == null || episode == null) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return t("web.admin.users.detail.common.episodeCode", {
    season: pad(season),
    episode: pad(episode),
  });
}

const BYTE_UNITS: readonly { unit: MessageKey; size: number; digits: number }[] = [
  { unit: "web.admin.users.detail.common.bytes.b", size: 1, digits: 0 },
  { unit: "web.admin.users.detail.common.bytes.kb", size: 1e3, digits: 0 },
  { unit: "web.admin.users.detail.common.bytes.mb", size: 1e6, digits: 0 },
  { unit: "web.admin.users.detail.common.bytes.gb", size: 1e9, digits: 1 },
  { unit: "web.admin.users.detail.common.bytes.tb", size: 1e12, digits: 1 },
];

/** A size in decimal units: "14.2 GB", "900 MB", "512 B". */
export function formatBytes(bytes: number): string {
  const value = Number.isFinite(bytes) ? Math.max(0, bytes) : 0;
  let index = 0;
  while (index < BYTE_UNITS.length - 1 && value >= BYTE_UNITS[index + 1]!.size) index++;
  let { unit, size, digits } = BYTE_UNITS[index]!;
  // Rounding can reach the next unit: 999.96 MB reads "1.0 GB", not "1000 MB".
  if (Number((value / size).toFixed(digits)) >= 1000 && index < BYTE_UNITS.length - 1) {
    ({ unit, size, digits } = BYTE_UNITS[index + 1]!);
  }
  return t(unit, { value: formatDecimal(value / size, digits) });
}

/**
 * A storage cap in the binary gigabytes the apps use when the user picks one
 * (1 GB = 1024³ bytes): 10737418240 → "10 GB", so the cap reads back as the
 * number that was chosen. Below 1 GB it reads in MB the same way.
 */
export function formatStorageLimit(bytes: number): string {
  const value = Number.isFinite(bytes) ? Math.max(0, bytes) : 0;
  const gb = value / 1024 ** 3;
  if (gb >= 1) {
    return t("web.admin.users.detail.common.bytes.gb", { value: formatDecimal(gb, 1, 0) });
  }
  return t("web.admin.users.detail.common.bytes.mb", {
    value: Math.round(value / 1024 ** 2),
  });
}

// The values adminActivityPresentation labels; anything else reads as itself.
// Checked by value, not by comparing a label, so a translated "Unknown" still works.
const ACTIVITY_METHODS: ReadonlySet<string> = new Set([
  "direct",
  "remux",
  "direct_stream",
  "transcode",
]);
const DECISIONS: ReadonlySet<string> = new Set(["direct", "copy", "remux", "hls", "transcode"]);

/**
 * "Direct Play", "Direct Stream", "Transcode"… for a play method or activity
 * bucket. A value the app doesn't know reads as itself ("direct_play" →
 * "Direct Play") rather than as "Unknown"; only a missing one is "Unknown".
 */
export function formatPlayMethod(method: string | null | undefined): string {
  const raw = (method ?? "").trim();
  if (ACTIVITY_METHODS.has(raw)) return activityMethodMeta(raw).label;
  if (DECISIONS.has(raw) || raw === "") return formatDecisionLabel(raw);
  return raw
    .split(/[\s_-]+/)
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1).toLowerCase())
    .join(" ");
}

/** The badge tint for a play method, matching the Activity page's method tags. */
export function playMethodBadgeClass(method: string | null | undefined): string {
  return activityMethodMeta((method ?? "").trim()).badgeClass;
}

/** A calendar date with the month named, "Sep 28, 2026", in the viewer's preferred order. */
export function formatAccountDate(iso: string): string {
  return formatDate(iso, "medium") || iso;
}
