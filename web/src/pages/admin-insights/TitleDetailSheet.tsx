import { useMemo } from "react";
import { Link, useSearchParams } from "react-router";
import { ExternalLink, History } from "lucide-react";

import { V2ProblemError } from "@/api/v2/request";
import { formatWatchTime } from "@/components/admin/dashboard/format";
import { Button } from "@/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminInsightsTitle, type AdminInsightsTitle } from "@/hooks/queries/admin/insights";
import { useT, type Translate } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

import { formatHistoryDuration } from "../admin-history/historyFormat";
import { episodeCode } from "../admin-users/detail/format";
import { InsightsPlaysChart } from "./InsightsPlaysChart";
import {
  buildHistoryDrilldownHref,
  buildInsightsWindowQueryParams,
  formatInsightsCount,
  formatInsightsWindowDates,
  formatRatePercentNumber,
  formatShortCalendarDate,
  insightsHrefWith,
  type InsightsFilterState,
  type InsightsPersonScope,
} from "./insightsHelpers";
import { InsightKpiCard, InsightSectionError } from "./insightsShared";
import { formatInsightsTitle } from "./insightsTitleFormat";
import { useInsightsPerson } from "./useInsightsPerson";

type TitleViewer = AdminInsightsTitle["viewers"][number];

function viewerProgress(viewer: TitleViewer, title: AdminInsightsTitle, t: Translate): string {
  if (viewer.last_episode) {
    const ep = viewer.last_episode;
    const label = [episodeCode(ep.season_number, ep.episode_number), ep.title]
      .filter(Boolean)
      .join(" · ");
    return t("web.admin.insights.titleDetail.viewers.lastEpisode", { episode: label });
  }
  if (viewer.furthest_position_seconds != null && viewer.furthest_position_seconds > 0) {
    const position = formatHistoryDuration(viewer.furthest_position_seconds, t);
    return title.duration_seconds
      ? t("web.admin.insights.titleDetail.viewers.furthest", {
          position,
          duration: formatHistoryDuration(title.duration_seconds, t),
        })
      : t("web.admin.insights.titleDetail.viewers.furthestNoDuration", { position });
  }
  return "";
}

/**
 * One movie or series over the page's window and filters, opened from the
 * Titles tab, or from a person's panel, in which case `person` scopes it to
 * that person's plays.
 */
