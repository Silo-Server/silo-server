import { useMemo, useState } from "react";

import { formatTime } from "@/lib/datetime";
import { cn } from "@/lib/utils";
import { chartSeriesColor, stackSegments } from "./chartMath";
import { ChartLegend, ChartTooltip, ChartTooltipRow } from "./chartChrome";

export interface StackedColumnBucket {
  /** Bucket start in epoch milliseconds. */
  t: number;
  /** One value per series, in the same order as `seriesLabels`. */
  segments: readonly number[];
}

export interface StackedColumnChartProps {
  buckets: readonly StackedColumnBucket[];
  /** Series names, in fixed entity order — index 0 sits at the baseline. */
  seriesLabels: readonly string[];
  /** Plot height in pixels (legend and tick labels sit outside it); ignored when `fill`. */
  height?: number;
  /**
   * Take the height of the parent instead of `height`, so the columns follow
   * the widget's row height. The marks are laid out in CSS rather than in a
   * viewBox, so a taller box simply gives every column more room.
   */
  fill?: boolean;
  formatValue?: (value: number) => string;
  formatBucket?: (t: number) => string;
  /** Label for the tooltip's total row; omit to hide the row. */
  totalLabel?: string;
  ariaLabel: string;
  className?: string;
}

/** Marks stay thin: the leftover band width is deliberate air, not padding. */
const MAX_COLUMN_WIDTH = 24;
/** Enough height that a single play is still visible above the baseline. */
const MIN_SEGMENT_PX = 2;
const TARGET_TICK_LABELS = 4;

function defaultFormatValue(value: number): string {
  return value.toLocaleString();
}

/**
 * Baseline-anchored stacked columns for bucketed counts (playback by method).
 *
 * Segments and neighbouring columns are separated by a 2px gap in the card
 * surface rather than by outlines, only the topmost segment of a column is
 * rounded, and every column carries its own hover/focus readout.
 */
export function StackedColumnChart({
  buckets,
  seriesLabels,
  height = 160,
  fill = false,
  formatValue = defaultFormatValue,
  formatBucket = (t) => formatTime(t),
  totalLabel = "Total",
  ariaLabel,
  className,
}: StackedColumnChartProps) {
  const [activeIndex, setActiveIndex] = useState<number | null>(null);

  const columns = useMemo(
    () =>
      buckets.map((bucket) => {
        const segments = stackSegments(seriesLabels.map((_, index) => bucket.segments[index] ?? 0));
        const total = segments[segments.length - 1]?.end ?? 0;
        let topIndex = -1;
        for (const segment of segments) {
          if (segment.value > 0) {
            topIndex = segment.index;
          }
        }
        return { t: bucket.t, segments, total, topIndex };
      }),
    [buckets, seriesLabels],
  );

  const max = columns.reduce((peak, column) => Math.max(peak, column.total), 0);
  const scaleMax = max > 0 ? max : 1;
  const tickStride = Math.max(1, Math.ceil(columns.length / TARGET_TICK_LABELS));
  const active = activeIndex === null ? null : (columns[activeIndex] ?? null);
  const columnRatio = (index: number) =>
    columns.length > 0 ? (index + 0.5) / columns.length : 0.5;
  const activeRatio = activeIndex === null ? 0.5 : columnRatio(activeIndex);

  return (
    <div className={cn("w-full", fill && "flex min-h-0 flex-1 flex-col", className)}>
      <ChartLegend
        className="mb-2.5 shrink-0"
        entries={seriesLabels.map((label, index) => ({
          label,
          color: chartSeriesColor(index),
        }))}
      />
      <div
        className={cn("relative", fill && "min-h-0 flex-1")}
        style={fill ? undefined : { height }}
      >
        <div
          role="img"
          aria-label={ariaLabel}
          className="flex h-full items-stretch gap-[2px]"
          onPointerLeave={() => setActiveIndex(null)}
        >
          {columns.map((column, index) => (
            <div
              key={column.t}
              className={cn(
                "focus-visible:ring-ring flex min-w-0 flex-1 justify-center rounded-sm transition-colors duration-100 focus-visible:ring-2 focus-visible:outline-none",
                activeIndex === index && "bg-foreground/5",
              )}
              tabIndex={0}
              aria-label={`${formatBucket(column.t)}: ${formatValue(column.total)}`}
              onPointerEnter={() => setActiveIndex(index)}
              onFocus={() => setActiveIndex(index)}
              onBlur={() => setActiveIndex(null)}
            >
              <div
                className="flex h-full w-full flex-col-reverse justify-start gap-[2px]"
                style={{ maxWidth: MAX_COLUMN_WIDTH }}
              >
                {column.segments.map((segment) =>
                  segment.value > 0 ? (
                    <div
                      key={segment.index}
                      className={cn(segment.index === column.topIndex && "rounded-t-[4px]")}
                      style={{
                        height: `${(segment.value / scaleMax) * 100}%`,
                        minHeight: MIN_SEGMENT_PX,
                        backgroundColor: chartSeriesColor(segment.index),
                      }}
                    />
                  ) : null,
                )}
              </div>
            </div>
          ))}
        </div>
        <div
          aria-hidden="true"
          className="bg-border/70 pointer-events-none absolute inset-x-0 bottom-0 h-px"
        />
        {active ? (
          <ChartTooltip xRatio={activeRatio} title={formatBucket(active.t)}>
            {seriesLabels.map((label, index) => (
              <ChartTooltipRow
                key={label}
                color={chartSeriesColor(index)}
                label={label}
                value={formatValue(active.segments[index]?.value ?? 0)}
              />
            ))}
            {totalLabel ? (
              <div className="border-border/60 mt-1 border-t pt-1">
                <ChartTooltipRow label={totalLabel} value={formatValue(active.total)} />
              </div>
            ) : null}
          </ChartTooltip>
        ) : null}
      </div>
      <div className="text-muted-foreground relative mt-1.5 h-3.5 shrink-0 text-[10px] tabular-nums">
        {columns.map((column, index) => {
          if (index % tickStride !== 0) {
            return null;
          }
          const ratio = columnRatio(index);
          return (
            <span
              key={column.t}
              className="absolute whitespace-nowrap"
              style={{
                left: `${ratio * 100}%`,
                transform:
                  ratio < 0.1
                    ? "translateX(0)"
                    : ratio > 0.9
                      ? "translateX(-100%)"
                      : "translateX(-50%)",
              }}
            >
              {formatBucket(column.t)}
            </span>
          );
        })}
      </div>
    </div>
  );
}
