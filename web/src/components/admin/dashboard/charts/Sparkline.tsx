import { useMemo } from "react";

import { cn } from "@/lib/utils";
import { buildAreaPath, buildLinePath, chartSeriesColor } from "./chartMath";

export interface SparklineProps {
  /** Evenly spaced samples; `null` breaks the line instead of reading as zero. */
  values: readonly (number | null)[];
  height?: number;
  seriesIndex?: number;
  showArea?: boolean;
  ariaLabel: string;
  className?: string;
}

const VIEW_WIDTH = 100;
const VIEW_HEIGHT = 100;

/**
 * Axis-less trend line for stat tiles. No hover layer: the tile's value is the
 * reading, the sparkline only shows its shape over the window.
 */
export function Sparkline({
  values,
  height = 32,
  seriesIndex = 0,
  showArea = true,
  ariaLabel,
  className,
}: SparklineProps) {
  const color = chartSeriesColor(seriesIndex);

  const { linePath, areaPath } = useMemo(() => {
    const numeric = values.filter(
      (value): value is number => value !== null && Number.isFinite(value),
    );
    const max = numeric.length > 0 ? Math.max(...numeric) : 0;
    const top = max > 0 ? max : 1;
    const projected = values.map((value, index) => ({
      x: values.length <= 1 ? VIEW_WIDTH / 2 : (index / (values.length - 1)) * VIEW_WIDTH,
      y:
        value === null || !Number.isFinite(value)
          ? null
          : VIEW_HEIGHT - (value / top) * VIEW_HEIGHT,
    }));
    return {
      linePath: buildLinePath(projected),
      areaPath: showArea ? buildAreaPath(projected, VIEW_HEIGHT) : "",
    };
  }, [values, showArea]);

  return (
    <svg
      role="img"
      aria-label={ariaLabel}
      className={cn("w-full overflow-visible", className)}
      style={{ height }}
      viewBox={`0 0 ${VIEW_WIDTH} ${VIEW_HEIGHT}`}
      preserveAspectRatio="none"
    >
      {areaPath ? <path d={areaPath} fill={color} opacity={0.12} stroke="none" /> : null}
      {linePath ? (
        <path
          d={linePath}
          fill="none"
          stroke={color}
          strokeWidth={2}
          strokeLinecap="round"
          strokeLinejoin="round"
          vectorEffect="non-scaling-stroke"
        />
      ) : null}
    </svg>
  );
}
