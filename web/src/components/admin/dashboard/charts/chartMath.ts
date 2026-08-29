/**
 * Pure geometry/scale helpers shared by the dashboard chart primitives.
 *
 * Nothing here touches the DOM or React so the arithmetic stays unit-testable:
 * the components own layout and interaction, this file owns the numbers.
 */

/** Number of categorical series slots the theme defines (`--chart-1` … `--chart-5`). */
export const CHART_SERIES_SLOTS = 5;

/**
 * Theme color for a categorical series slot.
 *
 * Colors come only from the theme's chart tokens and are assigned in fixed
 * entity order (direct → 1, remux → 2, transcode → 3), never cycled: a chart
 * that needs more than five series folds the tail into an "other" bucket
 * instead of inventing hues, so out-of-range indexes clamp to the last slot.
 */
export function chartSeriesColor(index: number): string {
  const slot = Number.isFinite(index)
    ? Math.min(Math.max(Math.trunc(index), 0), CHART_SERIES_SLOTS - 1)
    : 0;
  return `var(--chart-${slot + 1})`;
}

function round(value: number, decimals = 2): number {
  const factor = 10 ** decimals;
  return Math.round(value * factor) / factor;
}

function niceNumber(range: number, roundToNearest: boolean): number {
  const exponent = Math.floor(Math.log10(range));
  const fraction = range / 10 ** exponent;
  let nice: number;
  if (roundToNearest) {
    nice = fraction < 1.5 ? 1 : fraction < 3 ? 2 : fraction < 7 ? 5 : 10;
  } else {
    nice = fraction <= 1 ? 1 : fraction <= 2 ? 2 : fraction <= 5 ? 5 : 10;
  }
  return nice * 10 ** exponent;
}

export interface NiceTicksOptions {
  /**
   * Smallest allowed gap between ticks. Count-based charts pass 1 so a nearly
   * flat series gets `0, 1` instead of fractional "half a stream" gridlines.
   */
  minStep?: number;
}

/**
 * Axis ticks rounded to human numbers, ascending and always covering
 * `[min, max]`. `count` is the desired tick count, not a guarantee.
 */
export function niceTicks(
  min: number,
  max: number,
  count = 4,
  options?: NiceTicksOptions,
): number[] {
  let lo = Number.isFinite(min) ? min : 0;
  let hi = Number.isFinite(max) ? max : 0;
  if (lo > hi) {
    [lo, hi] = [hi, lo];
  }
  if (lo === hi) {
    // A flat series still needs a readable axis; grow the top rather than
    // collapsing every value onto one gridline.
    hi = lo + (lo === 0 ? 1 : Math.abs(lo));
  }
  const target = Math.max(2, Math.trunc(count));
  const rawStep = niceNumber(niceNumber(hi - lo, false) / (target - 1), true);
  const minStep = options?.minStep;
  const step =
    minStep && Number.isFinite(minStep) && minStep > 0 ? Math.max(rawStep, minStep) : rawStep;
  const decimals = Math.max(0, Math.min(10, -Math.floor(Math.log10(step))));
  const start = Math.floor(lo / step) * step;
  const end = Math.ceil(hi / step) * step;
  const ticks: number[] = [];
  // Guard against pathological steps producing an unbounded loop.
  const maxTicks = 1000;
  for (let i = 0; i <= maxTicks; i += 1) {
    const value = round(start + i * step, decimals);
    ticks.push(value);
    if (value >= end) {
      break;
    }
  }
  return ticks;
}

export interface LinePathPoint {
  x: number;
  /** `null` marks a missing sample: the path breaks instead of interpolating. */
  y: number | null;
}

function isPlotted(point: LinePathPoint): point is { x: number; y: number } {
  return point.y !== null && Number.isFinite(point.y) && Number.isFinite(point.x);
}

function contiguousRuns(points: readonly LinePathPoint[]): { x: number; y: number }[][] {
  const runs: { x: number; y: number }[][] = [];
  let current: { x: number; y: number }[] = [];
  for (const point of points) {
    if (isPlotted(point)) {
      current.push({ x: point.x, y: point.y });
      continue;
    }
    if (current.length > 0) {
      runs.push(current);
      current = [];
    }
  }
  if (current.length > 0) {
    runs.push(current);
  }
  return runs;
}

