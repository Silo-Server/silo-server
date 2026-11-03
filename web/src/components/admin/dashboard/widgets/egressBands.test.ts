import { describe, expect, it } from "vitest";

import type { AdminTimeseries, AdminTimeseriesEgressSource } from "@/api/types";
import { buildEgressBands } from "./egressBands";

const HOUR_MS = 3_600_000;

function node(id: number, name: string, egressBytes = 0): AdminTimeseriesEgressSource {
  return { key: `node:${id}`, kind: "node", node_id: String(id), name, egress_bytes: egressBytes };
}

function api(egressBytes = 0): AdminTimeseriesEgressSource {
  return { key: "api", kind: "api", name: "API server", egress_bytes: egressBytes };
}

function series(
  sources: AdminTimeseriesEgressSource[],
  egressKbps: number,
  bySource: Record<string, number>,
): AdminTimeseries {
  const to = Date.parse("2026-10-29T12:00:00Z");
  return {
    resolution_seconds: 60,
    from: new Date(to - HOUR_MS).toISOString(),
    to: new Date(to).toISOString(),
    oldest_sample_at: new Date(to - 2 * HOUR_MS).toISOString(),
    egress_sources: sources,
    points: [
      {
        t: new Date(to - 30 * 60_000).toISOString(),
        streams: 0,
        direct: 0,
        remux: 0,
        transcode: 0,
        egress_kbps: egressKbps,
        egress_by_source: bySource,
      },
    ],
  };
}

function plotted(points: { value: number | null }[]): number[] {
  return points.flatMap((point) => (point.value === null ? [] : [point.value]));
}

describe("buildEgressBands", () => {
  it("is null when no stream node served egress", () => {
    expect(buildEgressBands(series([api()], 3_000, { api: 3_000 }))).toBeNull();
    expect(buildEgressBands(series([], 3_000, {}))).toBeNull();
  });

  it("keeps the four busiest sources and folds the rest, plus unattributed egress, into Other", () => {
    const sources: AdminTimeseriesEgressSource[] = [
      api(),
      node(1, "proxy-ams"),
      node(2, "proxy-fra"),
      node(3, "proxy-lon"),
      node(4, "proxy-nyc"),
      node(5, "proxy-sgp"),
    ];
    // 15,500 kbps attributed of 16,000: 500 kbps belongs to no listed source.
    const bands = buildEgressBands(
      series(sources, 16_000, {
        api: 1_000,
        "node:1": 5_000,
        "node:2": 4_000,
        "node:3": 3_000,
        "node:4": 2_000,
        "node:5": 500,
      }),
    );

    // Bottom of the stack first: Other, then the kept sources in the server's
    // source order. Each color comes from the source's place in the full list.
    expect(bands?.map((band) => [band.label, band.color, plotted(band.points)])).toEqual([
      ["Other", "var(--muted-foreground)", [2]],
      ["proxy-ams", "var(--chart-3)", [5]],
      ["proxy-fra", "var(--chart-4)", [4]],
      ["proxy-lon", "var(--chart-5)", [3]],
      ["proxy-nyc", "var(--chart-1)", [2]],
    ]);
  });

  it("keeps a node's color when other sources come and go", () => {
    const colors = (sources: AdminTimeseriesEgressSource[], bySource: Record<string, number>) =>
      Object.fromEntries(
        (buildEgressBands(series(sources, 10_000, bySource)) ?? []).map((band) => [
          band.label,
          band.color,
        ]),
      );

    // The API drops out of the busiest four while proxy-nyc joins.
    const apiBusy = colors(
      [
        api(),
        node(1, "proxy-ams"),
        node(2, "proxy-fra"),
        node(3, "proxy-lon"),
        node(4, "proxy-nyc"),
      ],
      { api: 4_000, "node:1": 3_000, "node:2": 2_000, "node:3": 1_000 },
    );
    const nycBusy = colors(
      [
        api(),
        node(1, "proxy-ams"),
        node(2, "proxy-fra"),
        node(3, "proxy-lon"),
        node(4, "proxy-nyc"),
      ],
      { "node:4": 4_000, "node:1": 3_000, "node:2": 2_000, "node:3": 1_000 },
    );
    // A node that sorts first was idle in this window, so the server left it out.
    const amsIdle = colors([api(), node(2, "proxy-fra"), node(3, "proxy-lon")], {
      api: 4_000,
      "node:2": 2_000,
      "node:3": 1_000,
    });

    for (const name of ["proxy-fra", "proxy-lon"]) {
      expect(nycBusy[name]).toBe(apiBusy[name]);
      expect(amsIdle[name]).toBe(apiBusy[name]);
    }
  });

  it("ranks sources by what they served over the window, not by peak minutes", () => {
    // proxy-sgp never leads a bucket's peak minute but served the most overall.
    const bands = buildEgressBands(
      series(
        [
          api(1),
          node(1, "proxy-ams", 50),
          node(2, "proxy-fra", 40),
          node(3, "proxy-lon", 30),
          node(5, "proxy-sgp", 90),
        ],
        10_000,
        { api: 4_000, "node:1": 3_000, "node:2": 2_000, "node:3": 1_000 },
      ),
    );

    expect(bands?.map((band) => band.label)).toEqual([
      "Other",
      "proxy-ams",
      "proxy-fra",
      "proxy-lon",
      "proxy-sgp",
    ]);
  });

  it("tells apart kept nodes that share a name", () => {
    const bands = buildEgressBands(
      series([api(), node(1, "proxy"), node(2, "proxy")], 3_000, {
        api: 1_000,
        "node:1": 1_000,
        "node:2": 1_000,
      }),
    );

    expect(bands?.map((band) => band.label)).toEqual(["API server", "proxy (#1)", "proxy (#2)"]);
  });

  it("leaves Other out when the kept sources account for all egress", () => {
    const bands = buildEgressBands(
      series([api(), node(1, "proxy-ams")], 3_000, {
        api: 1_000,
        "node:1": 2_000,
      }),
    );

    expect(bands?.map((band) => band.label)).toEqual(["API server", "proxy-ams"]);
  });
});
