import { useMemo } from "react";

import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { useAdminTimeseries } from "@/hooks/queries/admin/dashboardInsights";
import { formatDataTotal, formatMbps, formatMbpsValue } from "../format";
import {
  formatRangeTimestamp,
  rangeEdgeLabels,
  rangeHours,
  rangePhrase,
  rangeTitle,
} from "../range";
import { useWidgetRange } from "../widgetChrome";
import { WidgetHeader } from "../WidgetHeader";
import { WidgetRangePicker } from "../WidgetRangePicker";
import { buildEgressBands } from "./egressBands";
import { TimeseriesChartBody } from "./timeseriesChart";
import { buildTimeseriesPoints } from "./timeseriesSeries";
import { useAppLanguage, useT } from "@/i18n";

/**
 * Egress the deployment served over the chosen window, in Mbps.
 *
 * The sampler mixes two sources into one number: the rolling average each
 * stream node reports, and the exact bytes each API process served itself. A
 * node-less single-server install charts the second alone. Wide windows are
 * bucketed server-side to the peak minute in each bucket, so "Peak" means the
 * same thing at every range.
 *
 * The main line is always the total. When the window contains measured
 * download traffic, the download subset is drawn as a second line under it —
 * never as a derived "playback" line: past the two-hour resolution the
 * server's total and download maxima are peak-preserved independently, so
 * they can come from different minutes and their difference is not any
 * minute's playback rate.
 *
 * Once a stream node has served egress in the window, a Total/Nodes switch
 * splits the same total into stacked bands per node (see buildEgressBands).
 * Installs without proxy nodes never see the switch.
 */
export function EgressWidget() {
  const t = useT();
  // buildEgressBands names the "Other" band, so the bands follow the language.
  const language = useAppLanguage();
  // The view is stored with the placement, so Nodes survives a reload.
  const { range, view, setView } = useWidgetRange();
  const query = useAdminTimeseries(rangeHours(range));
  const byNode = view === "nodes";
  // eslint-disable-next-line react-hooks/exhaustive-deps -- language re-runs the labels
  const bands = useMemo(() => buildEgressBands(query.data), [query.data, language]);
  const showBands = byNode && bands !== null;
  const hasDownloadTraffic = useMemo(
    () => (query.data?.points ?? []).some((point) => (point.download_egress_kbps ?? 0) > 0),
    [query.data],
  );
  const points = useMemo(
    () => buildTimeseriesPoints(query.data, (point) => point.egress_kbps / 1_000),
    [query.data],
  );
  const downloadPoints = useMemo(
    () =>
      hasDownloadTraffic
        ? buildTimeseriesPoints(query.data, (point) => (point.download_egress_kbps ?? 0) / 1_000)
        : [],
    [query.data, hasDownloadTraffic],
  );

  const peak = points.reduce((max, point) => Math.max(max, point.value ?? 0), 0);
  // Integrated server-side from every sampled minute; summing `points` here
  // would be wrong, since wide windows carry each bucket's peak minute.
  const totalBytes = query.data?.egress_bytes;

  return (
    <Card className="h-full">
      <WidgetHeader title={rangeTitle(t("web.admin.dashboard.widgets.egress.title"), range)}>
        {totalBytes != null ? (
          // Secondary to the peak: when the header is too narrow for both
          // beside the range picker, the total is dropped rather than
          // truncated. CardHeader is the `card-header` container.
          <span className="text-muted-foreground hidden text-[11px] whitespace-nowrap tabular-nums @md/card-header:inline">
            {t("web.admin.dashboard.widgets.egress.total", { value: formatDataTotal(totalBytes) })}
          </span>
        ) : null}
        <span className="text-muted-foreground text-[11px] whitespace-nowrap tabular-nums">
          {t("web.admin.dashboard.widgets.egress.peak", { value: formatMbps(peak) })}
        </span>
        {bands ? (
          <EgressViewToggle
            byNode={byNode}
            onChange={(next) => setView(next ? "nodes" : "total")}
          />
        ) : null}
        <WidgetRangePicker />
      </WidgetHeader>
      <CardContent className="flex min-h-0 flex-1 flex-col">
        <TimeseriesChartBody
          query={query}
          points={points}
          seriesLabel={t("web.admin.dashboard.widgets.egress.seriesLabel")}
          overlays={
            hasDownloadTraffic && !showBands
              ? [
                  {
                    label: t("web.admin.dashboard.widgets.egress.downloadsSeries"),
                    points: downloadPoints,
                    seriesIndex: 1,
                  },
                ]
              : undefined
          }
          stacks={showBands ? bands : undefined}
          ariaLabel={t("web.admin.dashboard.widgets.egress.chartLabel", {
            range: rangePhrase(range),
          })}
          errorMessage={t("web.admin.dashboard.widgets.egress.loadFailed")}
          emptyMessage={t("web.admin.dashboard.widgets.egress.empty")}
          formatValue={formatMbps}
          formatTick={formatMbpsValue}
          formatTimestamp={(time) => formatRangeTimestamp(range, time, { withTime: true })}
          edgeLabels={rangeEdgeLabels(range)}
          fill
        />
      </CardContent>
    </Card>
  );
}

/**
 * Total/Nodes switch, styled like the range picker beside it. Two toggle
 * buttons rather than one: each says which view it shows, and the pressed
 * state tells a screen reader which one is on.
 */
function EgressViewToggle({
  byNode,
  onChange,
}: {
  byNode: boolean;
  onChange: (byNode: boolean) => void;
}) {
  const t = useT();
  const options = [
    {
      byNode: false,
      label: t("web.admin.dashboard.widgets.egress.viewTotal"),
      ariaLabel: t("web.admin.dashboard.widgets.egress.viewTotalLabel"),
    },
    {
      byNode: true,
      label: t("web.admin.dashboard.widgets.egress.viewNodes"),
      ariaLabel: t("web.admin.dashboard.widgets.egress.viewNodesLabel"),
    },
  ];
  return (
    <div className="border-border/60 bg-muted/20 flex shrink-0 items-center gap-0.5 rounded-full border p-0.5">
      {options.map((option) => {
        const active = option.byNode === byNode;
        return (
          <button
            key={String(option.byNode)}
            type="button"
            aria-pressed={active}
            aria-label={option.ariaLabel}
            className={cn(
              "cursor-pointer rounded-full px-1.5 py-0.5 text-[10px] leading-none font-semibold transition-colors",
              "focus-visible:ring-ring focus-visible:ring-2 focus-visible:outline-none",
              active
                ? "border-primary/40 bg-primary/15 text-foreground"
                : "text-muted-foreground hover:text-foreground hover:bg-muted/60",
            )}
            onClick={() => onChange(option.byNode)}
          >
            {option.label}
          </button>
        );
      })}
    </div>
  );
}
