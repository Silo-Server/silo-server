import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import ReadingHeatmap, { densifyDays, heatmapBuckets, type HeatmapDay } from "./ReadingHeatmap";

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

describe("densifyDays", () => {
  it("fills gap days with zero seconds across the given range", () => {
    const days: HeatmapDay[] = [
      { date: "2026-07-01", seconds: 100 },
      { date: "2026-07-03", seconds: 200 },
    ];
    expect(densifyDays(days, "2026-07-01", "2026-07-05")).toEqual([
      { date: "2026-07-01", seconds: 100 },
      { date: "2026-07-02", seconds: 0 },
      { date: "2026-07-03", seconds: 200 },
      { date: "2026-07-04", seconds: 0 },
      { date: "2026-07-05", seconds: 0 },
    ]);
  });

  it("returns an empty range unchanged when it collapses to nothing", () => {
    expect(densifyDays([], "2026-07-05", "2026-07-01")).toEqual([]);
  });

  it("returns a single-day list when from equals to", () => {
    expect(densifyDays([{ date: "2026-07-04", seconds: 30 }], "2026-07-04", "2026-07-04")).toEqual([
      { date: "2026-07-04", seconds: 30 },
    ]);
  });
});

describe("heatmapBuckets + densifyDays (sparse history regression)", () => {
  it("keeps the day after a gap in its correct weekday cell instead of compressing the grid", () => {
    // 2026-07-01 is a Wednesday (getUTCDay() === 3); 2026-07-03 is a Friday
    // (getUTCDay() === 5). A gap on 2026-07-02 (no session that day) must not
    // shift 2026-07-03 left by one cell, or its weekday row would be wrong.
    const sparseDays: HeatmapDay[] = [
      { date: "2026-07-01", seconds: 100 },
      { date: "2026-07-03", seconds: 200 },
    ];
    const densified = densifyDays(sparseDays, "2026-07-01", "2026-07-05");
    expect(densified).toHaveLength(5);

    const cells = heatmapBuckets(densified, 200);
    // 3 leading blanks (Wednesday) + 5 densified days.
    expect(cells).toHaveLength(8);
    expect(cells[3]).toEqual({ date: "2026-07-01", seconds: 100, bucket: 2 });
    expect(cells[4]).toEqual({ date: "2026-07-02", seconds: 0, bucket: 0 }); // gap day
    expect(cells[5]).toEqual({ date: "2026-07-03", seconds: 200, bucket: 4 });

    // Weekday alignment: a cell's index modulo 7 must equal its date's UTC
    // weekday for the GitHub-style grid to read correctly.
    expect(5 % 7).toBe(new Date("2026-07-03T00:00:00Z").getUTCDay());
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

  it("densifies gap days when given a from/to range, rendering a filled-in cell for the gap", () => {
    const days: HeatmapDay[] = [
      { date: "2026-07-01", seconds: 100 },
      { date: "2026-07-03", seconds: 200 },
    ];
    const markup = renderToStaticMarkup(
      <ReadingHeatmap days={days} from="2026-07-01" to="2026-07-05" />,
    );
    // The gap day (2026-07-02) must render its own zero-second tooltip cell
    // rather than being skipped.
    expect(markup).toContain("2026-07-02 · 0m");
  });
});
