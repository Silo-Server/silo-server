import { CalendarDays, CalendarRange, Clock, Infinity as InfinityIcon } from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router";

import ReadingHeatmap from "@/components/stats/ReadingHeatmap";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useReadingHistory } from "@/hooks/queries/readingStats";
import { formatDate } from "@/lib/datetime";
import { formatDuration } from "@/lib/formatDuration";

function fractionLabel(fraction: number): string {
  return `${Math.round(fraction * 100)}%`;
}

function StatCard({ title, seconds, icon }: { title: string; seconds: number; icon: ReactNode }) {
  return (
    <Card className="rounded-2xl border-0 shadow-none">
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-muted-foreground text-sm font-medium">{title}</CardTitle>
        <div className="text-muted-foreground">{icon}</div>
      </CardHeader>
      <CardContent>
        <div className="text-3xl font-semibold tracking-tight">{formatDuration(seconds)}</div>
      </CardContent>
    </Card>
  );
}

export default function ReadingStats() {
  useDocumentTitle("Reading stats");
  const { data, isLoading, isError } = useReadingHistory();

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold tracking-tight sm:text-3xl">Reading stats</h1>

      {isError ? (
        <p className="text-muted-foreground text-sm">Failed to load reading stats.</p>
      ) : isLoading || !data ? (
        <div className="space-y-6">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 md:grid-cols-4">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-[108px] rounded-2xl" />
            ))}
          </div>
          <Skeleton className="h-32 w-full rounded-2xl" />
        </div>
      ) : (
        <>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 md:grid-cols-4">
            <StatCard
              title="Today"
              seconds={data.totals.today_seconds}
              icon={<Clock className="h-4 w-4" />}
            />
            <StatCard
              title="This week"
              seconds={data.totals.week_seconds}
              icon={<CalendarDays className="h-4 w-4" />}
            />
            <StatCard
              title="This month"
              seconds={data.totals.month_seconds}
              icon={<CalendarRange className="h-4 w-4" />}
            />
            <StatCard
              title="All time"
              seconds={data.totals.all_time_seconds}
              icon={<InfinityIcon className="h-4 w-4" />}
            />
          </div>

          <Card className="rounded-2xl border-0 shadow-none">
            <CardHeader>
              <CardTitle className="text-base font-semibold">Activity</CardTitle>
            </CardHeader>
            <CardContent>
              {data.days.length === 0 ? (
                <p className="text-muted-foreground text-sm">No reading activity yet.</p>
              ) : (
                <ReadingHeatmap days={data.days} />
              )}
            </CardContent>
          </Card>

          <Card className="rounded-2xl border-0 shadow-none">
            <CardHeader>
              <CardTitle className="text-base font-semibold">Top books</CardTitle>
            </CardHeader>
            <CardContent>
              {data.books.length === 0 ? (
                <p className="text-muted-foreground text-sm">No books read yet.</p>
              ) : (
                <ul className="divide-border/60 divide-y">
                  {data.books.map((book) => {
                    const isRemoved = !book.title;
                    const label = isRemoved ? "Removed book" : book.title;
                    return (
                      <li
                        key={book.content_id}
                        className="flex items-center justify-between gap-3 py-2.5 text-sm"
                      >
                        {isRemoved ? (
                          <span className="text-muted-foreground min-w-0 truncate italic">
                            {label}
                          </span>
                        ) : (
                          <Link
                            to={`/item/${book.content_id}`}
                            className="hover:text-primary min-w-0 truncate font-medium"
                          >
                            {label}
                          </Link>
                        )}
                        <span className="text-muted-foreground shrink-0 tabular-nums">
                          {formatDuration(book.seconds)}
                        </span>
                      </li>
                    );
                  })}
                </ul>
              )}
            </CardContent>
          </Card>

          <Card className="rounded-2xl border-0 shadow-none">
            <CardHeader>
              <CardTitle className="text-base font-semibold">Recent sessions</CardTitle>
            </CardHeader>
            <CardContent>
              {data.sessions.length === 0 ? (
                <p className="text-muted-foreground text-sm">No reading sessions yet.</p>
              ) : (
                <ul className="divide-border/60 divide-y">
                  {data.sessions.map((session) => {
                    const isRemoved = !session.title;
                    const label = isRemoved ? "Removed book" : session.title;
                    return (
                      <li
                        key={`${session.content_id}-${session.started_at}-${session.start_fraction}`}
                        className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 py-2.5 text-sm"
                      >
                        <div className="min-w-0">
                          <span className="text-muted-foreground text-xs">
                            {formatDate(session.started_at, "medium")}
                          </span>{" "}
                          {isRemoved ? (
                            <span className="text-muted-foreground truncate italic">{label}</span>
                          ) : (
                            <Link
                              to={`/item/${session.content_id}`}
                              className="hover:text-primary truncate font-medium"
                            >
                              {label}
                            </Link>
                          )}
                        </div>
                        <span className="text-muted-foreground shrink-0 tabular-nums">
                          {formatDuration(session.duration_seconds)} ·{" "}
                          {fractionLabel(session.start_fraction)}–
                          {fractionLabel(session.end_fraction)}
                        </span>
                      </li>
                    );
                  })}
                </ul>
              )}
            </CardContent>
          </Card>
        </>
      )}
    </div>
  );
}
