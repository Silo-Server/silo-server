import { useCallback, useMemo, useState } from "react";
import { Link } from "react-router";

import { BarList, type BarListItem } from "@/components/admin/dashboard/charts";
import { WidgetViewToggle } from "@/components/admin/dashboard/WidgetViewToggle";
import { JellyfinSessionPill } from "@/components/JellyfinSessionPill";
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
  formatInsightsCount,
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
  const [minSessions, setMinSessions] = useState<SampleThreshold>("5");

  const queryParams = useMemo(
    () => ({
      ...buildInsightsWindowQueryParams(filters),
      min_sessions: Number(minSessions),
    }),
    [filters, minSessions],
  );
  const query = useAdminInsightsDevices(queryParams);
  const data = query.data;

  const formatSessionCount = useCallback(
    (sessions: number) => formatInsightsCount(t, "web.admin.insights.sessionCount", sessions),
    [t],
  );

  const reasonItems = useMemo<BarListItem[]>(() => {
    if (!data) return [];
    return data.adaptation_reasons.map((item) => {
      const resolved = getAdaptationReasonByCode(item.reason);
      return {
        id: item.reason,
        label: resolved?.label ?? item.reason,
        value: item.sessions,
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
    const unknownClient = t("web.admin.insights.devices.unknownClient");
    // Ranked on the server without the matrix's sample threshold, so a client
    // with only a few sessions still shows when it transcodes.
    return data.top_transcoders.map((row) => {
      const clientLabel = row.client_name || unknownClient;
      const label = row.device_platform ? `${clientLabel} · ${row.device_platform}` : clientLabel;
      return {
        id: `${row.client_name}:${row.device_platform}`,
        label,
        value: row.transcode_sessions,
        secondary: t("web.admin.insights.devices.topTranscoders.transcodeShare", {
          transcodes: row.transcode_sessions.toLocaleString(formatLocale()),
          total: row.total_sessions.toLocaleString(formatLocale()),
          total_count: row.total_sessions,
        }),
        to: buildHistoryDrilldownHref({
          clientName: row.client_name || "unknown",
          devicePlatform: row.device_platform || "unknown",
          playMethod: "transcode",
          libraryId: filters.libraryId,
          userId: filters.userId,
          startedAfter: data.from,
          startedBefore: data.to,
        }),
      };
    });
  }, [data, filters.libraryId, filters.userId, t]);

  const hwAccelItems = useMemo<BarListItem[]>(() => {
    if (!data) return [];
    const swLabel = t("web.admin.insights.devices.hwAccel.software");
    const unknownLabel = t("web.admin.insights.devices.hwAccel.unknown");
    return data.hw_accel_breakdown.map((entry) => ({
      id: entry.key,
      label: formatHwAccelLabel(entry.key, swLabel, unknownLabel),
      value: entry.sessions,
    }));
  }, [data, t]);

  const burnInItems = useMemo<BarListItem[]>(() => {
    if (!data) return [];
    return data.subtitle_burn_in_codecs.map((entry) => ({
      id: entry.key,
      label: formatCodecLabel(entry.key) || entry.key.toUpperCase(),
      value: entry.sessions,
    }));
  }, [data]);

  const resolutionLabel = useCallback(
    (key: string) => {
      switch (key) {
        case "4320p":
          return t("web.admin.insights.devices.resolution.fuhd");
        case "2160p":
          return t("web.admin.insights.devices.resolution.uhd");
        case "480p":
          return t("web.admin.insights.devices.resolution.sd");
        case "1080p":
        case "720p":
          return key;
        default:
          return t("web.admin.insights.devices.resolution.unknown");
      }
    },
    [t],
  );

  const resolutionItems = useCallback(
    (entries: readonly { key: string; sessions: number }[]): BarListItem[] =>
      entries.map((entry) => ({
        id: entry.key,
        label: resolutionLabel(entry.key),
        value: entry.sessions,
      })),
    [resolutionLabel],
  );

  const downscaleItems = useMemo<BarListItem[]>(
    () =>
      (data?.resolution_changes ?? []).map((change) => ({
        id: `${change.from}:${change.to}`,
        label: t("web.admin.insights.devices.resolution.change", {
          from: resolutionLabel(change.from),
          to: resolutionLabel(change.to),
        }),
        value: change.sessions,
      })),
    [data?.resolution_changes, resolutionLabel, t],
  );

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
  const knownEncoderSessions = summary.hw_transcode_sessions + summary.sw_transcode_sessions;
  const burnInTranscodeRate =
    summary.transcode_sessions > 0
      ? summary.subtitle_burn_in_sessions / summary.transcode_sessions
      : 0;
  const burnInHistoryHref = buildHistoryDrilldownHref({
    subtitleBurnIn: true,
    libraryId: filters.libraryId,
    userId: filters.userId,
    startedAfter: data.from,
    startedBefore: data.to,
  });

  return (
    <div className="space-y-5">
      {coverage.total_sessions > 0 && coverage.route_facts_sessions === 0 ? (
        <InsightNoticeBanner message={t("web.admin.insights.coverage.routeFactsNone")} />
      ) : coverage.total_sessions > 0 && coverage.route_facts_sessions < coverage.total_sessions ? (
        <InsightNoticeBanner
          message={t("web.admin.insights.coverage.routeFactsPartial", {
            recorded: coverage.route_facts_sessions.toLocaleString(formatLocale()),
            total: coverage.total_sessions.toLocaleString(formatLocale()),
            total_count: coverage.total_sessions,
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
            direct: summary.direct_sessions.toLocaleString(formatLocale()),
            remux: summary.remux_sessions.toLocaleString(formatLocale()),
            transcode: summary.transcode_sessions.toLocaleString(formatLocale()),
          })}
          accent="success"
        />
        <InsightKpiCard
          label={t("web.admin.insights.devices.kpi.hwSplit")}
          value={
            knownEncoderSessions > 0
              ? t("web.admin.insights.percent", {
                  value: formatRatePercentNumber(
                    summary.hw_transcode_sessions / knownEncoderSessions,
                  ),
                })
              : EM_DASH
          }
          subtext={
            knownEncoderSessions > 0
              ? t("web.admin.insights.devices.kpi.hwSplitDetail", {
                  hw: summary.hw_transcode_sessions.toLocaleString(formatLocale()),
                  sw: summary.sw_transcode_sessions.toLocaleString(formatLocale()),
                })
              : t("web.admin.insights.devices.kpi.hwSplitNone")
          }
          accent="info"
        />
        <InsightKpiCard
          label={t("web.admin.insights.devices.kpi.subtitleBurnIn")}
          value={summary.subtitle_burn_in_sessions.toLocaleString(formatLocale())}
          subtext={t("web.admin.insights.devices.kpi.subtitleBurnInDetail", {
            percent: formatRatePercentNumber(burnInTranscodeRate),
          })}
          accent={summary.subtitle_burn_in_sessions > 0 ? "warning" : "default"}
        />
        <InsightKpiCard
          label={t("web.admin.insights.devices.kpi.hdrToneMap")}
          value={summary.hdr_tonemap_sessions.toLocaleString(formatLocale())}
          subtext={t("web.admin.insights.devices.kpi.hdrToneMapDetail", {
            hw: summary.hdr_tonemap_hw_sessions.toLocaleString(formatLocale()),
            sw: summary.hdr_tonemap_sw_sessions.toLocaleString(formatLocale()),
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
            value={minSessions}
            options={[
              {
                value: "5",
                label: t("web.admin.insights.devices.matrix.minSessions"),
                ariaLabel: t("web.admin.insights.devices.matrix.minSessions"),
              },
              {
                value: "1",
                label: t("web.admin.insights.devices.matrix.allSamples"),
                ariaLabel: t("web.admin.insights.devices.matrix.allSamples"),
              },
            ]}
            onChange={setMinSessions}
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
                              <JellyfinSessionPill session={row} />
                            </div>
                            <div className="text-muted-foreground text-[0.6875rem]">
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
                            sessions: formatSessionCount(cell.sessions),
                            transcodes: cell.transcode_sessions.toLocaleString(formatLocale()),
                            transcode_count: cell.transcode_sessions,
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
                                <span className="text-[0.625rem] tabular-nums opacity-80">
                                  {cell.sessions.toLocaleString(formatLocale())}
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
                          <div className="text-muted-foreground text-[0.6875rem]">
                            {row.total_sessions.toLocaleString(formatLocale())}
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
              formatValue={formatSessionCount}
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
              formatValue={formatSessionCount}
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
              formatValue={formatSessionCount}
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
              formatValue={formatSessionCount}
              emptyLabel={t("web.admin.insights.devices.burnInCodecs.empty")}
            />
          </CardContent>
        </Card>
      </div>

      {/* Source vs delivered resolution */}
      <Card className="surface-panel rounded-2xl border-0">
        <CardHeader className="space-y-0.5">
          <CardTitle className="text-sm font-bold">
            {t("web.admin.insights.devices.resolution.title")}
          </CardTitle>
          <p className="text-muted-foreground text-xs">
            {t("web.admin.insights.devices.resolution.subtitle")}
          </p>
        </CardHeader>
        <CardContent className="grid grid-cols-1 gap-5 md:grid-cols-3">
          <section className="space-y-1.5">
            <h3 className="text-muted-foreground text-xs font-semibold">
              {t("web.admin.insights.devices.resolution.source")}
            </h3>
            <BarList
              items={resolutionItems(data.source_resolutions)}
              seriesIndex={0}
              formatValue={formatSessionCount}
              emptyLabel={t("web.admin.insights.devices.resolution.empty")}
            />
          </section>
          <section className="space-y-1.5">
            <h3 className="text-muted-foreground text-xs font-semibold">
              {t("web.admin.insights.devices.resolution.delivered")}
            </h3>
            <BarList
              items={resolutionItems(data.delivered_resolutions)}
              seriesIndex={1}
              formatValue={formatSessionCount}
              emptyLabel={t("web.admin.insights.devices.resolution.empty")}
            />
          </section>
          <section className="space-y-1.5">
            <h3 className="text-muted-foreground text-xs font-semibold">
              {t("web.admin.insights.devices.resolution.downscaled")}
            </h3>
            <BarList
              items={downscaleItems}
              seriesIndex={2}
              formatValue={formatSessionCount}
              emptyLabel={t("web.admin.insights.devices.resolution.downscaledEmpty")}
            />
          </section>
        </CardContent>
      </Card>
    </div>
  );
}
