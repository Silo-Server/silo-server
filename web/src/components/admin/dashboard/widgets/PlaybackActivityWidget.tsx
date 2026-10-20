import { useMemo } from "react";

import { Card, CardContent } from "@/components/ui/card";
import { formatTime } from "@/lib/datetime";
import { useAdminPlaybackActivity } from "@/hooks/queries/admin/dashboardInsights";
import { ChartEmptyState, ChartSkeleton, StackedColumnChart } from "../charts";
import { SectionError } from "../feedback";
import { formatDayLabel, rangeHours, rangePhrase, rangeTitle } from "../range";
import { useWidgetRange } from "../widgetChrome";
import { WidgetHeader } from "../WidgetHeader";
import { WidgetRangePicker } from "../WidgetRangePicker";
import {
  buildPlaybackActivityColumns,
  DEFAULT_PLAYBACK_BUCKET_SECONDS,
  PLAYBACK_SERIES_LABELS,
} from "./playbackActivitySeries";

/** Playback starts per bucket over the chosen window, stacked by play method. */
export function PlaybackActivityWidget() {
  const { range } = useWidgetRange();
  const hours = rangeHours(range);
  const query = useAdminPlaybackActivity(hours);
  const bucketSeconds = query.data?.bucket_seconds || DEFAULT_PLAYBACK_BUCKET_SECONDS;
  // The grid anchors on the server's window end when the response carries it:
  // the buckets were cut on the database clock, and a browser clock a minute
  // behind it around a boundary would discard the newest bucket.
  const serverNow = query.data?.to ? Date.parse(query.data.to) : Number.NaN;
  const columns = useMemo(
    () =>
      buildPlaybackActivityColumns(query.data?.buckets, {
        hours,
        bucketSeconds,
        ...(Number.isFinite(serverNow) ? { now: serverNow } : {}),
      }),
    [query.data, hours, bucketSeconds, serverNow],
  );
  const total = columns.reduce(
    (sum, column) => sum + column.segments.reduce((columnSum, value) => columnSum + value, 0),
    0,
  );
  // Hourly buckets are read as clock times; daily ones as dates, since every
  // column of a month would otherwise be labelled midnight.
  const isDaily = bucketSeconds >= 86_400;

  return (
    <Card className="h-full">
      <WidgetHeader title={rangeTitle("Playback activity", range)}>
        <span className="text-muted-foreground text-[11px] tabular-nums">
          {total.toLocaleString()} {total === 1 ? "session" : "sessions"}
        </span>
        <WidgetRangePicker />
      </WidgetHeader>
      <CardContent className="flex min-h-0 flex-1 flex-col">
        {query.isLoading ? (
          <ChartSkeleton fill />
        ) : query.error ? (
          <SectionError message="Failed to load playback activity." />
        ) : total === 0 ? (
          <ChartEmptyState
            fill
            message={`No playback in ${rangePhrase(range)}`}
            detail="Sessions appear here as soon as someone starts watching."
          />
        ) : (
          <StackedColumnChart
            fill
            buckets={columns}
            seriesLabels={[...PLAYBACK_SERIES_LABELS]}
            ariaLabel={`Playback sessions per ${isDaily ? "day" : "hour"} over ${rangePhrase(range)}, by play method`}
            formatBucket={(t) =>
              isDaily ? formatDayLabel(t) : formatTime(t, { hour: "numeric", minute: undefined })
            }
            totalLabel="Sessions"
          />
        )}
      </CardContent>
    </Card>
  );
}
