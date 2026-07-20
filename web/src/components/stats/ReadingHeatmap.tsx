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

/**
 * Buckets each day's reading seconds into a 0-4 intensity level (0 = no
 * reading, 1-4 = quartiles of the profile's busiest day in the range) and
 * pads the front of the list with blank cells so the first day lands in its
 * correct weekday row of a GitHub-style column-per-week grid (weeks run
 * left-to-right, days run top-to-bottom starting Sunday).
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
}

export default function ReadingHeatmap({ days }: ReadingHeatmapProps) {
  const max = useMemo(() => days.reduce((acc, day) => Math.max(acc, day.seconds), 0), [days]);
  const cells = useMemo(() => heatmapBuckets(days, max), [days, max]);
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
