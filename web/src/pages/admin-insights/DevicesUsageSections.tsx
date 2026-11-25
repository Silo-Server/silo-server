import { useMemo } from "react";

import {
  BarList,
  ChartEmptyState,
  StackedColumnChart,
  type BarListItem,
  type StackedColumnBucket,
} from "@/components/admin/dashboard/charts";
import { JellyfinSessionPill } from "@/components/JellyfinSessionPill";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { AdminInsightsDevices } from "@/hooks/queries/admin/insights";
import { useT, type MessageKey, type Translate } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";
import { getLanguageName } from "@/lib/languageNames";

import {
  buildActivityBucketGrid,
  formatActivityBucketLabel,
  formatInsightsCount,
  formatRatePercentNumber,
  formatShortCalendarDate,
} from "./insightsHelpers";

type LabeledCount = AdminInsightsDevices["subtitles"]["languages"][number];

/**
 * The Devices tab's app and track breakdowns: Silo apps vs Jellyfin apps over
 * time, the app versions in use, and the subtitles and audio people play.
 * Subtitles and audio cover sessions with route facts only; the tab's coverage
 * notice already says how far back those go.
 */
export function DevicesUsageSections({ data }: { data: AdminInsightsDevices }) {
  return (
    <>
      <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
        <ProtocolShareCard data={data} />
        <ClientVersionsCard versions={data.client_versions} />
      </div>
      <SubtitleUsageCard usage={data.subtitles} />
      <AudioUsageCard usage={data.audio} />
    </>
  );
}

function useSessionCount() {
  const t = useT();
  return (sessions: number) => formatInsightsCount(t, "web.admin.insights.sessionCount", sessions);
}

function languageItems(
  counts: readonly LabeledCount[],
  unknownLabel: string,
  total: number,
  shareLabel: (percent: string) => string,
): BarListItem[] {
  return counts.map((count) => ({
    id: count.key,
    label: count.key === "unknown" ? unknownLabel : getLanguageName(count.key),
    value: count.sessions,
    secondary: shareLabel(formatRatePercentNumber(total > 0 ? count.sessions / total : 0)),
  }));
}

/** Items for keys with catalog labels; a key this build does not know reads as unknown. */
function labeledItems(
  t: Translate,
  counts: readonly LabeledCount[],
  labels: Record<string, MessageKey>,
): BarListItem[] {
  return counts.map((count) => {
    const key = labels[count.key];
    return {
      id: count.key,
      label: key ? t(key) : t("web.admin.insights.devices.hwAccel.unknown"),
      value: count.sessions,
    };
  });
}

function ProtocolShareCard({ data }: { data: AdminInsightsDevices }) {
  const t = useT();
  const buckets = useMemo<StackedColumnBucket[]>(() => {
    if (!data.protocol_buckets.some((b) => b.native_sessions + b.jellyfin_sessions > 0)) {
      return [];
    }
    const byDate = new Map(data.protocol_buckets.map((b) => [b.label_date, b]));
    const grid = buildActivityBucketGrid(data.from, data.to, data.tz, data.bucket_unit);
    const dates = grid.length > 0 ? grid : data.protocol_buckets.map((b) => b.label_date);
    return dates.map((date) => {
      const bucket = byDate.get(date);
      return {
        t: Date.parse(`${date}T00:00:00Z`),
        segments: [bucket?.native_sessions ?? 0, bucket?.jellyfin_sessions ?? 0],
      };
    });
  }, [data]);

  return (
    <Card className="surface-panel rounded-2xl border-0">
      <CardHeader className="space-y-0.5">
        <CardTitle className="text-sm font-bold">
          {t("web.admin.insights.devices.protocol.title")}
        </CardTitle>
        <p className="text-muted-foreground text-xs">
          {t("web.admin.insights.devices.protocol.subtitle")}
        </p>
      </CardHeader>
      <CardContent>
        {buckets.length === 0 ? (
          <ChartEmptyState message={t("web.admin.insights.devices.resolution.empty")} />
        ) : (
          <StackedColumnChart
            height={180}
            buckets={buckets}
            seriesLabels={[
              t("web.admin.insights.devices.protocol.silo"),
              t("web.admin.insights.devices.protocol.jellyfin"),
            ]}
            formatBucket={(ms) => formatActivityBucketLabel(ms, data.bucket_unit)}
            totalLabel={t("web.admin.insights.devices.protocol.total")}
            ariaLabel={t("web.admin.insights.devices.protocol.title")}
          />
        )}
      </CardContent>
    </Card>
  );
}

