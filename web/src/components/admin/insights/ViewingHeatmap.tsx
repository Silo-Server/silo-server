import { useMemo, useRef, useState, type KeyboardEvent } from "react";

import { formatWatchTime } from "@/components/admin/dashboard/format";
import { useT } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";
import { cn } from "@/lib/utils";

import { useHourLabels, useWeekdayLabels, type ViewingTimeCell } from "./viewingTime";

// One-hue ramp on the Direct play color, so the grid matches the other
// Insights charts. --primary is near-white in this theme, and opacity steps of
// it read as greys that are hard to tell apart. The first step stays as far
// from an empty cell as the old one was, and the top step mixes past --chart-1
// toward --contrast-boost so the busiest hours stand out.
const LEVEL_CLASSES = [
  "bg-muted/35",
  "bg-chart-1/40",
  "bg-chart-1/70",
  "bg-chart-1",
  "bg-[color-mix(in_srgb,var(--chart-1)_45%,var(--contrast-boost))]",
] as const;

function heatmapLevel(plays: number, maxPlays: number): number {
  if (plays <= 0 || maxPlays <= 0) return 0;
  const ratio = plays / maxPlays;
  if (ratio <= 0.25) return 1;
  if (ratio <= 0.5) return 2;
  if (ratio <= 0.75) return 3;
  return 4;
}

/**
 * Weekday × hour grid of play starts. One cell is focusable at a time (roving
 * tabindex) and the arrow keys move between cells, so the grid is one tab stop
 * however many hours it has. `compact` uses shorter cells so the grid and its
 * legend fit a three-row dashboard widget.
 */
export function ViewingHeatmap({
  cells,
  compact = false,
}: {
  cells: readonly ViewingTimeCell[];
  compact?: boolean;
}) {
  const t = useT();
  const weekdayLabels = useWeekdayLabels();
  const hourLabels = useHourLabels();
  const [active, setActive] = useState<{ dow: number; hour: number }>({ dow: 0, hour: 0 });
  const cellRefs = useRef<Array<HTMLDivElement | null>>([]);

  const grid = useMemo(() => {
    const map = new Map<string, { plays: number; watchSeconds: number }>();
    let maxPlays = 0;
    for (const cell of cells) {
      map.set(`${cell.day_of_week}:${cell.hour}`, {
        plays: cell.plays,
        watchSeconds: cell.watch_seconds,
      });
      if (cell.plays > maxPlays) maxPlays = cell.plays;
    }
    return { map, maxPlays };
  }, [cells]);

  function moveFocus(dow: number, hour: number) {
    setActive({ dow, hour });
    cellRefs.current[dow * 24 + hour]?.focus();
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>, dow: number, hour: number) {
    switch (event.key) {
      case "ArrowRight":
        event.preventDefault();
        moveFocus(dow, Math.min(23, hour + 1));
        break;
      case "ArrowLeft":
        event.preventDefault();
        moveFocus(dow, Math.max(0, hour - 1));
        break;
      case "ArrowDown":
        event.preventDefault();
        moveFocus(Math.min(6, dow + 1), hour);
        break;
      case "ArrowUp":
        event.preventDefault();
        moveFocus(Math.max(0, dow - 1), hour);
        break;
      case "Home":
        event.preventDefault();
        moveFocus(event.ctrlKey ? 0 : dow, 0);
        break;
      case "End":
        event.preventDefault();
        moveFocus(event.ctrlKey ? 6 : dow, 23);
        break;
      default:
        break;
    }
  }

  return (
    <div className={compact ? "space-y-2" : "space-y-3"}>
      <div className="overflow-x-auto">
        <div
          role="grid"
          aria-label={t("web.admin.insights.activity.heatmap.title")}
          dir="ltr"
          className={cn("min-w-[440px]", compact ? "space-y-0.5" : "space-y-1")}
        >
          <div
            role="row"
            className="grid grid-cols-[2.75rem_repeat(24,minmax(0,1fr))] items-center gap-1 pb-1"
          >
            <span role="columnheader" aria-hidden="true" />
            {hourLabels.map((label, hour) => (
              <span
                key={hour}
                role="columnheader"
                className="text-muted-foreground text-center text-[0.625rem] tabular-nums"
              >
                {hour % 6 === 0 ? label : ""}
              </span>
            ))}
          </div>
          {weekdayLabels.map((dayLabel, dow) => (
            <div
              key={dow}
              role="row"
              className="grid grid-cols-[2.75rem_repeat(24,minmax(0,1fr))] items-center gap-1"
            >
              <span
                role="rowheader"
                className="text-muted-foreground pe-1 text-end text-[0.6875rem] font-medium"
              >
                {dayLabel}
              </span>
              {hourLabels.map((hourLabel, hour) => {
                const cell = grid.map.get(`${dow}:${hour}`);
                const plays = cell?.plays ?? 0;
                const tooltip = t("web.admin.insights.activity.heatmap.cellTooltip", {
                  day: dayLabel,
                  hour: hourLabel,
                  plays: t("web.admin.insights.playCount", {
                    count: plays,
                    count_text: plays.toLocaleString(formatLocale()),
                  }),
                  watch_time: formatWatchTime(cell?.watchSeconds ?? 0),
                });
                const isActive = active.dow === dow && active.hour === hour;
                return (
                  <div
                    key={hour}
                    ref={(el) => {
                      cellRefs.current[dow * 24 + hour] = el;
                    }}
                    tabIndex={isActive ? 0 : -1}
                    role="gridcell"
                    aria-label={tooltip}
                    title={tooltip}
                    onFocus={() => setActive({ dow, hour })}
                    onKeyDown={(e) => handleKeyDown(e, dow, hour)}
                    className={cn(
                      "focus-visible:ring-ring rounded-[4px] transition-transform hover:scale-110 focus-visible:ring-2 focus-visible:outline-none",
                      compact ? "h-4" : "h-5",
                      LEVEL_CLASSES[heatmapLevel(plays, grid.maxPlays)],
                    )}
                  />
                );
              })}
            </div>
          ))}
        </div>
      </div>
      <div className="text-muted-foreground flex items-center justify-end gap-1.5 text-[0.6875rem]">
        <span>{t("web.admin.insights.activity.heatmap.less")}</span>
        {LEVEL_CLASSES.map((cls, idx) => (
          <span key={idx} className={cn("h-3 w-3 rounded-[3px]", cls)} />
        ))}
        <span>{t("web.admin.insights.activity.heatmap.more")}</span>
      </div>
    </div>
  );
}
