import { useMemo } from "react";
import { Link, useSearchParams } from "react-router";
import { History, UserRound } from "lucide-react";

import { BarList, BarListSkeleton, type BarListItem } from "@/components/admin/dashboard/charts";
import { formatWatchTime } from "@/components/admin/dashboard/format";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useAdminInsightsActivity,
  useAdminInsightsDevices,
  useAdminInsightsTitles,
} from "@/hooks/queries/admin/insights";
import { useT } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

import { InsightsPlaysChart } from "./InsightsPlaysChart";
import {
  buildHistoryDrilldownHref,
  buildInsightsWindowQueryParams,
  formatInsightsCount,
  formatInsightsWindowDates,
  formatRatePercentNumber,
  insightsHrefWith,
  type InsightsFilterState,
  type InsightsPersonScope,
} from "./insightsHelpers";
import { InsightKpiCard, InsightSectionError } from "./insightsShared";
import { formatInsightsTitle } from "./insightsTitleFormat";
import { useInsightsPerson } from "./useInsightsPerson";

const ALL_PROFILES = "__all__";
const PERSON_TOP_TITLES = 8;
const PERSON_TOP_CLIENTS = 6;

/**
 * One account, or one of its household profiles, over the page's window and
 * library filter. It reads Activity, Titles and Devices scoped to the person
 * and shows only person-scoped figures: the server-wide peak stays on the
 * Playback tab.
 */