function ClientVersionsCard({ versions }: { versions: AdminInsightsDevices["client_versions"] }) {
  const t = useT();
  const sessionCount = useSessionCount();
  return (
    <Card className="surface-panel rounded-2xl border-0">
      <CardHeader className="space-y-0.5">
        <CardTitle className="text-sm font-bold">
          {t("web.admin.insights.devices.versions.title")}
        </CardTitle>
        <p className="text-muted-foreground text-xs">
          {t("web.admin.insights.devices.versions.subtitle")}
        </p>
      </CardHeader>
      <CardContent>
        {versions.length === 0 ? (
          <p className="text-muted-foreground py-6 text-center text-xs">
            {t("web.admin.insights.devices.versions.empty")}
          </p>
        ) : (
          <div className="max-h-72 overflow-y-auto">
            <table className="w-full text-xs">
              <thead className="text-muted-foreground bg-card sticky top-0 text-start">
                <tr>
                  <th className="py-1.5 pe-2 text-start font-medium">
                    {t("web.admin.insights.devices.matrix.clientColumn")}
                  </th>
                  <th className="px-2 py-1.5 text-start font-medium">
                    {t("web.admin.insights.devices.versions.versionColumn")}
                  </th>
                  <th className="px-2 py-1.5 text-end font-medium">
                    {t("web.admin.insights.devices.versions.sessionsColumn")}
                  </th>
                  <th className="py-1.5 ps-2 text-end font-medium">
                    {t("web.admin.insights.devices.versions.lastSeenColumn")}
                  </th>
                </tr>
              </thead>
              <tbody>
                {versions.map((row) => (
                  <tr
                    key={`${row.client_name}|${row.device_platform}|${row.client_version}`}
                    className="border-border/50 border-t"
                  >
                    <td className="py-1.5 pe-2">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="font-medium">
                          {row.client_name || t("web.admin.insights.devices.unknownClient")}
                        </span>
                        <JellyfinSessionPill
                          session={{ is_jellyfin_client: row.is_jellyfin_client }}
                        />
                      </div>
                      {row.device_platform ? (
                        <div className="text-muted-foreground">{row.device_platform}</div>
                      ) : null}
                    </td>
                    <td className="px-2 py-1.5 font-mono tabular-nums" dir="ltr">
                      {row.client_version}
                    </td>
                    <td className="px-2 py-1.5 text-end tabular-nums">
                      {sessionCount(row.sessions)}
                    </td>
                    <td className="text-muted-foreground py-1.5 ps-2 text-end tabular-nums">
                      {formatShortCalendarDate(row.last_seen_at)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

const SUBTITLE_DELIVERY_LABELS: Record<string, MessageKey> = {
  embedded: "web.admin.insights.devices.subtitles.delivery.embedded",
  sidecar: "web.admin.insights.devices.subtitles.delivery.sidecar",
  hls_rendition: "web.admin.insights.devices.subtitles.delivery.hlsRendition",
  burn_in: "web.admin.insights.devices.subtitles.delivery.burnIn",
};

function SubtitleUsageCard({ usage }: { usage: AdminInsightsDevices["subtitles"] }) {
  const t = useT();
  const sessionCount = useSessionCount();
  const share = (percent: string) => t("web.admin.insights.percent", { value: percent });
  return (
    <Card className="surface-panel rounded-2xl border-0">
      <CardHeader className="space-y-0.5">
        <CardTitle className="text-sm font-bold">
          {t("web.admin.insights.devices.subtitles.title")}
        </CardTitle>
        <p className="text-muted-foreground text-xs">
          {usage.sessions > 0
            ? t("web.admin.insights.devices.subtitles.summary", {
                percent: formatRatePercentNumber(usage.subtitled_sessions / usage.sessions),
                subtitled: usage.subtitled_sessions.toLocaleString(formatLocale()),
                total: usage.sessions.toLocaleString(formatLocale()),
                total_count: usage.sessions,
              })
            : t("web.admin.insights.devices.subtitles.noFacts")}
        </p>
      </CardHeader>
      <CardContent className="grid grid-cols-1 gap-5 md:grid-cols-2">
        <section className="space-y-1.5">
          <h3 className="text-muted-foreground text-xs font-semibold">
            {t("web.admin.insights.devices.languages")}
          </h3>
          <BarList
            items={languageItems(
              usage.languages,
              t("web.languageName.unknown"),
              usage.subtitled_sessions,
              share,
            )}
            seriesIndex={0}
            formatValue={sessionCount}
            emptyLabel={t("web.admin.insights.devices.subtitles.empty")}
          />
        </section>
        <section className="space-y-1.5">
          <h3 className="text-muted-foreground text-xs font-semibold">
            {t("web.admin.insights.devices.subtitles.deliveryTitle")}
          </h3>
          <BarList
            items={labeledItems(t, usage.deliveries, SUBTITLE_DELIVERY_LABELS)}
            seriesIndex={1}
            formatValue={sessionCount}
            emptyLabel={t("web.admin.insights.devices.subtitles.empty")}
          />
          {usage.forced_sessions + usage.hearing_impaired_sessions > 0 ? (
            <p className="text-muted-foreground pt-1 text-xs tabular-nums">
              {t("web.admin.insights.devices.subtitles.flags", {
                forced: usage.forced_sessions.toLocaleString(formatLocale()),
                hearing_impaired: usage.hearing_impaired_sessions.toLocaleString(formatLocale()),
              })}
            </p>
          ) : null}
        </section>
      </CardContent>
    </Card>
  );
}

const AUDIO_DECISION_LABELS: Record<string, MessageKey> = {
  direct: "web.admin.insights.devices.audio.decision.direct",
  remux: "web.admin.insights.devices.audio.decision.remux",
  transcode: "web.admin.insights.devices.audio.decision.transcode",
};

const AUDIO_CHANNEL_LABELS: Record<string, MessageKey> = {
  mono: "web.admin.insights.devices.audio.channels.mono",
  stereo: "web.admin.insights.devices.audio.channels.stereo",
  surround: "web.admin.insights.devices.audio.channels.surround",
};

function AudioUsageCard({ usage }: { usage: AdminInsightsDevices["audio"] }) {
  const t = useT();
  const sessionCount = useSessionCount();
  const share = (percent: string) => t("web.admin.insights.percent", { value: percent });
  return (
    <Card className="surface-panel rounded-2xl border-0">
      <CardHeader className="space-y-0.5">
        <CardTitle className="text-sm font-bold">
          {t("web.admin.insights.devices.audio.title")}
        </CardTitle>
        <p className="text-muted-foreground text-xs">
          {usage.sessions > 0
            ? t("web.admin.insights.devices.audio.subtitle")
            : t("web.admin.insights.devices.subtitles.noFacts")}
        </p>
      </CardHeader>
      <CardContent className="grid grid-cols-1 gap-5 md:grid-cols-3">
        <section className="space-y-1.5">
          <h3 className="text-muted-foreground text-xs font-semibold">
            {t("web.admin.insights.devices.languages")}
          </h3>
          <BarList
            items={languageItems(
              usage.languages,
              t("web.languageName.unknown"),
              usage.sessions,
              share,
            )}
            seriesIndex={0}
            formatValue={sessionCount}
            emptyLabel={t("web.admin.insights.devices.resolution.empty")}
          />
        </section>
        <section className="space-y-1.5">
          <h3 className="text-muted-foreground text-xs font-semibold">
            {t("web.admin.insights.devices.audio.decisionTitle")}
          </h3>
          <BarList
            items={labeledItems(t, usage.decisions, AUDIO_DECISION_LABELS)}
            seriesIndex={1}
            formatValue={sessionCount}
            emptyLabel={t("web.admin.insights.devices.resolution.empty")}
          />
        </section>
        <section className="space-y-1.5">
          <h3 className="text-muted-foreground text-xs font-semibold">
            {t("web.admin.insights.devices.audio.channelsTitle")}
          </h3>
          <BarList
            items={labeledItems(t, usage.delivered_channels, AUDIO_CHANNEL_LABELS)}
            seriesIndex={2}
            formatValue={sessionCount}
            emptyLabel={t("web.admin.insights.devices.resolution.empty")}
          />
        </section>
      </CardContent>
    </Card>
  );
}
