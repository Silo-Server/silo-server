import { useCallback, useMemo, useState } from "react";
import { Link } from "react-router";

import { BarList, type BarListItem } from "@/components/admin/dashboard/charts";
import { WidgetViewToggle } from "@/components/admin/dashboard/WidgetViewToggle";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useAdminInsightsDevices } from "@/hooks/queries/admin/insights";
import { useT } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";
import { formatCodecLabel } from "@/lib/mediaFormat";
import { cn } from "@/lib/utils";

import { getAdaptationReasonByCode } from "../adminActivityReasons";
import {
  buildHistoryDrilldownHref,
  buildInsightsWindowQueryParams,
  formatHwAccelLabel,
  formatRatePercentNumber,
  formatShortCalendarDate,
  type InsightsFilterState,
} from "./insightsHelpers";
import { InsightKpiCard, InsightNoticeBanner, InsightSectionError } from "./insightsShared";

type SampleThreshold = "5" | "1";
const EM_DASH = "—";

function matrixCellClass(directRate: number): string {
  if (directRate >= 0.85) {
    return "border-success/25 bg-success/15 text-success-label hover:bg-success/25";
  }
  if (directRate >= 0.5) {
    return "border-warning/25 bg-warning/15 text-warning-label hover:bg-warning/25";
  }
  return "border-destructive/25 bg-destructive/15 text-destructive-label hover:bg-destructive/25";
}

