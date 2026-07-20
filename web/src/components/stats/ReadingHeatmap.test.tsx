import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import ReadingHeatmap, { heatmapBuckets, type HeatmapDay } from "./ReadingHeatmap";

function addDays(date: string, count: number): string {
  const d = new Date(`${date}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + count);
  return d.toISOString().slice(0, 10);
}

function range(start: string, days: number): HeatmapDay[] {
  return Array.from({ length: days }, (_, i) => ({ date: addDays(start, i), seconds: 0 }));
}

describe("heatmapBuckets", () => {
  it("buckets zero seconds as 0 regardless of max", () => {
    const days: HeatmapDay[] = [{ date: "2026-01-04", seconds: 0 }];
    const cells = heatmapBuckets(days, 400);
    expect(cells[0]).toEqual({ date: "2026-01-04", seconds: 0, bucket: 0 });
  });

  it("buckets seconds into quartiles of the given max", () => {
    // 2026-01-04 is a Sunday, so no leading blanks — cell index == day index.
    const days: HeatmapDay[] = [
      { date: "2026-01-04", seconds: 0 },
      { date: "2026-01-05", seconds: 100 },
      { date: "2026-01-06", seconds: 101 },
      { date: "2026-01-07", seconds: 200 },
      { date: "2026-01-08", seconds: 201 },
      { date: "2026-01-09", seconds: 300 },
      { date: "2026-01-10", seconds: 301 },
      { date: "2026-01-11", seconds: 400 },
    ];
    const buckets = heatmapBuckets(days, 400).map((c) => c?.bucket);
    expect(buckets).toEqual([0, 1, 2, 2, 3, 3, 4, 4]);
  });

  it("treats a zero or negative max as all-zero buckets", () => {
    const days: HeatmapDay[] = [{ date: "2026-01-04", seconds: 0 }];
    expect(heatmapBuckets(days, 0)[0]?.bucket).toBe(0);
  });

  it("pads leading blanks so the first day lands in its weekday row", () => {
    // 2026-01-01 is a Thursday (getUTCDay() === 4), so the grid should be
    // padded with 4 leading blank cells before the first real day.
    const days: HeatmapDay[] = [{ date: "2026-01-01", seconds: 5 }];
    const cells = heatmapBuckets(days, 5);
    expect(cells).toHaveLength(5);
    expect(cells.slice(0, 4)).toEqual([null, null, null, null]);
    expect(cells[4]).toEqual({ date: "2026-01-01", seconds: 5, bucket: 4 });
  });

  it("returns an empty grid for an empty range", () => {
    expect(heatmapBuckets([], 100)).toEqual([]);
  });

  it("produces a 366-cell grid for a full default range starting on a week boundary", () => {
    // 2026-01-04 is a Sunday, so there are no leading blanks and the grid
    // length matches the day count exactly (the server's default range is
    // 366 days: today plus the prior 365).
    const days = range("2026-01-04", 366);
    const cells = heatmapBuckets(days, 100);
    expect(cells).toHaveLength(366);
    expect(cells.every((c) => c !== null)).toBe(true);
  });
});

describe("ReadingHeatmap", () => {
  it("renders one cell per day plus leading blanks, with a date · Xm tooltip", () => {
    const days: HeatmapDay[] = [
      { date: "2026-01-01", seconds: 0 },
      { date: "2026-01-02", seconds: 90 },
    ];
    const markup = renderToStaticMarkup(<ReadingHeatmap days={days} />);
    expect(markup).toContain("2026-01-02 · 2m");
  });

  it("renders an empty grid without crashing when there is no data", () => {
    const markup = renderToStaticMarkup(<ReadingHeatmap days={[]} />);
    expect(typeof markup).toBe("string");
  });
});