export function TitleDetailSheet({
  titleId,
  person,
  filters,
  onClose,
}: {
  titleId: string | undefined;
  person: InsightsPersonScope | undefined;
  filters: InsightsFilterState;
  onClose: () => void;
}) {
  const t = useT();
  const [searchParams] = useSearchParams();
  const personNames = useInsightsPerson(person);
  const queryParams = useMemo(
    () => buildInsightsWindowQueryParams(filters, { person }),
    [filters, person],
  );
  const query = useAdminInsightsTitle(titleId, queryParams);
  const data = query.data;
  const notFound = query.error instanceof V2ProblemError && query.error.problemType === "not_found";

  const heading = data ? formatInsightsTitle(data, t) : t("web.admin.insights.title");
  const description = data
    ? [
        t(
          data.kind === "series"
            ? "web.admin.insights.titleDetail.kind.series"
            : "web.admin.insights.titleDetail.kind.movie",
        ),
        t(
          "web.admin.insights.detail.window",
          formatInsightsWindowDates(data.from, data.to, data.tz),
        ),
      ].join(" · ")
    : "";

  return (
    <Sheet
      open={Boolean(titleId)}
      onOpenChange={(open) => {
        if (!open) onClose();
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
            <SheetDescription className="truncate text-xs">{description}</SheetDescription>
          </div>
          {data ? (
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <Button variant="outline" size="sm" asChild>
                <Link to={`/item/${encodeURIComponent(data.content_id)}`}>
                  <ExternalLink className="h-3.5 w-3.5" />
                  {t("web.admin.history.detail.action.openMedia")}
                </Link>
              </Button>
              <Button variant="outline" size="sm" asChild>
                <Link
                  to={buildHistoryDrilldownHref({
                    ...(data.kind === "series"
                      ? { seriesId: data.content_id }
                      : { mediaItemId: data.content_id }),
                    libraryId: filters.libraryId,
                    userId: person?.userId ?? filters.userId,
                    profileId: person?.profileId,
                    startedAfter: data.from,
                    startedBefore: data.to,
                  })}
                >
                  <History className="h-3.5 w-3.5" />
                  {t("web.admin.insights.detail.action.history")}
                </Link>
              </Button>
            </div>
          ) : null}
          {person ? (
            <div className="text-muted-foreground mt-2 flex flex-wrap items-center gap-2 text-xs">
              <span>
                {t("web.admin.insights.titleDetail.scopedToPerson", {
                  name: personNames.name ?? String(person.userId),
                })}
              </span>
              <Link
                to={insightsHrefWith(searchParams, { person: null, profile: null })}
                className="text-primary font-medium hover:underline"
              >
                {t("web.admin.insights.titleDetail.showEveryone")}
              </Link>
            </div>
          ) : null}
        </SheetHeader>

        <div className="flex-1 space-y-5 overflow-y-auto px-5 py-4">
          {query.isLoading ? (
            <div className="space-y-3">
              <Skeleton className="h-24 w-full rounded-xl" />
              <Skeleton className="h-40 w-full rounded-xl" />
              <Skeleton className="h-32 w-full rounded-xl" />
            </div>
          ) : notFound ? (
            <div className="text-muted-foreground py-12 text-center text-sm">
              {t("web.admin.insights.titleDetail.notFound")}
            </div>
          ) : query.error || !data ? (
            <InsightSectionError
              error={query.error}
              onRetry={() => {
                void query.refetch();
              }}
            />
          ) : (
            <TitleDetailBody data={data} searchParams={searchParams} />
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

function TitleDetailBody({
  data,
  searchParams,
}: {
  data: AdminInsightsTitle;
  searchParams: URLSearchParams;
}) {
  const t = useT();
  const { summary } = data;
  const count = (n: number) => n.toLocaleString(formatLocale());
  return (
    <>
      <div className="grid grid-cols-2 gap-3">
        <InsightKpiCard
          label={t("web.admin.insights.activity.kpi.plays")}
          value={count(summary.plays)}
          subtext={t("web.admin.insights.devices.kpi.directPlayDetail", {
            direct: count(summary.direct_plays),
            remux: count(summary.remux_plays),
            transcode: count(summary.transcode_plays),
          })}
        />
        <InsightKpiCard
          label={t("web.admin.insights.detail.viewers")}
          value={count(summary.viewers)}
        />
        <InsightKpiCard
          label={t("web.admin.insights.activity.kpi.watchTime")}
          value={formatWatchTime(summary.watch_seconds)}
          subtext={formatInsightsCount(t, "web.admin.insights.sessionCount", summary.sessions)}
        />
        <InsightKpiCard
          label={t("web.admin.insights.activity.kpi.completionRate")}
          value={t("web.admin.insights.percent", {
            value: formatRatePercentNumber(
              summary.plays > 0 ? summary.completed_plays / summary.plays : 0,
            ),
          })}
          subtext={t("web.admin.insights.activity.kpi.completionDetail", {
            completed: count(summary.completed_plays),
            total: count(summary.plays),
          })}
          accent="success"
        />
      </div>

      <section className="space-y-2">
        <h3 className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
          {t("web.admin.insights.detail.playsOverTime")}
        </h3>
        <InsightsPlaysChart
          buckets={data.buckets}
          from={data.from}
          to={data.to}
          tz={data.tz}
          unit={data.bucket_unit}
          emptyMessage={t("web.admin.insights.activity.volume.empty")}
        />
      </section>

      <section className="space-y-2">
        <h3 className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
          {t("web.admin.insights.titleDetail.viewers.title")}
        </h3>
        {data.viewers.length === 0 ? (
          <p className="text-muted-foreground py-4 text-center text-sm">
            {t("web.admin.insights.detail.noPlays")}
          </p>
        ) : (
          <ul className="divide-border/50 border-border/70 divide-y rounded-xl border">
            {data.viewers.map((viewer) => {
              const progress = viewerProgress(viewer, data, t);
              return (
                <li
                  key={`${viewer.user_id}:${viewer.profile_id}`}
                  className="flex items-start gap-3 px-3 py-2 text-xs"
                >
                  <div className="min-w-0 flex-1 space-y-0.5">
                    <Link
                      to={insightsHrefWith(searchParams, {
                        title: null,
                        person: viewer.user_id,
                        profile: viewer.profile_id,
                      })}
                      dir="auto"
                      className="hover:text-primary bidi-text block truncate text-sm font-medium hover:underline"
                    >
                      {viewer.profile_name}
                    </Link>
                    <div className="text-muted-foreground truncate">
                      {[viewer.username, progress].filter(Boolean).join(" · ")}
                    </div>
                  </div>
                  <div className="shrink-0 text-end tabular-nums">
                    <div className="font-semibold">
                      {formatInsightsCount(t, "web.admin.insights.playCount", viewer.plays)}
                    </div>
                    <div className="text-muted-foreground">
                      {[
                        formatWatchTime(viewer.watch_seconds),
                        formatShortCalendarDate(viewer.last_played_at),
                      ].join(" · ")}
                    </div>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
        {data.viewers_truncated ? (
          <p className="text-muted-foreground text-xs">
            {t("web.admin.insights.titleDetail.viewers.truncated")}
          </p>
        ) : null}
      </section>

      {data.kind === "series" ? (
        <section className="space-y-2">
          <h3 className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
            {t("web.admin.insights.titleDetail.episodes.title")}
          </h3>
          {data.episodes.length === 0 ? (
            <p className="text-muted-foreground py-4 text-center text-sm">
              {t("web.admin.insights.detail.noPlays")}
            </p>
          ) : (
            <ul className="divide-border/50 border-border/70 divide-y rounded-xl border">
              {data.episodes.map((ep) => (
                <li key={ep.content_id} className="flex items-center gap-3 px-3 py-2 text-xs">
                  <span className="text-muted-foreground w-14 shrink-0 font-mono tabular-nums">
                    {episodeCode(ep.season_number, ep.episode_number)}
                  </span>
                  <span className="min-w-0 flex-1 truncate">{ep.title}</span>
                  <span className="shrink-0 text-end tabular-nums">
                    <span className="font-semibold">
                      {formatInsightsCount(t, "web.admin.insights.playCount", ep.plays)}
                    </span>
                    <span className="text-muted-foreground ms-1.5">
                      {formatInsightsCount(t, "web.admin.insights.viewerCount", ep.viewers)}
                    </span>
                  </span>
                </li>
              ))}
            </ul>
          )}
          {data.episodes_truncated ? (
            <p className="text-muted-foreground text-xs">
              {t("web.admin.insights.titleDetail.episodes.truncated")}
            </p>
          ) : null}
        </section>
      ) : null}
    </>
  );
}
