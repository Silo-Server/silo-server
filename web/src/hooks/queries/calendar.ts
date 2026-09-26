import { useQuery } from "@tanstack/react-query";
import type { components } from "@/api/v2/schema";
import { v2, type V2Query } from "@/api/v2/request";
import { calendarKeys } from "./keys";
import { addDays } from "@/lib/calendarWeek";

export type CalendarEvent = components["schemas"]["CalendarEvent"];
export type CalendarDay = components["schemas"]["CalendarDay"];
export type CalendarFilter = NonNullable<V2Query<"GET /api/v2/calendar">["filter"]>;

export function useCalendarWeek(weekStart: string, params: { filter: string; libraryId?: number }) {
  const timezone = getViewerTimezone();

  return useQuery({
    queryKey: calendarKeys.week(weekStart, params.filter, params.libraryId, timezone),
    queryFn: ({ signal }) =>
      v2("GET /api/v2/calendar", {
        query: {
          start: weekStart,
          end: addDays(weekStart, 6),
          filter: params.filter as CalendarFilter,
          timezone,
          library_id: params.libraryId ? String(params.libraryId) : undefined,
        },
        signal,
      }).then((d): CalendarDay[] => d.events),
    staleTime: 10 * 60 * 1000,
  });
}

function getViewerTimezone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}
