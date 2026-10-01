import { describe, expect, it } from "vitest";

import {
  buildAreaPath,
  buildLinePath,
  chartSeriesColor,
  niceTicks,
  stackSegments,
  timeBuckets,
  type LinePathPoint,
} from "./chartMath";

describe("chartSeriesColor", () => {
  const cases: { name: string; index: number; expected: string }[] = [
    { name: "first slot", index: 0, expected: "var(--chart-1)" },
    { name: "third slot", index: 2, expected: "var(--chart-3)" },
    { name: "last slot", index: 4, expected: "var(--chart-5)" },
    { name: "clamps past the last slot instead of cycling", index: 9, expected: "var(--chart-5)" },
    { name: "clamps negative indexes", index: -3, expected: "var(--chart-1)" },
    { name: "falls back for non-finite indexes", index: Number.NaN, expected: "var(--chart-1)" },
  ];

  for (const { name, index, expected } of cases) {
    it(name, () => {
      expect(chartSeriesColor(index)).toBe(expected);
    });
  }
});

describe("niceTicks", () => {
  const cases: {
    name: string;
    args: Parameters<typeof niceTicks>;
    expected: number[];
  }[] = [
    { name: "rounds a small count range up", args: [0, 3, 4], expected: [0, 2, 4] },
    { name: "steps a wide range by tens", args: [0, 100, 5], expected: [0, 20, 40, 60, 80, 100] },
    { name: "handles a fractional range", args: [0, 0.4, 4], expected: [0, 0.2, 0.4] },
    { name: "swaps reversed bounds", args: [10, 0, 3], expected: [0, 5, 10] },
    { name: "expands a flat series", args: [0, 0, 4], expected: [0, 0.5, 1] },
    {
      name: "honors a minimum step for counted values",
      args: [0, 0, 4, { minStep: 1 }],
      expected: [0, 1],
    },
    {
      name: "keeps integer steps when the range is tiny",
      args: [0, 1, 4, { minStep: 1 }],
      expected: [0, 1],
    },
    {
      name: "falls back for non-finite bounds",
      args: [Number.NaN, Number.NaN, 4],
      expected: [0, 0.5, 1],
    },
  ];

  for (const { name, args, expected } of cases) {
    it(name, () => {
      expect(niceTicks(...args)).toEqual(expected);
    });
  }

  it("always covers the requested range", () => {
    const ticks = niceTicks(0, 37, 4);
    expect(ticks[0]).toBeLessThanOrEqual(0);
    expect(ticks[ticks.length - 1]).toBeGreaterThanOrEqual(37);
  });
});

describe("buildLinePath", () => {
  const cases: { name: string; points: LinePathPoint[]; expected: string }[] = [
    { name: "renders nothing for no points", points: [], expected: "" },
    {
      name: "renders nothing when every sample is missing",
      points: [
        { x: 0, y: null },
        { x: 10, y: null },
      ],
      expected: "",
    },
    {
      name: "renders an isolated sample as a zero-length subpath",
      points: [{ x: 0, y: 5 }],
      expected: "M 0 5 L 0 5",
    },
    {
      name: "connects contiguous samples",
      points: [
        { x: 0, y: 5 },
        { x: 10, y: 3 },
        { x: 20, y: 4 },
      ],
      expected: "M 0 5 L 10 3 L 20 4",
    },
    {
      name: "breaks the path across a gap",
      points: [
        { x: 0, y: 1 },
        { x: 10, y: 2 },
        { x: 20, y: null },
        { x: 30, y: 4 },
        { x: 40, y: 5 },
      ],
      expected: "M 0 1 L 10 2 M 30 4 L 40 5",
    },
    {
      name: "treats a non-finite value as a gap",
      points: [
        { x: 0, y: 1 },
        { x: 10, y: Number.NaN },
        { x: 20, y: 3 },
      ],
      expected: "M 0 1 L 0 1 M 20 3 L 20 3",
    },
    {
      name: "rounds coordinates to two decimals",
      points: [
        { x: 1.234, y: 2.567 },
        { x: 3.891, y: 4.111 },
      ],
      expected: "M 1.23 2.57 L 3.89 4.11",
    },
  ];

  for (const { name, points, expected } of cases) {
    it(name, () => {
      expect(buildLinePath(points)).toBe(expected);
    });
  }
});

