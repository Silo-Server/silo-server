import { useMemo } from "react";

import { cn } from "@/lib/utils";

export interface HeatmapDay {
  date: string;
  seconds: number;
}

export type HeatmapBucket = 0 | 1 | 2 | 3 | 4;

export interface HeatmapCell {
  date: string;
  seconds: number;
  bucket: HeatmapBucket;
}

const BUCKET_CLASSES: Record<HeatmapBucket, string> = {
  0: "bg-muted",
  1: "bg-primary/25",
  2: "bg-primary/50",
  3: "bg-primary/75",
  4: "bg-primary",
};

function bucketFor(seconds: number, max: number): HeatmapBucket {
  if (seconds <= 0 || max <= 0) return 0;
  const quartile = max / 4;
  if (seconds <= quartile) return 1;
  if (seconds <= quartile * 2) return 2;
  if (seconds <= quartile * 3) return 3;
  return 4;
}

const MS_PER_DAY = 24 * 60 * 60 * 1000;

/**
 * Fills every date in [from, to] (inclusive, UTC calendar days) with a
 * `{date, seconds: 0}` entry where `days` has none. The server's daily
 * rollup only returns days that had at least one session, so any gap must be
 * densified client-side before `heatmapBuckets` lays days into consecutive
 * grid cells — otherwise a gap silently shifts every later day left by one
 * cell, compressing the calendar and putting it on the wrong weekday row.
 */
export function densifyDays(days: HeatmapDay[], from: string, to: string): HeatmapDay[] {
  const secondsByDate = new Map(days.map((day) => [day.date, day.seconds]));
  const start = new Date(`${from}T00:00:00Z`).getTime();
  const end = new Date(`${to}T00:00:00Z`).getTime();
  const filled: HeatmapDay[] = [];
  for (let t = start; t <= end; t += MS_PER_DAY) {
    const date = new Date(t).toISOString().slice(0, 10);
    filled.push({ date, seconds: secondsByDate.get(date) ?? 0 });
  }
  return filled;
}

/**
 * Buckets each day's reading seconds into a 0-4 intensity level (0 = no
 * reading, 1-4 = quartiles of the profile's busiest day in the range) and
 * pads the front of the list with blank cells so the first day lands in its
 * correct weekday row of a GitHub-style column-per-week grid (weeks run
 * left-to-right, days run top-to-bottom starting Sunday).
 *
 * Assumes `days` has no gaps — pass it through `densifyDays` first if it
 * might be sparse (e.g. the server's daily rollup), or later days will land
 * in the wrong weekday row.
 */
export function heatmapBuckets(days: HeatmapDay[], max: number): (HeatmapCell | null)[] {
  const firstDay = days[0];
  if (!firstDay) return [];
  const leadingBlanks = new Date(`${firstDay.date}T00:00:00Z`).getUTCDay();
  const blanks: null[] = new Array(leadingBlanks).fill(null);
  const cells: HeatmapCell[] = days.map((day) => ({
    date: day.date,
    seconds: day.seconds,
    bucket: bucketFor(day.seconds, max),
  }));
  return [...blanks, ...cells];
}

export interface ReadingHeatmapProps {
  days: HeatmapDay[];
  /**
   * The exact [from, to] range `days` was requested for (YYYY-MM-DD, UTC).
   * When given, gap days (no session that day) are densified to zero-second
   * entries before bucketing, so the grid can't compress and misalign
   * weekday rows. Optional for callers that already pass a dense list.
   */
  from?: string;
  to?: string;
}

export default function ReadingHeatmap({ days, from, to }: ReadingHeatmapProps) {
  const denseDays = useMemo(
    () => (from && to ? densifyDays(days, from, to) : days),
    [days, from, to],
  );
  const max = useMemo(
    () => denseDays.reduce((acc, day) => Math.max(acc, day.seconds), 0),
    [denseDays],
  );
  const cells = useMemo(() => heatmapBuckets(denseDays, max), [denseDays, max]);
  const columns = Math.max(1, Math.ceil(cells.length / 7));

  return (
    <div
      role="img"
      aria-label="Reading activity heatmap"
      className="grid w-full grid-flow-col grid-rows-7 gap-1 overflow-x-auto"
      style={{ gridTemplateColumns: `repeat(${columns}, minmax(0.5rem, 1fr))` }}
    >
      {cells.map((cell, i) =>
        cell ? (
          <div
            key={cell.date}
            title={`${cell.date} · ${Math.round(cell.seconds / 60)}m`}
            className={cn("aspect-square rounded-[2px]", BUCKET_CLASSES[cell.bucket])}
          />
        ) : (
          <div key={`blank-${i}`} aria-hidden="true" className="aspect-square" />
        ),
      )}
    </div>
  );
}
