import type { AdminTimeseries, AdminTimeseriesEgressSource } from "@/api/types";
import { chartSeriesColor } from "../charts";
import type { LineChartPoint } from "../charts";
import { buildTimeseriesPoints } from "./timeseriesSeries";

/** One stacked band of the egress-by-node view, in Mbps. */
export interface EgressBand {
  key: string;
  label: string;
  color: string;
  points: LineChartPoint[];
}

/**
 * How many sources get a band of their own. The rest share "Other", so the
 * card's legend stays one line and its height stays fixed at any node count.
 */
const MAX_NAMED_BANDS = 4;

/**
 * `chartSeriesColor` indexes for named bands. Index 1 (`--chart-2`, teal) is
 * skipped because the total view already uses it for Downloads, and the same
 * color must not mean two things on one widget.
 */
const BAND_COLOR_SLOTS = [0, 2, 3, 4] as const;

const OTHER_KEY = "other";
const OTHER_COLOR = "var(--muted-foreground)";

/**
 * Splits the egress series into stacked bands, bottom of the stack first, or
 * returns null when no stream node served egress in the window: an install
 * without proxies has nothing to split, so it keeps the total view only.
 *
 * The sources that served the most over the window (`egress_bytes`) get a
 * band each; the per-point split only breaks ties, since it shows each
 * bucket's peak minute and nothing else. A source's color comes from the
 * source itself (the API, or the node's id), not from its rank or from which
 * other sources are listed, so a node keeps its color as nodes go idle or
 * another one enters the busiest four. Four colors cannot keep more than four
 * sources apart, so when two kept sources want the same color, the API and
 * then the lower node id keep it and the other takes the next free one.
 * "Other" carries every remaining source plus egress no source accounts for,
 * so the stack's top edge is always the point's total.
 */
export function buildEgressBands(series: AdminTimeseries | undefined): EgressBand[] | null {
  const sources = series?.egress_sources ?? [];
  if (!series || !sources.some((source) => source.kind === "node")) {
    return null;
  }

  const volume = new Map<string, number>();
  for (const point of series.points) {
    for (const [key, kbps] of Object.entries(point.egress_by_source ?? {})) {
      volume.set(key, (volume.get(key) ?? 0) + kbps);
    }
  }
  const kept = new Set(
    [...sources]
      .sort(
        (a, b) =>
          (b.egress_bytes ?? 0) - (a.egress_bytes ?? 0) ||
          (volume.get(b.key) ?? 0) - (volume.get(a.key) ?? 0) ||
          a.key.localeCompare(b.key),
      )
      .slice(0, MAX_NAMED_BANDS)
      .map((source) => source.key),
  );

  const keptSources = sources.filter((source) => kept.has(source.key));
  const colors = assignColors(keptSources);
  const labels = bandLabels(keptSources);
  const named = keptSources.map((source) => ({
    key: source.key,
    label: labels.get(source.key) ?? source.name,
    color: colors.get(source.key) ?? chartSeriesColor(BAND_COLOR_SLOTS[0]),
    points: buildTimeseriesPoints(
      series,
      (point) => (point.egress_by_source?.[source.key] ?? 0) / 1_000,
    ),
  }));

  const other = buildTimeseriesPoints(series, (point) => {
    const keptKbps = Object.entries(point.egress_by_source ?? {})
      .filter(([key]) => kept.has(key))
      .reduce((sum, [, kbps]) => sum + kbps, 0);
    return Math.max(point.egress_kbps - keptKbps, 0) / 1_000;
  });
  const hasOther = other.some((point) => (point.value ?? 0) > 0);

  return hasOther
    ? [{ key: OTHER_KEY, label: "Other", color: OTHER_COLOR, points: other }, ...named]
    : named;
}

/**
 * Legend labels for the kept sources. Node names need not be unique, so a
 * name two kept nodes share gets each node's id appended.
 */
function bandLabels(sources: readonly AdminTimeseriesEgressSource[]): Map<string, string> {
  const uses = new Map<string, number>();
  for (const source of sources) {
    uses.set(source.name, (uses.get(source.name) ?? 0) + 1);
  }
  return new Map(
    sources.map((source) => [
      source.key,
      (uses.get(source.name) ?? 0) > 1 && source.node_id !== undefined
        ? `${source.name} (#${source.node_id})`
        : source.name,
    ]),
  );
}

/** The color slot index a source asks for: the API first, a node by its id. */
function preferredSlot(source: AdminTimeseriesEgressSource): number {
  if (source.kind === "api") {
    return 0;
  }
  const id = Number(source.node_id);
  return Number.isInteger(id) && id >= 0 ? id % BAND_COLOR_SLOTS.length : 0;
}

/**
 * Colors for the kept sources, claimed in identity order (the API, then nodes
 * by ascending id) so a collision always resolves the same way.
 */
function assignColors(sources: readonly AdminTimeseriesEgressSource[]): Map<string, string> {
  const byIdentity = [...sources].sort(
    (a, b) =>
      Number(b.kind === "api") - Number(a.kind === "api") ||
      Number(a.node_id ?? 0) - Number(b.node_id ?? 0),
  );
  const taken = new Set<number>();
  const colors = new Map<string, string>();
  for (const source of byIdentity) {
    const preferred = preferredSlot(source);
    for (let offset = 0; offset < BAND_COLOR_SLOTS.length; offset += 1) {
      const index = (preferred + offset) % BAND_COLOR_SLOTS.length;
      if (!taken.has(index)) {
        taken.add(index);
        colors.set(source.key, chartSeriesColor(BAND_COLOR_SLOTS[index] ?? 0));
        break;
      }
    }
  }
  return colors;
}
