import { useMemo } from "react";
import { Link } from "react-router";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useAdminTopActivity } from "@/hooks/queries/admin/dashboardInsights";
import { BarList, BarListSkeleton } from "../charts";
import type { BarListItem } from "../charts";
import { SectionError } from "../feedback";
import { formatWatchTime } from "../format";
import { rangeDays, rangePhrase, rangeTitle } from "../range";
import { useWidgetRange } from "../widgetChrome";
import { WidgetRangePicker } from "../WidgetRangePicker";

const ROW_LIMIT = 8;

/**
 * Most-played titles of the chosen window. Episodes are rolled up to their
 * series by the endpoint, so a show appears once with the plays of all its
 * episodes.
 */
export function TopTitlesWidget() {
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
      <CardHeader className="flex shrink-0 flex-row items-center justify-between gap-2 space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">{rangeTitle("Top titles", range)}</CardTitle>
        <div className="flex min-w-0 items-center gap-2">
          <Link
            to="/admin/history"
            className="text-muted-foreground hover:text-primary text-[11px] whitespace-nowrap transition-colors"
          >
            All history ›
          </Link>
          <WidgetRangePicker />
        </div>
      </CardHeader>
      <CardContent className="min-h-0 flex-1 overflow-y-auto">
        {query.isLoading ? (
          <BarListSkeleton />
        ) : query.error ? (
          <SectionError message="Failed to load top titles." />
        ) : (
          <BarList
            items={items}
            formatValue={(plays) => `${plays.toLocaleString()} ${plays === 1 ? "play" : "plays"}`}
            emptyLabel={`No plays in ${rangePhrase(range)}`}
          />
        )}
      </CardContent>
    </Card>
  );
}
