import { useMemo } from "react";
import { Link } from "react-router";

import { Card, CardContent } from "@/components/ui/card";
import { useDashboardInsightsTitles } from "@/hooks/queries/admin/insights";
import { BarList, BarListSkeleton } from "../charts";
import type { BarListItem } from "../charts";
import { SectionError } from "../feedback";
import { formatWatchTime } from "../format";
import { rangePhrase, rangeTitle } from "../range";
import { useWidgetRange } from "../widgetChrome";
import { WidgetHeader } from "../WidgetHeader";
import { WidgetRangePicker } from "../WidgetRangePicker";
import { formatLocale } from "@/lib/appLocale";
import { useT } from "@/i18n";
import { widgetRangeToInsightsRange } from "@/pages/admin-insights/insightsHelpers";

const ROW_LIMIT = 8;

/**
 * Most-played movies and series of the chosen window, counted the way Insights
 * counts plays: resumed sittings are one play and false starts none. Episodes
 * roll up to their series, so a show appears once with the plays of all its
 * episodes. The read returns each kind's top rows, and the top of both lists
 * together is always among them.
 */
export function TopTitlesWidget() {
  const t = useT();
  const { range } = useWidgetRange();
  const query = useDashboardInsightsTitles(range, ROW_LIMIT);
  const items = useMemo<BarListItem[]>(() => {
    const rows = [...(query.data?.movies ?? []), ...(query.data?.shows ?? [])];
    rows.sort((a, b) => b.plays - a.plays || b.watch_seconds - a.watch_seconds);
    return rows.slice(0, ROW_LIMIT).map((title) => ({
      id: title.content_id,
      label: title.title || t("web.admin.insights.titles.unknownTitle"),
      value: title.plays,
      secondary: formatWatchTime(title.watch_seconds),
      // Link to the catalog item, not a filtered history view: for TV the id is
      // a series content id while history rows store episode ids, so a history
      // filter would match nothing. A removed title has nowhere to go.
      to: title.title ? `/item/${encodeURIComponent(title.content_id)}` : undefined,
    }));
  }, [query.data, t]);

  return (
    <Card className="h-full">
      <WidgetHeader title={rangeTitle(t("web.admin.dashboard.widgets.topTitles.title"), range)}>
        <Link
          to={`/admin/insights?tab=titles&range=${widgetRangeToInsightsRange(range)}`}
          className="text-muted-foreground hover:text-primary hidden text-[0.6875rem] whitespace-nowrap transition-colors @sm/card-header:inline"
        >
          {t("web.admin.dashboard.widgets.openInsights")}
        </Link>
        <Link
          to="/admin/history"
          className="text-muted-foreground hover:text-primary text-[0.6875rem] whitespace-nowrap transition-colors"
        >
          {t("web.admin.dashboard.widgets.topTitles.allHistory")}
        </Link>
        <WidgetRangePicker />
      </WidgetHeader>
      <CardContent className="min-h-0 flex-1 overflow-y-auto">
        {query.isLoading ? (
          <BarListSkeleton />
        ) : query.error && !query.data ? (
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
