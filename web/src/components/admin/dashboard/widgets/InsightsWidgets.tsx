import type { ReactNode } from "react";
import { Link } from "react-router";

import { ActivePeopleChart } from "@/components/admin/insights/ActivePeopleChart";
import { LibraryCoverageList } from "@/components/admin/insights/LibraryCoverageList";

import {
  PlaysOverTimeChart,
  type PlaysOverTimeView,
} from "@/components/admin/insights/PlaysOverTimeChart";
import { ViewingHeatmap } from "@/components/admin/insights/ViewingHeatmap";
import { ViewingTimeChart } from "@/components/admin/insights/ViewingTimeChart";
import type { ViewingTimeMetric } from "@/components/admin/insights/viewingTime";
import { Card, CardContent } from "@/components/ui/card";
import {
  useDashboardInsightsActivity,
  useDashboardInsightsLibrary,
  type AdminInsightsActivity,
} from "@/hooks/queries/admin/insights";
import { useT } from "@/i18n";
import { widgetRangeToInsightsRange } from "@/pages/admin-insights/insightsHelpers";
import { BarListSkeleton, ChartEmptyState, ChartSkeleton } from "../charts";
import { SectionError } from "../feedback";
import { rangeTitle } from "../range";
import { useWidgetRange } from "../widgetChrome";
import { WidgetHeader } from "../WidgetHeader";
import { WidgetRangePicker } from "../WidgetRangePicker";
import { WidgetViewToggle } from "../WidgetViewToggle";

/**
 * Dashboard widgets backed by the Insights Activity read. They count plays the
 * Insights way (resumed sittings are one play, false starts none), reach back
 * as far as history goes, and show the whole server in the browser's
 * timezone. Widgets on the same window share one read.
 */

function InsightsWidgetCard({
  title,
  tab,
  toggle,
  children,
}: {
  title: string;
  tab: string;
  toggle?: ReactNode;
  children: ReactNode;
}) {
  const t = useT();
  const { range } = useWidgetRange();
  return (
    <Card className="h-full">
      <WidgetHeader title={rangeTitle(title, range)}>
        {toggle}
        <Link
          to={`/admin/insights?tab=${tab}&range=${widgetRangeToInsightsRange(range)}`}
          className="text-muted-foreground hover:text-primary hidden text-[0.6875rem] whitespace-nowrap transition-colors @lg/card-header:inline"
        >
          {t("web.admin.dashboard.widgets.openInsights")}
        </Link>
        <WidgetRangePicker />
      </WidgetHeader>
      <CardContent className="flex min-h-0 flex-1 flex-col">{children}</CardContent>
    </Card>
  );
}

function useMetricToggle(): { metric: ViewingTimeMetric; toggle: ReactNode } {
  const t = useT();
  const { view, setView } = useWidgetRange();
  const metric: ViewingTimeMetric = view === "watch_time" ? "watch_time" : "plays";
  return {
    metric,
    toggle: (
      <WidgetViewToggle
        value={metric}
        options={[
          {
            value: "plays",
            label: t("web.admin.insights.activity.volume.metricPlays"),
            ariaLabel: t("web.admin.insights.activity.volume.metricPlays"),
          },
          {
            value: "watch_time",
            label: t("web.admin.insights.activity.volume.metricWatchTime"),
            ariaLabel: t("web.admin.insights.activity.volume.metricWatchTime"),
          },
        ]}
        onChange={setView}
      />
    ),
  };
}

/**
 * The Activity read for the widget's window, or the skeleton or error that
 * stands in for it. A failed poll keeps the last good data on screen: these
 * reads poll every 5 or 15 minutes without retrying, so an error shown over
 * data would stay up until the next poll.
 */
function ActivityBody({ children }: { children: (data: AdminInsightsActivity) => ReactNode }) {
  const t = useT();
  const { range } = useWidgetRange();
  const query = useDashboardInsightsActivity(range);
  if (query.isLoading) {
    return <ChartSkeleton fill />;
  }
  if (!query.data) {
    return <SectionError message={t("web.admin.dashboard.widgets.insights.loadFailed")} />;
  }
  return <>{children(query.data)}</>;
}

