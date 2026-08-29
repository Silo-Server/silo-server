import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * Small pieces every chart in this folder shares: the legend that keeps
 * identity off color alone, and the hover readout. Kept here rather than in
 * each chart so the two never drift apart visually.
 */

export interface ChartLegendEntry {
  label: string;
  /** A `var(--chart-N)` token, from `chartSeriesColor`. */
  color: string;
}

/**
 * Legend row. Always rendered for two or more series; a single-series chart is
 * titled by its card header instead. The swatch mirrors the mark (a rect for
 * columns and areas, a short stroke for lines) and the label wears text tokens,
 * never the series color.
 */
export function ChartLegend({
  entries,
  shape = "rect",
  className,
}: {
  entries: readonly ChartLegendEntry[];
  shape?: "rect" | "line";
  className?: string;
}) {
  if (entries.length < 2) {
    return null;
  }
  return (
    <div
      className={cn(
        "text-muted-foreground flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px]",
        className,
      )}
    >
      {entries.map((entry) => (
        <span key={entry.label} className="flex items-center gap-1.5">
          <span
            aria-hidden="true"
            className={cn(
              "shrink-0",
              shape === "line" ? "h-0.5 w-3 rounded-full" : "h-2 w-2 rounded-[2px]",
            )}
            style={{ backgroundColor: entry.color }}
          />
          {entry.label}
        </span>
      ))}
    </div>
  );
}

function clampRatio(ratio: number): number {
  if (!Number.isFinite(ratio)) {
    return 0;
  }
  return Math.min(Math.max(ratio, 0), 1);
}

/**
 * Hover/focus readout anchored above the plot at `xRatio` (0 = left edge,
 * 1 = right edge). It shifts to stay inside the card near the edges instead of
 * overflowing the widget.
 */
export function ChartTooltip({
  xRatio,
  title,
  children,
  className,
}: {
  xRatio: number;
  title: string;
  children?: ReactNode;
  className?: string;
}) {
  const ratio = clampRatio(xRatio);
  const shift = ratio < 0.15 ? "0%" : ratio > 0.85 ? "-100%" : "-50%";
  return (
    <div
      className={cn("pointer-events-none absolute top-0 z-10", className)}
      style={{ left: `${ratio * 100}%`, transform: `translateX(${shift})` }}
    >
      <div className="border-border bg-popover/95 text-popover-foreground rounded-md border px-2.5 py-1.5 shadow-md">
        <div className="text-muted-foreground mb-1 text-[10px] whitespace-nowrap tabular-nums">
          {title}
        </div>
        {children}
      </div>
    </div>
  );
}

/**
 * One tooltip line: the value leads in strong ink, the series name follows in
 * muted ink beside a short stroke in the series color.
 */
export function ChartTooltipRow({
  color,
  label,
  value,
}: {
  color?: string;
  label: string;
  value: string;
}) {
  return (
    <div className="flex items-center gap-2 text-[11px] whitespace-nowrap">
      {color ? (
        <span
          aria-hidden="true"
          className="h-0.5 w-3 shrink-0 rounded-full"
          style={{ backgroundColor: color }}
        />
      ) : null}
      <span className="text-muted-foreground">{label}</span>
      <span className="ml-auto font-semibold tabular-nums">{value}</span>
    </div>
  );
}
