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
 * Most active household profiles of the chosen window.
 *
 * Rows are per profile, not per account: several profiles share one login, so
 * the label carries both when the account name adds anything.
 */
export function TopProfilesWidget() {
  const t = useT();
  const { range } = useWidgetRange();
  const query = useAdminTopActivity(rangeDays(range));
  const items = useMemo<BarListItem[]>(
    () =>
      (query.data?.profiles ?? []).slice(0, ROW_LIMIT).map((profile) => {
        const account =
          profile.username ||
          t("web.admin.dashboard.widgets.topProfiles.userFallback", {
            id: String(profile.user_id),
          });
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
    [query.data, t],
  );

  return (
    <Card className="h-full">
      <WidgetHeader title={rangeTitle(t("web.admin.dashboard.widgets.topProfiles.title"), range)}>
        <Link
          to="/admin/history"
          className="text-muted-foreground hover:text-primary text-[11px] whitespace-nowrap transition-colors"
        >
          {t("web.admin.dashboard.widgets.topProfiles.allHistory")}
        </Link>
        <WidgetRangePicker />
      </WidgetHeader>
      <CardContent className="min-h-0 flex-1 overflow-y-auto">
        {query.isLoading ? (
          <BarListSkeleton />
        ) : query.error ? (
          <SectionError message={t("web.admin.dashboard.widgets.topProfiles.loadFailed")} />
        ) : (
          <BarList
            items={items}
            formatValue={(plays) =>
              t("web.admin.dashboard.widgets.topProfiles.playCount", {
                count: plays,
                count_text: plays.toLocaleString(formatLocale()),
              })
            }
            emptyLabel={t("web.admin.dashboard.widgets.topProfiles.empty", {
              range: rangePhrase(range),
            })}
          />
        )}
      </CardContent>
    </Card>
  );
}
