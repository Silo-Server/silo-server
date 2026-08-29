import type { AdminTimeseries, AdminTimeseriesPoint } from "@/api/types";
import { timeBuckets } from "../charts";
import type { LineChartPoint } from "../charts";

/**
 * Shaping for the widgets that plot `GET /admin/stats/timeseries` (concurrent
 * streams, egress). Both read the same response and differ only in which field
 * they select and how they format it, so the window arithmetic lives here.
 */

const DEFAULT_RESOLUTION_SECONDS = 60;

/**
 * Dense minute series for the response window.
 *
 * The endpoint omits minutes the sampler never wrote — a restart, a process
 * that was down — so the window is re-expanded here and missing minutes become
 * `null`. That distinction is the whole point: a gap breaks the line, while a
 * sampled minute with no streams draws a real zero.
 */
export function buildTimeseriesPoints(
  series: AdminTimeseries | undefined,
  select: (point: AdminTimeseriesPoint) => number,
): LineChartPoint[] {
  if (!series) {
    return [];
  }
  const resolution =
    series.resolution_seconds > 0 ? series.resolution_seconds : DEFAULT_RESOLUTION_SECONDS;
  const stepMs = resolution * 1_000;
  const samples = series.points.flatMap((point) => {
    const t = Date.parse(point.t);
    if (!Number.isFinite(t)) {
      return [];
    }
    const value = select(point);
    return [{ t, value: Number.isFinite(value) ? value : null }];
  });

  const from = Date.parse(series.from);
  const to = Date.parse(series.to);
  if (!Number.isFinite(from) || !Number.isFinite(to) || to < from) {
    // Malformed window: plot whatever samples arrived rather than nothing.
    return samples.map((sample) => ({ t: sample.t, value: sample.value }));
  }

  const start = Math.floor(from / stepMs) * stepMs;
  const end = Math.floor(to / stepMs) * stepMs;
  return timeBuckets<number | null>(start, end, stepMs, samples, null).map((bucket) => ({
    t: bucket.t,
    value: bucket.present ? bucket.value : null,
  }));
}

/**
 * The most recent sample, but only while it is fresh enough to still describe
 * "now" — a stalled sampler must read as unknown, never as the last value it
 * happened to write.
 */
export function latestFreshPoint(
  series: AdminTimeseries | undefined,
  maxAgeMs: number,
  now: number = Date.now(),
): AdminTimeseriesPoint | null {
  const point = series?.points[series.points.length - 1];
  if (!point) {
    return null;
  }
  const t = Date.parse(point.t);
  if (!Number.isFinite(t) || now - t > maxAgeMs) {
    return null;
  }
  return point;
}
