/**
 * Chart primitives for the admin dashboard widgets.
 *
 * Hand-rolled on purpose — the web app carries no chart library, and these
 * widgets need only a handful of quiet marks. House rules for everything in
 * this folder:
 *
 * - Series colors come only from the theme's `--chart-1` … `--chart-5` tokens,
 *   assigned in fixed entity order (direct → 1, remux → 2, transcode → 3) and
 *   never cycled or repainted when a filter changes the series count.
 * - One value axis per chart; text (labels, values, legends, ticks) wears text
 *   tokens, never the series color.
 * - A legend is present for two or more series; a single-series chart is named
 *   by its card header instead.
 * - Marks are thin, separated by 2px gaps in the card surface rather than by
 *   outlines, and every plotted chart ships a hover/focus readout.
 */

export { BarList, type BarListItem, type BarListProps } from "./BarList";
export { BarListSkeleton, ChartEmptyState, ChartSkeleton } from "./ChartEmptyState";
export { ChartLegend, ChartTooltip, ChartTooltipRow, type ChartLegendEntry } from "./chartChrome";
export {
  CHART_SERIES_SLOTS,
  buildAreaPath,
  buildLinePath,
  chartSeriesColor,
  niceTicks,
  stackSegments,
  timeBuckets,
  type LinePathPoint,
  type NiceTicksOptions,
  type StackSegment,
  type TimeBucket,
  type TimeSample,
} from "./chartMath";
export {
  LineChart,
  type LineChartOverlay,
  type LineChartPoint,
  type LineChartProps,
} from "./LineChart";
export { Sparkline, type SparklineProps } from "./Sparkline";
export { useMeasuredSize, type MeasuredSize } from "./useMeasuredSize";
export {
  StackedColumnChart,
  type StackedColumnBucket,
  type StackedColumnChartProps,
} from "./StackedColumnChart";