export function PersonDetailSheet({
  person,
  filters,
  onClose,
}: {
  person: InsightsPersonScope | undefined;
  filters: InsightsFilterState;
  onClose: () => void;
}) {
  const t = useT();
  const [searchParams, setSearchParams] = useSearchParams();
  const open = person !== undefined;
  const names = useInsightsPerson(person);

  const params = useMemo(
    () => (person ? buildInsightsWindowQueryParams(filters, { person }) : undefined),
    [filters, person],
  );
  const activity = useAdminInsightsActivity(params ?? {}, { enabled: open });
  const titles = useAdminInsightsTitles(
    { ...(params ?? {}), limit: PERSON_TOP_TITLES },
    { enabled: open },
  );
  const devices = useAdminInsightsDevices(
    { ...(params ?? {}), min_sessions: 1 },
    { enabled: open },
  );

  const heading = names.name ?? t("web.admin.insights.title");
  const windowLabel = activity.data
    ? t(
        "web.admin.insights.detail.window",
        formatInsightsWindowDates(activity.data.from, activity.data.to, activity.data.tz),
      )
    : "";
  // The picker lists the account's profiles, plus the scoped one when the
  // account's store no longer lists it, so "All profiles" stays reachable.
  const showProfilePicker =
    names.profiles.length > 1 || Boolean(person?.profileId && !names.profileListed);

  const titleItems = useMemo<BarListItem[]>(() => {
    if (!titles.data) return [];
    return [...titles.data.movies, ...titles.data.shows]
      .sort((a, b) => b.plays - a.plays || b.watch_seconds - a.watch_seconds)
      .slice(0, PERSON_TOP_TITLES)
      .map((row) => ({
        id: row.content_id,
        label: formatInsightsTitle(row, t),
        value: row.plays,
        secondary: formatWatchTime(row.watch_seconds),
        // The person stays in the URL, so the title panel shows this person's
        // plays and closing it returns here.
        to: insightsHrefWith(searchParams, { title: row.content_id }),
      }));
  }, [searchParams, t, titles.data]);

  const clientItems = useMemo<BarListItem[]>(() => {
    if (!devices.data) return [];
    const unknownClient = t("web.admin.insights.devices.unknownClient");
    return devices.data.matrix.slice(0, PERSON_TOP_CLIENTS).map((row) => ({
      id: `${row.client_name}:${row.device_platform}`,
      label: [row.client_name || unknownClient, row.device_platform].filter(Boolean).join(" · "),
      value: row.total_sessions,
      secondary: t("web.admin.insights.personDetail.clientDirectRate", {
        percent: formatRatePercentNumber(row.direct_rate),
      }),
    }));
  }, [devices.data, t]);

  const count = (n: number) => n.toLocaleString(formatLocale());
  const playCount = (n: number) => formatInsightsCount(t, "web.admin.insights.playCount", n);
  const sessionCount = (n: number) => formatInsightsCount(t, "web.admin.insights.sessionCount", n);

  return (
    <Sheet
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
    >
      <SheetContent
        side="right"
        className="flex w-full flex-col gap-0 overflow-hidden p-0 sm:max-w-xl"
      >
        <SheetHeader className="border-border/70 border-b px-5 py-4">
          <div className="min-w-0 space-y-1 pe-8">
            <SheetTitle dir="auto" className="bidi-text truncate text-base font-bold">
              {heading}
            </SheetTitle>
            <SheetDescription className="truncate text-xs">
              {[names.profileLabel ? names.accountName : undefined, windowLabel]
                .filter(Boolean)
                .join(" · ")}
            </SheetDescription>
          </div>
          {person ? (
            <div className="mt-2 flex flex-wrap items-center gap-2">
              {showProfilePicker ? (
                <Select
                  value={person.profileId ?? ALL_PROFILES}
                  onValueChange={(value) => {
                    const next = new URLSearchParams(searchParams);
                    if (value === ALL_PROFILES) next.delete("profile");
                    else next.set("profile", value);
                    setSearchParams(next, { replace: true });
                  }}
                >
                  <SelectTrigger
                    aria-label={t("web.admin.insights.personDetail.profileLabel")}
                    className="h-8 max-w-[14rem] min-w-[8.5rem] text-xs"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={ALL_PROFILES}>
                      {t("web.admin.insights.personDetail.allProfiles")}
                    </SelectItem>
                    {names.profiles.map((p) => (
                      <SelectItem key={p.id} value={p.id}>
                        {p.name}
                      </SelectItem>
                    ))}
                    {person.profileId && !names.profileListed ? (
                      <SelectItem value={person.profileId}>{names.profileLabel}</SelectItem>
                    ) : null}
                  </SelectContent>
                </Select>
              ) : null}
              <Button variant="outline" size="sm" asChild>
                <Link to={`/admin/users/${person.userId}`}>
                  <UserRound className="h-3.5 w-3.5" />
                  {t("web.admin.insights.personDetail.action.openAccount")}
                </Link>
              </Button>
              {activity.data ? (
                // Only once the window is resolved: an unbounded link would
                // open the person's whole history.
                <Button variant="outline" size="sm" asChild>
                  <Link
                    to={buildHistoryDrilldownHref({
                      userId: person.userId,
                      profileId: person.profileId,
                      libraryId: filters.libraryId,
                      startedAfter: activity.data.from,
                      startedBefore: activity.data.to,
                    })}
                  >
                    <History className="h-3.5 w-3.5" />
                    {t("web.admin.insights.detail.action.history")}
                  </Link>
                </Button>
              ) : null}
            </div>
          ) : null}
        </SheetHeader>

        <div className="flex-1 space-y-5 overflow-y-auto px-5 py-4">
          {activity.isLoading ? (
            <div className="space-y-3">
              <Skeleton className="h-24 w-full rounded-xl" />
              <Skeleton className="h-40 w-full rounded-xl" />
            </div>
          ) : activity.error || !activity.data ? (
            <InsightSectionError
              error={activity.error}
              onRetry={() => {
                void activity.refetch();
              }}
            />
          ) : (
            <>
              <div className="grid grid-cols-2 gap-3">
                <InsightKpiCard
                  label={t("web.admin.insights.activity.kpi.plays")}
                  value={count(activity.data.summary.plays)}
                  subtext={t("web.admin.insights.activity.kpi.playsBreakdown", {
                    movies: count(activity.data.summary.movies),
                    movie_count: activity.data.summary.movies,
                    episodes: count(activity.data.summary.episodes),
                    episode_count: activity.data.summary.episodes,
                  })}
                />
                <InsightKpiCard
                  label={t("web.admin.insights.activity.kpi.watchTime")}
                  value={formatWatchTime(activity.data.summary.watch_seconds)}
                  subtext={sessionCount(activity.data.summary.sessions)}
                />
                <InsightKpiCard
                  label={t("web.admin.insights.activity.kpi.completionRate")}
                  value={t("web.admin.insights.percent", {
                    value: formatRatePercentNumber(activity.data.summary.completion_rate),
                  })}
                  subtext={t("web.admin.insights.activity.kpi.completionDetail", {
                    completed: count(activity.data.summary.completed_plays),
                    total: count(activity.data.summary.plays),
                  })}
                  accent="success"
                />
                <InsightKpiCard
                  label={t("web.admin.insights.devices.kpi.directPlayRate")}
                  value={t("web.admin.insights.percent", {
                    value: formatRatePercentNumber(
                      activity.data.summary.plays > 0
                        ? activity.data.summary.direct_plays / activity.data.summary.plays
                        : 0,
                    ),
                  })}
                  subtext={t("web.admin.insights.devices.kpi.directPlayDetail", {
                    direct: count(activity.data.summary.direct_plays),
                    remux: count(activity.data.summary.remux_plays),
                    transcode: count(activity.data.summary.transcode_plays),
                  })}
                  accent="info"
                />
              </div>

              <section className="space-y-2">
                <h3 className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
                  {t("web.admin.insights.detail.playsOverTime")}
                </h3>
                <InsightsPlaysChart
                  buckets={activity.data.buckets}
                  from={activity.data.from}
                  to={activity.data.to}
                  tz={activity.data.tz}
                  unit={activity.data.bucket_unit}
                  emptyMessage={t("web.admin.insights.activity.volume.empty")}
                />
              </section>
            </>
          )}

          <section className="space-y-2">
            <h3 className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
              {t("web.admin.insights.personDetail.topTitles")}
            </h3>
            {titles.error ? (
              <InsightSectionError
                error={titles.error}
                onRetry={() => {
                  void titles.refetch();
                }}
              />
            ) : titles.isLoading ? (
              <BarListSkeleton />
            ) : (
              <BarList
                items={titleItems}
                seriesIndex={0}
                formatValue={playCount}
                emptyLabel={t("web.admin.insights.detail.noPlays")}
              />
            )}
          </section>

          <section className="space-y-2">
            <h3 className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
              {t("web.admin.insights.personDetail.clients")}
            </h3>
            {devices.error ? (
              <InsightSectionError
                error={devices.error}
                onRetry={() => {
                  void devices.refetch();
                }}
              />
            ) : devices.isLoading ? (
              <BarListSkeleton />
            ) : (
              <BarList
                items={clientItems}
                seriesIndex={1}
                formatValue={sessionCount}
                emptyLabel={t("web.admin.insights.personDetail.clientsEmpty")}
              />
            )}
          </section>
        </div>
      </SheetContent>
    </Sheet>
  );
}
