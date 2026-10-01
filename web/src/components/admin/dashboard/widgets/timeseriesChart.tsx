import type { UseQueryResult } from "@tanstack/react-query";

import type { AdminTimeseries } from "@/api/types";
import { ChartEmptyState, ChartSkeleton, LineChart } from "../charts";
import type { LineChartOverlay, LineChartPoint } from "../charts";
import { SectionError } from "../feedback";

export interface TimeseriesChartBodyProps {
  query: UseQueryResult<AdminTimeseries>;
  points: readonly LineChartPoint[];
  seriesLabel: string;
  ariaLabel: string;
  errorMessage: string;
  emptyMessage?: string;
  formatValue?: (value: number) => string;
  formatTick?: (value: number) => string;
  formatTimestamp?: (t: number) => string;
  /** How far back the window reaches, and where it ends; see LineChart. */
  edgeLabels?: { start: string; end: string };
  minTickStep?: number;
  /** Secondary series drawn over the primary one; see LineChart. */
  overlays?: readonly LineChartOverlay[];
  height?: number;
  /**
   * Fill the card's content area instead of a fixed `height`. Widget rows are
   * resizable, so the plot follows the card rather than the other way round.
   */
  fill?: boolean;
}

/**
 * Loading / error / collecting / plotted states for a sampled line chart,
 * shared by the concurrent-streams and egress widgets.
 *
 * Never renders nothing: a widget with no data still says why, because the
 * sampler only has history for the time the server was actually up.
 */
export function TimeseriesChartBody({
  query,
  points,
  seriesLabel,
  ariaLabel,
  errorMessage,
  emptyMessage = "No samples yet",
  formatValue,
  formatTick,
  formatTimestamp,
  edgeLabels,
  minTickStep,
  overlays,
  height = 160,
  fill = false,
}: TimeseriesChartBodyProps) {
  if (query.isLoading) {
    return <ChartSkeleton height={height} fill={fill} />;
  }
  if (query.error) {
    return <SectionError message={errorMessage} />;
  }

  const hasSample = points.some((point) => point.value !== null);
  if (!hasSample) {
    return (
      <ChartEmptyState
        height={height}
        fill={fill}
        message={emptyMessage}
        since={query.data?.oldest_sample_at ?? null}
        detail="The metrics sampler collects one sample a minute while the server runs."
      />
    );
  }

  return (
    <LineChart
      points={points}
      height={height}
      fill={fill}
      seriesLabel={seriesLabel}
      ariaLabel={ariaLabel}
      formatValue={formatValue}
      formatTick={formatTick}
      formatTimestamp={formatTimestamp}
      edgeLabels={edgeLabels}
      minTickStep={minTickStep}
      overlays={overlays}
    />
  );
}