/**
 * `d` for a polyline through already-projected pixel coordinates. Gaps (`null`
 * y) break the path into separate subpaths; an isolated sample becomes a
 * zero-length subpath so a round line cap still renders it as a dot.
 */
export function buildLinePath(points: readonly LinePathPoint[]): string {
  return contiguousRuns(points)
    .map((run) => {
      const head = run[0];
      if (!head) {
        return "";
      }
      const segments = run
        .slice(1)
        .map((point) => `L ${round(point.x)} ${round(point.y)}`)
        .join(" ");
      const start = `M ${round(head.x)} ${round(head.y)}`;
      return segments ? `${start} ${segments}` : `${start} L ${round(head.x)} ${round(head.y)}`;
    })
    .filter((segment) => segment !== "")
    .join(" ");
}

/**
 * `d` for the area wash under the same points, closed to `baselineY`. Each
 * contiguous run is its own closed subpath so gaps stay empty.
 */
export function buildAreaPath(points: readonly LinePathPoint[], baselineY: number): string {
  return contiguousRuns(points)
    .map((run) => {
      const head = run[0];
      const tail = run[run.length - 1];
      if (!head || !tail) {
        return "";
      }
      const body = run.map((point) => `L ${round(point.x)} ${round(point.y)}`).join(" ");
      return `M ${round(head.x)} ${round(baselineY)} ${body} L ${round(tail.x)} ${round(baselineY)} Z`;
    })
    .filter((segment) => segment !== "")
    .join(" ");
}

export interface StackSegment {
  /** Series index the segment belongs to. */
  index: number;
  value: number;
  /** Cumulative offset where the segment starts (0 = baseline). */
  start: number;
  /** Cumulative offset where the segment ends. */
  end: number;
}

/**
 * Cumulative offsets for one stacked column. Missing/negative values are
 * treated as 0 so a bad sample can never push a segment below the baseline.
 * Zero-valued segments are kept (with `start === end`) so callers can rely on
 * segment index matching series index.
 */
export function stackSegments(values: readonly number[]): StackSegment[] {
  let offset = 0;
  return values.map((raw, index) => {
    const value = Number.isFinite(raw) && raw > 0 ? raw : 0;
    const start = offset;
    offset += value;
    return { index, value, start, end: offset };
  });
}

export interface TimeSample<T> {
  /** Bucket timestamp in epoch milliseconds. */
  t: number;
  value: T;
}

export interface TimeBucket<T> {
  /** Bucket start in epoch milliseconds. */
  t: number;
  value: T;
  /** False when the bucket was zero-filled because no sample covered it. */
  present: boolean;
}

const MAX_TIME_BUCKETS = 10_000;

/**
 * Zero-filled bucket series from `from` to `to` inclusive, stepping by
 * `stepMs`. Sparse API responses (which omit empty buckets entirely) become a
 * dense series, and `present` marks which buckets were real samples so line
 * charts can render gaps instead of fake zeros.
 *
 * `from` is used verbatim as the first bucket start — callers pass an already
 * truncated timestamp. Samples outside the window are ignored; when two
 * samples land in the same bucket the later one in iteration order wins.
 */
export function timeBuckets<T>(
  from: number,
  to: number,
  stepMs: number,
  samples: readonly TimeSample<T>[],
  empty: T,
): TimeBucket<T>[] {
  if (!Number.isFinite(from) || !Number.isFinite(to) || !Number.isFinite(stepMs) || stepMs <= 0) {
    return [];
  }
  if (to < from) {
    return [];
  }
  const count = Math.min(Math.floor((to - from) / stepMs) + 1, MAX_TIME_BUCKETS);
  const buckets: TimeBucket<T>[] = [];
  for (let i = 0; i < count; i += 1) {
    buckets.push({ t: from + i * stepMs, value: empty, present: false });
  }
  for (const sample of samples) {
    if (!Number.isFinite(sample.t) || sample.t < from) {
      continue;
    }
    const index = Math.floor((sample.t - from) / stepMs);
    const bucket = buckets[index];
    if (!bucket) {
      continue;
    }
    buckets[index] = { t: bucket.t, value: sample.value, present: true };
  }
  return buckets;
}
