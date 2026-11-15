import type { AdminTimeseries, AdminTimeseriesPoint } from "@/api/types";
import { chartSeriesColor } from "../charts";
import type { LineChartOverlay, LineChartStack } from "../charts";
import { t, type MessageKey } from "@/i18n";
import { buildTimeseriesPoints } from "./timeseriesSeries";

type Method = "direct" | "remux" | "transcode";

/**
 * Methods in entity order, with the same labels and color slots as the
 * Playback activity widget, so a method keeps its color across the dashboard.
 */
const METHODS: readonly { method: Method; labelKey: MessageKey; slot: number }[] = [
  {
    method: "direct",
    labelKey: "web.admin.dashboard.widgets.playbackActivity.series.directPlay",
    slot: 0,
  },
  {
    method: "remux",
    labelKey: "web.admin.dashboard.widgets.playbackActivity.series.directStream",
    slot: 1,
  },
  {
    method: "transcode",
    labelKey: "web.admin.dashboard.widgets.playbackActivity.series.transcode",
    slot: 2,
  },
];

/** Neutral color for series that are not a method: the total and "Other". */
export const NEUTRAL_SERIES_COLOR = "var(--muted-foreground)";

/**
 * One line per method, each the method's own peak in the bucket: "how many
 * transcodes at worst" is the question a line answers, so the lines are drawn
 * from the per-column peaks even though they need not add up to the total.
 */
export function buildMethodLines(series: AdminTimeseries | undefined): LineChartOverlay[] {
  return METHODS.map(({ method, labelKey, slot }) => ({
    label: t(labelKey),
    seriesIndex: slot,
    points: buildTimeseriesPoints(series, (point) => point[method]),
  }));
}

/**
 * Stacked bands, direct play at the baseline, read from `streams_by_method`
 * (the bucket's peak-streams minute) so the stack's top edge is the total. The
 * per-column peaks cannot be stacked: past the minute resolution they come
 * from different minutes and overshoot the total.
 *
 * Streams no listed method accounts for get an "Other" band on top, present
 * only when some bucket has any, so the stack always meets the total line.
 */
export function buildMethodBands(series: AdminTimeseries): LineChartStack[] {
  const split = (point: AdminTimeseriesPoint, method: Method) =>
    point.streams_by_method?.[method] ?? 0;
  const bands: LineChartStack[] = METHODS.map(({ method, labelKey, slot }) => ({
    key: method,
    label: t(labelKey),
    color: chartSeriesColor(slot),
    points: buildTimeseriesPoints(series, (point) => split(point, method)),
  }));
  const other = buildTimeseriesPoints(series, (point) =>
    Math.max(point.streams - METHODS.reduce((sum, { method }) => sum + split(point, method), 0), 0),
  );
  if (other.some((point) => (point.value ?? 0) > 0)) {
    bands.push({
      key: "other",
      label: t("web.admin.dashboard.widgets.concurrentStreams.otherMethod"),
      color: NEUTRAL_SERIES_COLOR,
      points: other,
    });
  }
  return bands;
}

/**
 * Whether the response carries the split the Stacked view needs. A server
 * that predates `streams_by_method` (an older replica answering mid-upgrade)
 * omits it, and stacking without it would file every stream under "Other".
 */
export function hasMethodSplit(series: AdminTimeseries): boolean {
  return series.points.every((point) => point.streams_by_method !== undefined);
}