/** Plays per day, week or month, split by play method or media type, or watch time. */
export function InsightsPlaysWidget() {
  const t = useT();
  const { view, setView } = useWidgetRange();
  const mode: PlaysOverTimeView = view === "media" || view === "watch_time" ? view : "method";
  return (
    <InsightsWidgetCard
      title={t("web.admin.insights.activity.volume.title")}
      tab="playback"
      toggle={
        <WidgetViewToggle
          value={mode}
          options={[
            {
              value: "method",
              label: t("web.admin.insights.activity.volume.splitMethod"),
              ariaLabel: t("web.admin.insights.activity.volume.splitMethod"),
            },
            {
              value: "media",
              label: t("web.admin.insights.activity.volume.splitMediaType"),
              ariaLabel: t("web.admin.insights.activity.volume.splitMediaType"),
            },
            {
              value: "watch_time",
              label: t("web.admin.insights.activity.volume.metricWatchTime"),
              ariaLabel: t("web.admin.insights.activity.volume.metricWatchTime"),
            },
          ]}
          onChange={setView}
        />
      }
    >
      <ActivityBody>{(data) => <PlaysOverTimeChart data={data} view={mode} fill />}</ActivityBody>
    </InsightsWidgetCard>
  );
}

/** The weekday × hour heatmap of play starts. */
export function InsightsViewingHoursWidget() {
  const t = useT();
  return (
    <InsightsWidgetCard title={t("web.admin.insights.activity.heatmap.title")} tab="playback">
      <ActivityBody>
        {(data) =>
          data.heatmap.length === 0 ? (
            <ChartEmptyState fill message={t("web.admin.insights.activity.heatmap.empty")} />
          ) : (
            <div className="min-h-0 flex-1 overflow-y-auto">
              <ViewingHeatmap cells={data.heatmap} compact />
            </div>
          )
        }
      </ActivityBody>
    </InsightsWidgetCard>
  );
}

/** Plays or watch time by day of the week. */
export function InsightsWeekdayWidget() {
  const t = useT();
  const { metric, toggle } = useMetricToggle();
  return (
    <InsightsWidgetCard
      title={t("web.admin.insights.activity.weekday.title")}
      tab="playback"
      toggle={toggle}
    >
      <ActivityBody>
        {(data) => <ViewingTimeChart cells={data.heatmap} axis="weekday" metric={metric} fill />}
      </ActivityBody>
    </InsightsWidgetCard>
  );
}

/** Plays or watch time by hour of the day. */
export function InsightsHourOfDayWidget() {
  const t = useT();
  const { metric, toggle } = useMetricToggle();
  return (
    <InsightsWidgetCard
      title={t("web.admin.insights.activity.hour.title")}
      tab="playback"
      toggle={toggle}
    >
      <ActivityBody>
        {(data) => <ViewingTimeChart cells={data.heatmap} axis="hour" metric={metric} fill />}
      </ActivityBody>
    </InsightsWidgetCard>
  );
}

/** Distinct profiles and accounts watching per day, week or month. */
export function InsightsActivePeopleWidget() {
  const t = useT();
  return (
    <InsightsWidgetCard title={t("web.admin.insights.activity.activePeople.title")} tab="playback">
      <ActivityBody>{(data) => <ActivePeopleChart data={data} fill />}</ActivityBody>
    </InsightsWidgetCard>
  );
}

/** Each movie and TV library with the share of its titles played in the window. */
export function InsightsLibraryCoverageWidget() {
  const t = useT();
  const { range } = useWidgetRange();
  const query = useDashboardInsightsLibrary(range);
  return (
    <InsightsWidgetCard title={t("web.admin.insights.library.coverage.title")} tab="library">
      <div className="min-h-0 flex-1 overflow-y-auto">
        {query.isLoading ? (
          <BarListSkeleton />
        ) : !query.data ? (
          <SectionError message={t("web.admin.dashboard.widgets.insights.loadFailed")} />
        ) : (
          <LibraryCoverageList libraries={query.data.libraries} />
        )}
      </div>
    </InsightsWidgetCard>
  );
}
