import { describe, expect, it } from "vitest";

import {
  appendBucketRow,
  bucketRowsFromRaw,
  parseBucketPolicies,
  recommendedBucketRows,
  removeBucketRow,
  serializeBucketPolicies,
  serializeBucketRows,
  updateBucketRow,
  type LogRetentionBucketPolicy,
} from "./logRetentionPolicy";

describe("logRetentionPolicy", () => {
  it("parses valid bucket policies and normalizes levels", () => {
    const policies = parseBucketPolicies(
      JSON.stringify([
        {
          component: "metadata",
          level: "INFO",
          retention_days: "1",
          max_rows: "100000",
          max_size_mb: "128",
        },
      ]),
    );

    expect(policies).toEqual<LogRetentionBucketPolicy[]>([
      {
        component: "metadata",
        level: "info",
        retention_days: 1,
        max_rows: 100000,
        max_size_mb: 128,
      },
    ]);
  });

  it("drops invalid policies when parsing", () => {
    const policies = parseBucketPolicies(
      JSON.stringify([
        { component: "", level: "info", retention_days: 1, max_rows: 10, max_size_mb: 10 },
        {
          component: "scanner",
          level: "verbose",
          retention_days: 1,
          max_rows: 10,
          max_size_mb: 10,
        },
      ]),
    );

    expect(policies).toEqual([]);
  });

  it("preserves zero for disabled limits and normalizes invalid numeric fields to 0", () => {
    const policies = parseBucketPolicies(
      JSON.stringify([
        {
          component: "metadata",
          level: "info",
          retention_days: 0,
          max_rows: -5,
          max_size_mb: "",
        },
      ]),
    );

    expect(policies).toEqual<LogRetentionBucketPolicy[]>([
      {
        component: "metadata",
        level: "info",
        retention_days: 0,
        max_rows: 0,
        max_size_mb: 0,
      },
    ]);
  });

  it("serializes disabled limits as 0", () => {
    const raw = serializeBucketPolicies([
      {
        component: "metadata",
        level: "info",
        retention_days: 0,
        max_rows: 0,
        max_size_mb: 0,
      },
    ]);

    expect(JSON.parse(raw)).toEqual([
      {
        component: "metadata",
        level: "info",
        retention_days: 0,
        max_rows: 0,
        max_size_mb: 0,
      },
    ]);
  });

  it("serializes only valid policies", () => {
    const raw = serializeBucketPolicies([
      {
        component: " metadata ",
        level: "INFO",
        retention_days: 1,
        max_rows: 100000,
        max_size_mb: 128,
      },
      {
        component: "",
        level: "warn",
        retention_days: 7,
        max_rows: 250000,
        max_size_mb: 256,
      },
    ]);

    expect(JSON.parse(raw)).toEqual([
      {
        component: "metadata",
        level: "info",
        retention_days: 1,
        max_rows: 100000,
        max_size_mb: 128,
      },
    ]);
  });
});

describe("logRetentionPolicy bucket rows", () => {
  it("gives every parsed row a stable id", () => {
    const { rows, error } = bucketRowsFromRaw(
      JSON.stringify([
        { component: "metadata", level: "info", retention_days: 1, max_rows: 10, max_size_mb: 8 },
        { component: "scanner", level: "warn", retention_days: 7, max_rows: 20, max_size_mb: 16 },
      ]),
    );

    expect(error).toBe("");
    expect(rows.map((row) => row.id)).toEqual(["1", "2"]);
    expect(rows[1]?.component).toBe("scanner");
  });

  it("falls back to the recommended rows when the stored JSON is unreadable", () => {
    const { rows, error } = bucketRowsFromRaw("{not json");

    expect(error).not.toBe("");
    expect(rows).toEqual(recommendedBucketRows());
  });

  it("numbers a new row after the highest id still present", () => {
    let rows = bucketRowsFromRaw("").rows;
    rows = appendBucketRow(rows);
    rows = appendBucketRow(rows);
    expect(rows.map((row) => row.id)).toEqual(["1", "2"]);

    rows = updateBucketRow(rows, "1", "component", "scanner");
    rows = updateBucketRow(rows, "1", "max_rows", "-4");
    expect(rows[0]?.component).toBe("scanner");
    expect(rows[0]?.max_rows).toBe(0);

    // Ids are max-of-remaining + 1, not a monotonic counter: removing the last
    // row frees its id for the next one. That is safe because an id only has to
    // be unique among the rows currently on screen — it is a React key and the
    // handle the editor's own update/remove calls use, and it is stripped before
    // the rows are serialized.
    rows = removeBucketRow(rows, "2");
    rows = appendBucketRow(rows);
    expect(rows.map((row) => row.id)).toEqual(["1", "2"]);
  });

  it("serializes rows without their editor ids", () => {
    const rows = updateBucketRow(appendBucketRow([]), "1", "component", "metadata");

    expect(JSON.parse(serializeBucketRows(rows))).toEqual([
      {
        component: "metadata",
        level: "info",
        retention_days: 1,
        max_rows: 100000,
        max_size_mb: 128,
      },
    ]);
  });
});
