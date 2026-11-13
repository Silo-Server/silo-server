import { t, type MessageKey } from "@/i18n";
import { formatDate, formatTime, preferredDateLocale } from "@/lib/datetime";
import type { WidgetRange } from "./types";

/**
 * How a `WidgetRange` turns into request parameters and into words.
 *
 * Every widget that offers a range reads its window from here, so the picker,
 * the card title, the chart's accessible name, and the empty state can never
 * disagree about what "week" means.
 */

/** Widest-to-narrowest order the picker renders in. */
export const WIDGET_RANGE_ORDER: readonly WidgetRange[] = ["hour", "day", "week", "month"];

/**
 * Hours for the timeseries and playback-activity endpoints. A month is 720
 * hours (30 days), so the charted window matches the "30d" label and the
 * 30-day window `RANGE_DAYS` asks the top-activity endpoint for. The sampler
 * retains 31 days — deliberately one day wider than anything charted here, so
 * the oldest bucket in view is never one the retention job is about to drop.
 */
const RANGE_HOURS: Record<WidgetRange, number> = {
  hour: 1,
  day: 24,
  week: 168,
  month: 720,
};

/**
 * Days for the top-activity endpoint. "hour" collapses onto a single day: the
 * leaderboards do not offer it, and a sub-day window would return almost
 * nothing worth ranking.
 */
const RANGE_DAYS: Record<WidgetRange, number> = {
  hour: 1,
  day: 1,
  week: 7,
  month: 30,
};

/** Compact label for the segmented control. */
const RANGE_LABELS: Record<WidgetRange, MessageKey> = {
  hour: "web.admin.dashboard.range.label.hour",
  day: "web.admin.dashboard.range.label.day",
  week: "web.admin.dashboard.range.label.week",
  month: "web.admin.dashboard.range.label.month",
};

/** Spoken form, for aria-labels and prose ("No plays in the last 7 days"). */
const RANGE_PHRASES: Record<WidgetRange, MessageKey> = {
  hour: "web.admin.dashboard.range.phrase.hour",
  day: "web.admin.dashboard.range.phrase.day",
  week: "web.admin.dashboard.range.phrase.week",
  month: "web.admin.dashboard.range.phrase.month",
};

/** Card title with its window, e.g. "Egress · last 30 d". */
const RANGE_TITLES: Record<WidgetRange, MessageKey> = {
  hour: "web.admin.dashboard.range.title.hour",
  day: "web.admin.dashboard.range.title.day",
  week: "web.admin.dashboard.range.title.week",
  month: "web.admin.dashboard.range.title.month",
};

/** Leading edge label on a time axis; the trailing one is always "now". */
const RANGE_EDGE_LABELS: Record<WidgetRange, MessageKey> = {
  hour: "web.admin.dashboard.range.edgeStart.hour",
  day: "web.admin.dashboard.range.edgeStart.day",
  week: "web.admin.dashboard.range.edgeStart.week",
  month: "web.admin.dashboard.range.edgeStart.month",
};

export function rangeHours(range: WidgetRange): number {
  return RANGE_HOURS[range];
}

export function rangeDays(range: WidgetRange): number {
  return RANGE_DAYS[range];
}

export function rangeLabel(range: WidgetRange): string {
  return t(RANGE_LABELS[range]);
}

export function rangePhrase(range: WidgetRange): string {
  return t(RANGE_PHRASES[range]);
}

/** "Playback activity · last 24 h" — one format for every ranged widget. */
export function rangeTitle(title: string, range: WidgetRange): string {
  return t(RANGE_TITLES[range], { title });
}

/** Axis edges: how far back the window starts, and where it ends. */
export function rangeEdgeLabels(range: WidgetRange): { start: string; end: string } {
  return { start: t(RANGE_EDGE_LABELS[range]), end: t("web.admin.dashboard.range.edgeEnd") };
}

/**
 * Timestamp for a hover readout or a column tick.
 *
 * Short windows are read as clock times; from a week up the clock stops being
 * the distinguishing part, so the day is shown instead (with the time as well
 * in a tooltip, where there is room for it).
 */
export function formatRangeTimestamp(
  range: WidgetRange,
  time: number,
  options?: { withTime?: boolean },
): string {
  if (range === "hour" || range === "day") {
    return formatTime(time);
  }
  const day = formatDayLabel(time);
  return options?.withTime
    ? t("web.admin.dashboard.range.dayWithTime", { day, time: formatTime(time) })
    : day;
}

/** "Aug 26" — short enough for an axis tick, unambiguous across a month. */
export function formatDayLabel(time: number): string {
  const date = new Date(time);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  const locale = preferredDateLocale();
  try {
    return date.toLocaleDateString(locale, { month: "short", day: "numeric" });
  } catch {
    // An unsupported locale tag must not blank out the axis.
    return formatDate(date, "medium");
  }
}
