import { useMemo } from "react";

import { useAppLanguage } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

/**
 * Helpers for the Activity read's weekday × hour heatmap and the charts built
 * from it. Cells use the server's numbering: `day_of_week` 0 is Monday, 6 is
 * Sunday; `hour` is 0–23 in the requested timezone.
 */

export interface ViewingTimeCell {
  day_of_week: number;
  hour: number;
  plays: number;
  watch_seconds: number;
}

export type ViewingTimeMetric = "plays" | "watch_time";

// Noon on a known Monday through Sunday, formatted in UTC, gives locale weekday
// names in the server's Monday-first order without a calendar library.
const WEEKDAY_ANCHORS_UTC = [
  "2024-01-01T12:00:00Z",
  "2024-01-02T12:00:00Z",
  "2024-01-03T12:00:00Z",
  "2024-01-04T12:00:00Z",
  "2024-01-05T12:00:00Z",
  "2024-01-06T12:00:00Z",
  "2024-01-07T12:00:00Z",
] as const;

function cellValue(cell: ViewingTimeCell, metric: ViewingTimeMetric): number {
  return metric === "watch_time" ? cell.watch_seconds : cell.plays;
}

/** Totals per weekday, Monday first. */
export function weekdayTotals(
  cells: readonly ViewingTimeCell[],
  metric: ViewingTimeMetric,
): number[] {
  const totals = Array.from({ length: 7 }, () => 0);
  for (const cell of cells) {
    if (cell.day_of_week >= 0 && cell.day_of_week < 7) {
      totals[cell.day_of_week] = (totals[cell.day_of_week] ?? 0) + cellValue(cell, metric);
    }
  }
  return totals;
}

/** Totals per hour of the day, 0 to 23. */
export function hourTotals(cells: readonly ViewingTimeCell[], metric: ViewingTimeMetric): number[] {
  const totals = Array.from({ length: 24 }, () => 0);
  for (const cell of cells) {
    if (cell.hour >= 0 && cell.hour < 24) {
      totals[cell.hour] = (totals[cell.hour] ?? 0) + cellValue(cell, metric);
    }
  }
  return totals;
}

/** Short weekday names, Monday first, in the interface language. */
export function useWeekdayLabels(): string[] {
  const language = useAppLanguage();
  return useMemo(() => {
    const fmt = new Intl.DateTimeFormat(formatLocale(), { weekday: "short", timeZone: "UTC" });
    return WEEKDAY_ANCHORS_UTC.map((iso) => fmt.format(new Date(iso)));
    // eslint-disable-next-line react-hooks/exhaustive-deps -- language re-runs localized weekday names
  }, [language]);
}

/** Hour labels 0–23 in the interface language's clock ("9 AM", "21"). */
export function useHourLabels(): string[] {
  const language = useAppLanguage();
  return useMemo(() => {
    const fmt = new Intl.DateTimeFormat(formatLocale(), { hour: "numeric", timeZone: "UTC" });
    return Array.from({ length: 24 }, (_, hour) =>
      fmt.format(new Date(Date.UTC(2024, 0, 1, hour, 0, 0))),
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps -- language re-runs localized hour labels
  }, [language]);
}
