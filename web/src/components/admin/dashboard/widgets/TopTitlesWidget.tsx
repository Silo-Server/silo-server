import { useMemo } from "react";
import { Link } from "react-router";

import { Card, CardContent } from "@/components/ui/card";
import { useAdminTopActivity } from "@/hooks/queries/admin/dashboardInsights";
import { BarList, BarListSkeleton } from "../charts";
import type { BarListItem } from "../charts";
import { SectionError } from "../feedback";
import { formatWatchTime } from "../format";
import { rangeDays, rangePhrase, rangeTitle } from "../range";
import { useWidgetRange } from "../widgetChrome";
import { WidgetHeader } from "../WidgetHeader";
import { WidgetRangePicker } from "../WidgetRangePicker";
import { formatLocale } from "@/lib/appLocale";
import { useT } from "@/i18n";

const ROW_LIMIT = 8;

/**
 * Most-played titles of the chosen window. Episodes are rolled up to their
 * series by the endpoint, so a show appears once with the plays of all its
 * episodes.
 */
export function TopTitlesWidget() {
  const t = useT();
  const { range } = useWidgetRange();
  const query = useAdminTopActivity(rangeDays(range));
  const items = useMemo<BarListItem[]>(
    () =>
      (query.data?.titles ?? []).slice(0, ROW_LIMIT).map((title) => ({
        id: title.media_item_id,
        label: title.title || title.media_item_id,
        value: title.plays,
        secondary: formatWatchTime(title.total_seconds),
        // Link to the catalog item, not a filtered history view: for TV the id is
        // a series content id while history rows store episode ids, so a history
        // filter would match nothing.
        to: title.media_item_id ? `/item/${encodeURIComponent(title.media_item_id)}` : undefined,
      })),
    [query.data],
  );

  return (
    <Card className="h-full">
      <WidgetHeader title={rangeTitle(t("web.admin.dashboard.widgets.topTitles.title"), range)}>
        <Link
          to="/admin/history"
          className="text-muted-foreground hover:text-primary text-[11px] whitespace-nowrap transition-colors"
        >
          {t("web.admin.dashboard.widgets.topTitles.allHistory")}
        </Link>
        <WidgetRangePicker />
      </WidgetHeader>
      <CardContent className="min-h-0 flex-1 overflow-y-auto">
        {query.isLoading ? (
          <BarListSkeleton />
        ) : query.error ? (
          <SectionError message={t("web.admin.dashboard.widgets.topTitles.loadFailed")} />
        ) : (
          <BarList
            items={items}
            formatValue={(plays) =>
              t("web.admin.dashboard.widgets.topTitles.playCount", {
                count: plays,
                count_text: plays.toLocaleString(formatLocale()),
              })
            }
            emptyLabel={t("web.admin.dashboard.widgets.topTitles.empty", {
              range: rangePhrase(range),
            })}
          />
        )}
      </CardContent>
    </Card>
  );
}
