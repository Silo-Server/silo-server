import type { ReactNode } from "react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminPlaybackActivity } from "@/hooks/queries/admin/dashboardInsights";
import { SectionError } from "../feedback";
import { rangeHours, rangeTitle } from "../range";
import { useWidgetRange } from "../widgetChrome";
import { WidgetRangePicker } from "../WidgetRangePicker";

/**
 * Coarse playback health for the chosen window.
 *
 * Deliberately four counts and one rate: time-to-first-frame and failed-start
 * counts would belong here, but nothing records playback *start* events yet
 * (playback_history_admin only gains a row when a session finalizes). Adding
 * them means capturing start telemetry in internal/playback first — until then
 * this widget shows what the server actually knows rather than an estimate
 * reverse-engineered from logs. See docs/admin-api.md.
 */
export function PlaybackReliabilityWidget() {
  const { range } = useWidgetRange();
  const query = useAdminPlaybackActivity(rangeHours(range));
  const reliability = query.data?.reliability;

  return (
    <Card className="h-full">
      <CardHeader className="flex shrink-0 flex-row items-center justify-between gap-2 space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">
          {rangeTitle("Playback reliability", range)}
        </CardTitle>
        <WidgetRangePicker />
      </CardHeader>
      <CardContent className="min-h-0 flex-1 overflow-y-auto">
        {query.isLoading ? (
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            {Array.from({ length: 4 }).map((_, index) => (
              <Skeleton key={index} className="h-16 rounded-lg" />
            ))}
          </div>
        ) : query.error || !reliability ? (
          <SectionError message="Failed to load playback reliability." />
        ) : (
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <MiniStat label="Started" value={reliability.sessions_started.toLocaleString()} />
            <MiniStat
              label="Transcode starts"
              value={reliability.transcode_starts.toLocaleString()}
            />
            <MiniStat
              label="Completed"
              value={formatCompletionRate(
                reliability.completion_rate,
                reliability.finalized_sessions,
              )}
              detail={
                reliability.finalized_sessions > 0
                  ? `${reliability.completed_sessions.toLocaleString()} of ${reliability.finalized_sessions.toLocaleString()} finished`
                  : "No finished sessions yet"
              }
            />
            <MiniStat label="Profiles" value={reliability.unique_profiles.toLocaleString()} />
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function MiniStat({ label, value, detail }: { label: string; value: string; detail?: ReactNode }) {
  return (
    <div className="bg-surface border-border rounded-lg border p-3">
      <div className="text-[20px] leading-none font-extrabold tracking-tight tabular-nums">
        {value}
      </div>
      <div className="text-muted-foreground mt-1.5 text-[11px] font-medium">{label}</div>
      {detail ? <div className="text-muted-foreground mt-0.5 text-[10px]">{detail}</div> : null}
    </div>
  );
}

/**
 * Completion is measured over finalized sessions only — sessions still playing
 * have not had the chance to finish, so counting them would drag the rate down
 * simply because someone is watching right now.
 */
function formatCompletionRate(rate: number, finalizedSessions: number): string {
  if (finalizedSessions <= 0 || !Number.isFinite(rate)) {
    return "—";
  }
  return `${Math.round(Math.min(Math.max(rate, 0), 1) * 100)}%`;
}