describe("buildAreaPath", () => {
  it("closes a run to the baseline", () => {
    expect(
      buildAreaPath(
        [
          { x: 0, y: 2 },
          { x: 10, y: 4 },
        ],
        100,
      ),
    ).toBe("M 0 100 L 0 2 L 10 4 L 10 100 Z");
  });

  it("closes each run separately across a gap", () => {
    expect(
      buildAreaPath(
        [
          { x: 0, y: 2 },
          { x: 10, y: null },
          { x: 20, y: 6 },
        ],
        50,
      ),
    ).toBe("M 0 50 L 0 2 L 0 50 Z M 20 50 L 20 6 L 20 50 Z");
  });

  it("renders nothing without samples", () => {
    expect(buildAreaPath([{ x: 0, y: null }], 10)).toBe("");
  });
});

describe("stackSegments", () => {
  const cases: {
    name: string;
    values: number[];
    expected: { index: number; value: number; start: number; end: number }[];
  }[] = [
    { name: "returns nothing for no series", values: [], expected: [] },
    {
      name: "accumulates offsets from the baseline",
      values: [2, 1, 3],
      expected: [
        { index: 0, value: 2, start: 0, end: 2 },
        { index: 1, value: 1, start: 2, end: 3 },
        { index: 2, value: 3, start: 3, end: 6 },
      ],
    },
    {
      name: "keeps zero segments so indexes line up with series",
      values: [0, 4],
      expected: [
        { index: 0, value: 0, start: 0, end: 0 },
        { index: 1, value: 4, start: 0, end: 4 },
      ],
    },
    {
      name: "clamps negative and non-finite values to zero",
      values: [-5, Number.NaN, 3],
      expected: [
        { index: 0, value: 0, start: 0, end: 0 },
        { index: 1, value: 0, start: 0, end: 0 },
        { index: 2, value: 3, start: 0, end: 3 },
      ],
    },
  ];

  for (const { name, values, expected } of cases) {
    it(name, () => {
      expect(stackSegments(values)).toEqual(expected);
    });
  }
});

describe("timeBuckets", () => {
  const MINUTE = 60_000;

  it("zero-fills every bucket in the window", () => {
    const buckets = timeBuckets(0, 3 * MINUTE, MINUTE, [{ t: MINUTE, value: 5 }], 0);

    expect(buckets).toEqual([
      { t: 0, value: 0, present: false },
      { t: MINUTE, value: 5, present: true },
      { t: 2 * MINUTE, value: 0, present: false },
      { t: 3 * MINUTE, value: 0, present: false },
    ]);
  });

  it("assigns samples that land mid-bucket", () => {
    const buckets = timeBuckets(0, MINUTE, MINUTE, [{ t: MINUTE + 15_000, value: 7 }], 0);

    expect(buckets[1]).toEqual({ t: MINUTE, value: 7, present: true });
  });

  it("ignores samples outside the window", () => {
    const buckets = timeBuckets(
      MINUTE,
      2 * MINUTE,
      MINUTE,
      [
        { t: 0, value: 1 },
        { t: 9 * MINUTE, value: 2 },
        { t: Number.NaN, value: 3 },
      ],
      0,
    );

    expect(buckets.every((bucket) => !bucket.present)).toBe(true);
  });

  it("lets the later sample win within one bucket", () => {
    const buckets = timeBuckets(
      0,
      0,
      MINUTE,
      [
        { t: 0, value: 1 },
        { t: 30_000, value: 2 },
      ],
      0,
    );

    expect(buckets).toEqual([{ t: 0, value: 2, present: true }]);
  });

  it("supports non-numeric bucket payloads", () => {
    const buckets = timeBuckets<number[]>(0, MINUTE, MINUTE, [{ t: 0, value: [1, 2] }], []);

    expect(buckets).toEqual([
      { t: 0, value: [1, 2], present: true },
      { t: MINUTE, value: [], present: false },
    ]);
  });

  const degenerate: { name: string; args: Parameters<typeof timeBuckets<number>> }[] = [
    { name: "an inverted window", args: [MINUTE, 0, MINUTE, [], 0] },
    { name: "a zero step", args: [0, MINUTE, 0, [], 0] },
    { name: "a negative step", args: [0, MINUTE, -MINUTE, [], 0] },
    { name: "a non-finite bound", args: [0, Number.POSITIVE_INFINITY, MINUTE, [], 0] },
  ];

  for (const { name, args } of degenerate) {
    it(`returns nothing for ${name}`, () => {
      expect(timeBuckets(...args)).toEqual([]);
    });
  }
});
