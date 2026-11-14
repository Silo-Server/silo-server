import { useMemo } from "react";

import { Card, CardContent } from "@/components/ui/card";
import { useAdminDownloadsStats } from "@/hooks/queries/admin/dashboardInsights";
import { formatFileSize } from "@/lib/mediaFormat";
import { BarList, BarListSkeleton } from "../charts";
import type { BarListItem } from "../charts";
import { SectionError } from "../feedback";
import { WidgetHeader } from "../WidgetHeader";
import { formatLocale } from "@/lib/appLocale";
import { useT } from "@/i18n";

const ROW_LIMIT = 8;

/**
 * Who keeps media downloaded for offline use, and how much.
 *
 * Headline numbers and the per-account list count active managed device
 * entries — media a device holds (or is fetching) offline — while one-shot web
 * downloads only show in the 24h line. Bytes are the completed entries' file
 * sizes: what sits on devices as far as the server can know without devices
 * reporting back. All zeros is a real state, not a failure, so it renders as
 * "No offline downloads".
 */
export function DownloadsWidget() {
  const t = useT();
  const query = useAdminDownloadsStats();
  const stats = query.data;
  const items = useMemo<BarListItem[]>(
    () =>
      (stats?.top_users ?? []).slice(0, ROW_LIMIT).map((user) => ({
        id: String(user.user_id),
        label:
          user.username ||
          t("web.admin.dashboard.widgets.downloads.userFallback", { id: String(user.user_id) }),
        value: user.downloads,
        secondary: formatFileSize(user.total_bytes, { fallback: "" }),
      })),
    [stats, t],
  );

  const hasDownloads =
    (stats?.active_downloads ?? 0) > 0 || (stats?.downloads_started_24h ?? 0) > 0;

  return (
    <Card className="h-full">
      <WidgetHeader title={t("web.admin.dashboard.widgets.downloads.title")}>
        {stats && hasDownloads ? (
          <span className="text-muted-foreground text-[11px] tabular-nums">
            {t("web.admin.dashboard.widgets.downloads.summary24h", {
              started: stats.downloads_started_24h.toLocaleString(formatLocale()),
              finished: stats.downloads_completed_24h.toLocaleString(formatLocale()),
            })}
          </span>
        ) : null}
      </WidgetHeader>
      <CardContent className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto">
        {query.isLoading ? (
          <BarListSkeleton />
        ) : query.error ? (
          <SectionError message={t("web.admin.dashboard.widgets.downloads.loadFailed")} />
        ) : !stats || !hasDownloads ? (
          <div className="text-muted-foreground py-4 text-center text-sm">
            {t("web.admin.dashboard.widgets.downloads.empty")}
          </div>
        ) : (
          <>
            <dl className="grid shrink-0 grid-cols-3 gap-2">
              <DownloadsStat
                label={t("web.admin.dashboard.widgets.downloads.users")}
                value={stats.users_with_downloads.toLocaleString(formatLocale())}
              />
              <DownloadsStat
                label={t("web.admin.dashboard.widgets.downloads.items")}
                value={stats.active_downloads.toLocaleString(formatLocale())}
              />
              <DownloadsStat
                label={t("web.admin.dashboard.widgets.downloads.onDevices")}
                value={formatFileSize(stats.total_bytes, { fallback: "0 B" })}
              />
            </dl>
            <BarList
              items={items}
              formatValue={(count) =>
                t("web.admin.dashboard.widgets.downloads.itemCount", {
                  count,
                  count_text: count.toLocaleString(formatLocale()),
                })
              }
              emptyLabel={t("web.admin.dashboard.widgets.downloads.empty")}
            />
          </>
        )}
      </CardContent>
    </Card>
  );
}

function DownloadsStat({ label, value }: { label: string; value: string }) {
  return (
    <div className="surface-panel-subtle rounded-lg px-2.5 py-2">
      <dt className="text-muted-foreground text-[11px] leading-none font-medium">{label}</dt>
      <dd className="mt-1 text-lg leading-none font-extrabold tracking-tight tabular-nums">
        {value}
      </dd>
    </div>
  );
}
