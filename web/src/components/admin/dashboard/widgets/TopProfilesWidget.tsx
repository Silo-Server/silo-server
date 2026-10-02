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
 * Most active household profiles of the chosen window.
 *
 * Rows are per profile, not per account: several profiles share one login, so
 * the label carries both when the account name adds anything.
 */
export function TopProfilesWidget() {
  const { range } = useWidgetRange();
  const query = useAdminTopActivity(rangeDays(range));
  const items = useMemo<BarListItem[]>(
    () =>
      (query.data?.profiles ?? []).slice(0, ROW_LIMIT).map((profile) => {
        const account = profile.username || `User #${profile.user_id}`;
        const name = profile.profile_name || profile.profile_id || account;
        return {
          id: `${profile.user_id}:${profile.profile_id}`,
          label: name === account ? account : `${name} · ${account}`,
          value: profile.plays,
          secondary: formatWatchTime(profile.total_seconds),
          to: `/admin/history?user_id=${profile.user_id}${
            profile.profile_id ? `&profile_id=${encodeURIComponent(profile.profile_id)}` : ""
          }`,
        };
      }),
    [query.data],
  );

  return (
    <Card className="h-full">
      <CardHeader className="flex shrink-0 flex-row items-center justify-between gap-2 space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">
          {rangeTitle("Most active profiles", range)}
        </CardTitle>
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
          <SectionError message="Failed to load profile activity." />
        ) : (
          <BarList
            items={items}
            formatValue={(plays) => `${plays.toLocaleString()} ${plays === 1 ? "play" : "plays"}`}
            emptyLabel={`No profile activity in ${rangePhrase(range)}`}
          />
        )}
      </CardContent>
    </Card>
  );
}