export function DevicesTab({ filters }: { filters: InsightsFilterState }) {
  const t = useT();
  const [minPlays, setMinPlays] = useState<SampleThreshold>("5");

  const queryParams = useMemo(
    () => ({
      ...buildInsightsWindowQueryParams(filters),
      min_plays: Number(minPlays),
    }),
    [filters, minPlays],
  );
  const query = useAdminInsightsDevices(queryParams);
  const data = query.data;

  const formatPlayCount = useCallback(
    (plays: number) =>
      t("web.admin.insights.playCount", {
        count: plays,
        count_text: plays.toLocaleString(formatLocale()),
      }),
    [t],
  );

  const reasonItems = useMemo<BarListItem[]>(() => {
    if (!data) return [];
    return data.adaptation_reasons.map((item) => {
      const resolved = getAdaptationReasonByCode(item.reason);
      return {
        id: item.reason,
        label: resolved?.label ?? item.reason,
        value: item.plays,
        secondary: t("web.admin.insights.percent", {
          value: formatRatePercentNumber(item.share_of_adapted),
        }),
        to: buildHistoryDrilldownHref({
          adaptationReason: item.reason,
          libraryId: filters.libraryId,
          userId: filters.userId,
          startedAfter: data.from,
          startedBefore: data.to,
        }),
      };
    });
  }, [data, filters.libraryId, filters.userId, t]);

  const topTranscoderItems = useMemo<BarListItem[]>(() => {
    if (!data) return [];
    return data.top_transcoding_accounts.map((acc) => {
      const topReason = acc.top_reason ? getAdaptationReasonByCode(acc.top_reason)?.label : null;
      const share = t("web.admin.insights.devices.topTranscoders.transcodeShare", {
        transcodes: acc.transcode_plays.toLocaleString(formatLocale()),
        total: acc.total_plays.toLocaleString(formatLocale()),
      });
      return {
        id: acc.user_id,
        label: acc.username,
        value: acc.transcode_plays,
        secondary: topReason ? `${share} · ${topReason}` : share,
        to: buildHistoryDrilldownHref({
          userId: acc.user_id,
          playMethod: "transcode",
          libraryId: filters.libraryId,
          startedAfter: data.from,
          startedBefore: data.to,
        }),
      };
    });
  }, [data, filters.libraryId, t]);

  const hwAccelItems = useMemo<BarListItem[]>(() => {
    if (!data) return [];
    const swLabel = t("web.admin.insights.devices.hwAccel.software");
    const unknownLabel = t("web.admin.insights.devices.hwAccel.unknown");
    return data.hw_accel_breakdown.map((entry) => ({
      id: entry.key,
      label: formatHwAccelLabel(entry.key, swLabel, unknownLabel),
      value: entry.plays,
    }));
  }, [data, t]);

  const burnInItems = useMemo<BarListItem[]>(() => {
    if (!data) return [];
    return data.subtitle_burn_in_codecs.map((entry) => ({
      id: entry.key,
      label: formatCodecLabel(entry.key) || entry.key.toUpperCase(),
      value: entry.plays,
    }));
  }, [data]);

  if (query.isLoading) {
    return (
      <div className="space-y-5">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-24 rounded-2xl" />
          ))}
        </div>
        <Skeleton className="h-72 rounded-2xl" />
        <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
          <Skeleton className="h-56 rounded-2xl" />
          <Skeleton className="h-56 rounded-2xl" />
        </div>
      </div>
    );
  }

  if (query.error || !data) {
    return (
      <InsightSectionError
        error={query.error}
        onRetry={() => {
          void query.refetch();
        }}
      />
    );
  }

  const { summary, coverage } = data;
  const knownEncoderPlays = summary.hw_transcode_plays + summary.sw_transcode_plays;
  const burnInTranscodeRate =
    summary.transcode_plays > 0 ? summary.subtitle_burn_in_plays / summary.transcode_plays : 0;
  const burnInHistoryHref = buildHistoryDrilldownHref({
    subtitleBurnIn: true,
    libraryId: filters.libraryId,
    userId: filters.userId,
    startedAfter: data.from,
    startedBefore: data.to,
  });

  return (
    <div className="space-y-5">
      {coverage.total_plays > 0 && coverage.route_facts_plays === 0 ? (
        <InsightNoticeBanner message={t("web.admin.insights.coverage.routeFactsNone")} />
      ) : coverage.total_plays > 0 && coverage.route_facts_plays < coverage.total_plays ? (
        <InsightNoticeBanner
          message={t("web.admin.insights.coverage.routeFactsPartial", {
            recorded: coverage.route_facts_plays.toLocaleString(formatLocale()),
            total: coverage.total_plays.toLocaleString(formatLocale()),
            since: formatShortCalendarDate(coverage.oldest_route_facts_at),
          })}
        />
      ) : null}

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <InsightKpiCard
          label={t("web.admin.insights.devices.kpi.directPlayRate")}
          value={t("web.admin.insights.percent", {
            value: formatRatePercentNumber(summary.direct_rate),
          })}
          subtext={t("web.admin.insights.devices.kpi.directPlayDetail", {
            direct: summary.direct_plays.toLocaleString(formatLocale()),
            remux: summary.remux_plays.toLocaleString(formatLocale()),
            transcode: summary.transcode_plays.toLocaleString(formatLocale()),
          })}
          accent="success"
        />
        <InsightKpiCard
          label={t("web.admin.insights.devices.kpi.hwSplit")}
          value={
            knownEncoderPlays > 0
              ? t("web.admin.insights.percent", {
                  value: formatRatePercentNumber(summary.hw_transcode_plays / knownEncoderPlays),
                })
              : EM_DASH
          }
          subtext={
            knownEncoderPlays > 0
              ? t("web.admin.insights.devices.kpi.hwSplitDetail", {
                  hw: summary.hw_transcode_plays.toLocaleString(formatLocale()),
                  sw: summary.sw_transcode_plays.toLocaleString(formatLocale()),
                })
              : t("web.admin.insights.devices.kpi.hwSplitNone")
          }
          accent="info"
        />
        <InsightKpiCard
          label={t("web.admin.insights.devices.kpi.subtitleBurnIn")}
          value={summary.subtitle_burn_in_plays.toLocaleString(formatLocale())}
          subtext={t("web.admin.insights.devices.kpi.subtitleBurnInDetail", {
            percent: formatRatePercentNumber(burnInTranscodeRate),
          })}
          accent={summary.subtitle_burn_in_plays > 0 ? "warning" : "default"}
        />
        <InsightKpiCard
          label={t("web.admin.insights.devices.kpi.hdrToneMap")}
          value={summary.hdr_tonemap_plays.toLocaleString(formatLocale())}
          subtext={t("web.admin.insights.devices.kpi.hdrToneMapDetail", {
            hw: summary.hdr_tonemap_hw_plays.toLocaleString(formatLocale()),
            sw: summary.hdr_tonemap_sw_plays.toLocaleString(formatLocale()),
          })}
        />
      </div>

      {/* Client x Source Video Codec Direct-Play Matrix */}
      <Card className="surface-panel rounded-2xl border-0">
        <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-2 space-y-0">
          <div className="space-y-0.5">
            <CardTitle className="text-sm font-bold">
              {t("web.admin.insights.devices.matrix.title")}
            </CardTitle>
            <p className="text-muted-foreground text-xs">
              {t("web.admin.insights.devices.matrix.subtitle")}
            </p>
          </div>
          <WidgetViewToggle
            value={minPlays}
            options={[
              {
                value: "5",
                label: t("web.admin.insights.devices.matrix.minPlays"),
                ariaLabel: t("web.admin.insights.devices.matrix.minPlays"),
              },
              {
                value: "1",
                label: t("web.admin.insights.devices.matrix.allSamples"),
                ariaLabel: t("web.admin.insights.devices.matrix.allSamples"),
              },
            ]}
            onChange={setMinPlays}
          />
        </CardHeader>
        <CardContent>
          {data.matrix.length === 0 || data.codecs.length === 0 ? (
            <div className="text-muted-foreground py-10 text-center text-sm">
              {t("web.admin.insights.devices.matrix.empty")}
            </div>
          ) : (
            <div className="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("web.admin.insights.devices.matrix.clientColumn")}</TableHead>
                    {data.codecs.map((codec) => (
                      <TableHead key={codec} className="text-center">
                        {formatCodecLabel(codec) || codec.toUpperCase()}
                      </TableHead>
                    ))}
                    <TableHead className="text-end">
                      {t("web.admin.insights.devices.matrix.totalColumn")}
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.matrix.map((row) => {
                    const cellByCodec = new Map(row.cells.map((c) => [c.source_video_codec, c]));
                    const clientLabel =
                      row.client_name || t("web.admin.insights.devices.unknownClient");
                    return (
                      <TableRow key={`${row.client_name}:${row.device_platform}`}>
                        <TableCell>
                          <div className="space-y-0.5">
                            <div className="flex items-center gap-1.5 font-medium">
                              <span>{clientLabel}</span>
                              {row.is_jellyfin_client ? (
                                <Badge
                                  variant="secondary"
                                  title={t("web.admin.history.detail.jellyfinBadgeTitle")}
                                  className="px-1 py-0 text-[10px] font-bold"
                                >
                                  {t("web.admin.history.detail.jellyfinBadge")}
                                </Badge>
                              ) : null}
                            </div>
                            <div className="text-muted-foreground text-[11px]">
                              {row.device_platform}
                            </div>
                          </div>
                        </TableCell>
                        {data.codecs.map((codec) => {
                          const cell = cellByCodec.get(codec);
                          if (!cell) {
                            return (
                              <TableCell
                                key={codec}
                                className="text-muted-foreground text-center text-xs"
                              >
                                {EM_DASH}
                              </TableCell>
                            );
                          }
                          const codecLabel = formatCodecLabel(codec) || codec.toUpperCase();
                          const rateStr = formatRatePercentNumber(cell.direct_rate);
                          const tooltip = t("web.admin.insights.devices.matrix.cellTooltip", {
                            client: clientLabel,
                            codec: codecLabel,
                            direct_rate: rateStr,
                            plays: formatPlayCount(cell.plays),
                            transcodes: cell.transcode_plays.toLocaleString(formatLocale()),
                          });
                          const drillHref = buildHistoryDrilldownHref({
                            clientName: row.client_name || "unknown",
                            devicePlatform: row.device_platform || "unknown",
                            sourceVideoCodec: codec,
                            libraryId: filters.libraryId,
                            userId: filters.userId,
                            startedAfter: data.from,
                            startedBefore: data.to,
                          });
                          return (
                            <TableCell key={codec} className="p-1.5 text-center">
                              <Link
                                to={drillHref}
                                title={tooltip}
                                aria-label={tooltip}
                                className={cn(
                                  "inline-flex min-w-[4.5rem] flex-col items-center justify-center rounded-lg border px-2 py-1 text-xs transition-colors",
                                  matrixCellClass(cell.direct_rate),
                                )}
                              >
                                <span className="font-bold tabular-nums">
                                  {t("web.admin.insights.percent", { value: rateStr })}
                                </span>
                                <span className="text-[10px] tabular-nums opacity-80">
                                  {cell.plays.toLocaleString(formatLocale())}
                                </span>
                              </Link>
                            </TableCell>
                          );
                        })}
                        <TableCell className="text-end tabular-nums">
                          <div className="font-semibold">
                            {t("web.admin.insights.percent", {
                              value: formatRatePercentNumber(row.direct_rate),
                            })}
                          </div>
                          <div className="text-muted-foreground text-[11px]">
                            {row.total_plays.toLocaleString(formatLocale())}
                          </div>
                        </TableCell>
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>

      {/* Adaptation Reasons + Top Transcoding Accounts */}
      <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
        <Card className="surface-panel rounded-2xl border-0">
          <CardHeader className="space-y-0.5">
            <CardTitle className="text-sm font-bold">
              {t("web.admin.insights.devices.reasons.title")}
            </CardTitle>
            <p className="text-muted-foreground text-xs">
              {t("web.admin.insights.devices.reasons.subtitle")}
            </p>
          </CardHeader>
          <CardContent>
            <BarList
              items={reasonItems}
              seriesIndex={2}
              formatValue={formatPlayCount}
              emptyLabel={t("web.admin.insights.devices.reasons.empty")}
            />
          </CardContent>
        </Card>

        <Card className="surface-panel rounded-2xl border-0">
          <CardHeader>
            <CardTitle className="text-sm font-bold">
              {t("web.admin.insights.devices.topTranscoders.title")}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <BarList
              items={topTranscoderItems}
              seriesIndex={0}
              formatValue={formatPlayCount}
              emptyLabel={t("web.admin.insights.devices.topTranscoders.empty")}
            />
          </CardContent>
        </Card>
      </div>

      {/* Encoder acceleration + Burn-in subtitle codecs */}
      <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
        <Card className="surface-panel rounded-2xl border-0">
          <CardHeader>
            <CardTitle className="text-sm font-bold">
              {t("web.admin.insights.devices.hwAccel.title")}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <BarList
              items={hwAccelItems}
              seriesIndex={1}
              formatValue={formatPlayCount}
              emptyLabel={t("web.admin.insights.devices.hwAccel.empty")}
            />
          </CardContent>
        </Card>

        <Card className="surface-panel rounded-2xl border-0">
          <CardHeader className="flex flex-row items-center justify-between space-y-0">
            <CardTitle className="text-sm font-bold">
              {t("web.admin.insights.devices.burnInCodecs.title")}
            </CardTitle>
            {burnInItems.length > 0 ? (
              <Link
                to={burnInHistoryHref}
                className="text-primary text-xs font-medium hover:underline"
              >
                {t("web.admin.insights.openHistory")}
              </Link>
            ) : null}
          </CardHeader>
          <CardContent>
            <BarList
              items={burnInItems}
              seriesIndex={3}
              formatValue={formatPlayCount}
              emptyLabel={t("web.admin.insights.devices.burnInCodecs.empty")}
            />
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
