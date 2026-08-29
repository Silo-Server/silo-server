import { Link } from "react-router";

import { cn } from "@/lib/utils";
import { chartSeriesColor } from "./chartMath";

export interface BarListItem {
  id: string;
  label: string;
  value: number;
  /** Muted secondary metric rendered after the value (e.g. watch hours). */
  secondary?: string;
  /** Optional admin route the label links to. */
  to?: string;
}

export interface BarListProps {
  items: readonly BarListItem[];
  /** Categorical slot for the track color; bar lists are single-hue by design. */
  seriesIndex?: number;
  formatValue?: (value: number) => string;
  emptyLabel?: string;
  className?: string;
}

function defaultFormatValue(value: number): string {
  return value.toLocaleString();
}

/**
 * Ranked horizontal bars for "top N" lists.
 *
 * One hue for every row — the bars encode magnitude, not identity, so there is
 * nothing for a legend to say. Labels and values stay in text tokens over the
 * track wash.
 */
export function BarList({
  items,
  seriesIndex = 0,
  formatValue = defaultFormatValue,
  emptyLabel = "No activity yet",
  className,
}: BarListProps) {
  if (items.length === 0) {
    return <div className="text-muted-foreground py-4 text-center text-sm">{emptyLabel}</div>;
  }

  const color = chartSeriesColor(seriesIndex);
  const max = items.reduce(
    (peak, item) => (Number.isFinite(item.value) ? Math.max(peak, item.value) : peak),
    0,
  );

  return (
    <ol className={cn("space-y-1", className)}>
      {items.map((item, index) => {
        const value = Number.isFinite(item.value) ? Math.max(item.value, 0) : 0;
        const ratio = max > 0 ? value / max : 0;
        return (
          <li
            key={item.id}
            className="relative flex items-center gap-2 overflow-hidden rounded-sm px-2 py-1.5 text-sm"
          >
            <span
              aria-hidden="true"
              className="absolute inset-y-0 left-0 rounded-r-[4px]"
              style={{
                width: `${ratio * 100}%`,
                minWidth: value > 0 ? 6 : 0,
                backgroundColor: color,
                opacity: 0.18,
              }}
            />
            <span className="text-muted-foreground relative w-4 shrink-0 text-[11px] tabular-nums">
              {index + 1}
            </span>
            <span className="relative min-w-0 flex-1 truncate" title={item.label}>
              {item.to ? (
                <Link to={item.to} className="hover:text-primary transition-colors">
                  {item.label}
                </Link>
              ) : (
                item.label
              )}
            </span>
            <span className="relative shrink-0 text-[13px] font-semibold tabular-nums">
              {formatValue(value)}
            </span>
            {item.secondary ? (
              <span className="text-muted-foreground relative shrink-0 text-[11px] tabular-nums">
                {item.secondary}
              </span>
            ) : null}
          </li>
        );
      })}
    </ol>
  );
}
