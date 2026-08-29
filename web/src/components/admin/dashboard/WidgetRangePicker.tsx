import { cn } from "@/lib/utils";
import { rangeLabel, rangePhrase, WIDGET_RANGE_ORDER } from "./range";
import { findDashboardWidget } from "./registry";
import type { WidgetRange } from "./types";
import { useWidgetRange } from "./widgetChrome";

/**
 * A compact segmented control for a widget's window.
 *
 * Toggle buttons rather than a listbox: there are at most four options, they
 * are two characters wide, and one tap has to change the chart — a dropdown
 * would cost an extra interaction for the same choice. Each button reports its
 * own pressed state, so a screen reader hears which window is showing without
 * having to open anything.
 */
export function RangeSegmentedControl({
  value,
  options,
  onChange,
  className,
}: {
  value: WidgetRange;
  options: readonly WidgetRange[];
  onChange: (range: WidgetRange) => void;
  className?: string;
}) {
  if (options.length < 2) {
    return null;
  }

  return (
    <div
      className={cn(
        "border-border/60 bg-muted/20 flex shrink-0 items-center gap-0.5 rounded-full border p-0.5",
        className,
      )}
    >
      {WIDGET_RANGE_ORDER.filter((range) => options.includes(range)).map((range) => {
        const active = range === value;
        return (
          <button
            key={range}
            type="button"
            aria-pressed={active}
            aria-label={`Show ${rangePhrase(range)}`}
            className={cn(
              "cursor-pointer rounded-full px-1.5 py-0.5 text-[10px] leading-none font-semibold tabular-nums transition-colors",
              "focus-visible:ring-ring focus-visible:ring-2 focus-visible:outline-none",
              active
                ? "border-primary/40 bg-primary/15 text-foreground"
                : "text-muted-foreground hover:text-foreground hover:bg-muted/60",
            )}
            onClick={() => onChange(range)}
          >
            {rangeLabel(range)}
          </button>
        );
      })}
    </div>
  );
}

/**
 * The range picker as a widget wears it: options come from the registry, the
 * current value and the setter from the grid. Renders nothing when the widget
 * is not placed in a grid (unit tests) or offers no ranges.
 */
export function WidgetRangePicker({ className }: { className?: string }) {
  const { id, range, setRange } = useWidgetRange();
  const ranges = id ? findDashboardWidget(id)?.ranges : undefined;
  if (!ranges) {
    return null;
  }
  return (
    <RangeSegmentedControl
      value={range}
      options={ranges.allowed}
      onChange={setRange}
      className={className}
    />
  );
}
