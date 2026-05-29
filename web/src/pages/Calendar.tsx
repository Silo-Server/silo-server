import { useState } from "react";
import { useSearchParams } from "react-router";
import { CalendarDays } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import MediaCarousel from "@/components/MediaCarousel";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useCalendarWeek } from "@/hooks/queries/calendar";
import { useUserLibraries } from "@/hooks/queries/libraries";
import WeekNavigator from "@/components/calendar/WeekNavigator";
import DayGroup from "@/components/calendar/DayGroup";
import { addWeeks, formatDayHeading, getWeekDays, getWeekStart } from "@/lib/calendarWeek";

type CalendarFilter = "following" | "popular" | "trending" | "everything";

// "popular" (server-wide most-watched) is hidden for now — it's sparse until the
// server has enough watch history. Re-add the entry here and in KNOWN_FILTERS to
// surface it; the backend filter remains supported.
const PRESET_OPTIONS: { value: CalendarFilter; label: string }[] = [
  { value: "following", label: "Following" },
  { value: "trending", label: "Trending" },
  { value: "everything", label: "All" },
];

const DEFAULT_PRESET: CalendarFilter = "following";
const PRESET_STORAGE_KEY = "calendar:preset";

// Accept the four presets plus legacy server values so old shared links keep working.
const KNOWN_FILTERS = new Set<string>([
  "following",
  "trending",
  "everything",
  "all",
  "favorites",
  "watchlist",
]);

/** Skeleton rows mirror a full week; enough slides per row to fill wide viewports. */
const CALENDAR_SKELETON_DAY_ROWS = 7;
const CALENDAR_SKELETON_ITEMS_PER_ROW = 18;

function readStoredPreset(): CalendarFilter {
  if (typeof window === "undefined") return DEFAULT_PRESET;
  const stored = window.localStorage.getItem(PRESET_STORAGE_KEY);
  return stored && PRESET_OPTIONS.some((o) => o.value === stored)
    ? (stored as CalendarFilter)
    : DEFAULT_PRESET;
}

function writeStoredPreset(value: string) {
  if (typeof window === "undefined") return;
  window.localStorage.setItem(PRESET_STORAGE_KEY, value);
}

function parseCalendarParams(searchParams: URLSearchParams) {
  const weekRaw = searchParams.get("week");
  const weekStart =
    weekRaw && /^\d{4}-\d{2}-\d{2}$/.test(weekRaw) ? weekRaw : getWeekStart(new Date());
  const rawFilter = searchParams.get("filter");
  const filter = rawFilter && KNOWN_FILTERS.has(rawFilter) ? rawFilter : readStoredPreset();
  const libraryIdRaw = searchParams.get("library");
  const libraryId = libraryIdRaw ? Number(libraryIdRaw) : undefined;
  return { weekStart, filter, libraryId };
}

export default function Calendar() {
  useDocumentTitle("Calendar");
  const [searchParams, setSearchParams] = useSearchParams();
  const { weekStart, filter, libraryId } = parseCalendarParams(searchParams);
  const libraries = useUserLibraries();

  const queryParams = { filter, libraryId };
  const prevWeek = addWeeks(weekStart, -1);
  const nextWeek = addWeeks(weekStart, 1);

  const currentQuery = useCalendarWeek(weekStart, queryParams);

  // Selected day in the week navigator. Drives the highlight in the day strip
  // and is used to scroll to the matching DayGroup when the user taps a day.
  // Derived `activeSelectedDay` ignores stale selections from a different week
  // so we don't need a reset effect when the user navigates weeks.
  const [selectedDay, setSelectedDay] = useState<string | null>(null);
  const visibleWeekDays = getWeekDays(weekStart);
  const activeSelectedDay =
    selectedDay && visibleWeekDays.includes(selectedDay) ? selectedDay : null;

  function setParams(updates: Record<string, string | undefined>) {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        for (const [k, v] of Object.entries(updates)) {
          if (v == null || v === "") next.delete(k);
          else next.set(k, v);
        }
        return next;
      },
      { replace: true },
    );
  }

  const goToWeek = (w: string) => setParams({ week: w });
  const setFilter = (f: string) => {
    writeStoredPreset(f);
    setParams({ filter: f === DEFAULT_PRESET ? undefined : f });
  };
  const setLibrary = (id: string) => setParams({ library: id === "all" ? undefined : id });

  const onSelectDay = (date: string) => {
    setSelectedDay(date);
    // Defer to next frame so the highlighted state renders before we scroll.
    requestAnimationFrame(() => {
      const el = document.getElementById(`day-${date}`);
      if (el) el.scrollIntoView({ behavior: "smooth", block: "start" });
    });
  };

  const days = currentQuery.data ?? [];
  const datesWithEvents = new Set(days.map((d) => d.date));
  const showSelectedEmpty =
    !!activeSelectedDay && !datesWithEvents.has(activeSelectedDay) && !currentQuery.isLoading;

  return (
    <div className="space-y-2 py-4 pb-10 lg:py-8">
      {/* Same horizontal rhythm as Recommendations: one page gutter; MediaCarousel row padding aligns with it */}
      <div className="px-4 pt-4 pb-4 sm:px-6 sm:pt-6 lg:px-10 xl:px-12">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h1 className="text-2xl font-bold tracking-tight sm:text-3xl">Calendar</h1>
          <div className="flex flex-wrap items-center gap-2">
            {/* Preset pills (desktop) */}
            <div
              role="group"
              aria-label="Calendar preset"
              className="surface-panel-subtle hidden items-center gap-0.5 rounded-full p-1 lg:flex"
            >
              {PRESET_OPTIONS.map((opt) => (
                <button
                  key={opt.value}
                  type="button"
                  aria-pressed={filter === opt.value}
                  onClick={() => setFilter(opt.value)}
                  className={`rounded-full px-3 py-1 text-[12px] font-semibold transition-all duration-150 sm:px-4 sm:py-1.5 sm:text-[13px] ${
                    filter === opt.value
                      ? "bg-primary text-primary-foreground shadow-sm"
                      : "text-muted-foreground hover:bg-surface-hover hover:text-foreground"
                  }`}
                >
                  {opt.label}
                </button>
              ))}
            </div>

            {/* Preset dropdown (smaller displays) */}
            <div className="lg:hidden">
              <Select value={filter} onValueChange={setFilter}>
                <SelectTrigger className="border-border/50 bg-surface/60 h-9 w-auto min-w-[130px] rounded-full text-[12px] font-semibold backdrop-blur-sm sm:text-[13px]">
                  <SelectValue placeholder="Following" />
                </SelectTrigger>
                <SelectContent>
                  {PRESET_OPTIONS.map((opt) => (
                    <SelectItem key={opt.value} value={opt.value}>
                      {opt.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            {/* Library filter */}
            {libraries.data && libraries.data.length > 1 && (
              <Select value={libraryId ? String(libraryId) : "all"} onValueChange={setLibrary}>
                <SelectTrigger className="border-border/50 bg-surface/60 h-9 w-auto min-w-[120px] rounded-full text-[12px] font-semibold backdrop-blur-sm sm:min-w-[140px] sm:text-[13px]">
                  <SelectValue placeholder="All Libraries" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">All Libraries</SelectItem>
                  {libraries.data.map((lib) => (
                    <SelectItem key={lib.id} value={String(lib.id)}>
                      {lib.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </div>
        </div>
      </div>

      {/* Sticky week navigator — anchors to the page-level container so the
          containing block spans every DayGroup, keeping it pinned through
          full-page scroll. top-20 clears the mobile header; when the mobile
          header auto-hides on scroll-down (Layout sets data-mobile-header-hidden
          on <html>) we pin closer to the viewport top so the gap stays tight. */}
      <div className="sticky top-20 z-20 px-4 pb-3 transition-[top] duration-200 ease-out sm:px-6 lg:top-2 lg:px-10 xl:px-12 [html[data-mobile-header-hidden=true]_&]:top-2">
        <WeekNavigator
          weekStart={weekStart}
          days={days}
          selectedDay={activeSelectedDay}
          onSelectDay={onSelectDay}
          onPrev={() => goToWeek(prevWeek)}
          onNext={() => goToWeek(nextWeek)}
          onToday={() => goToWeek(getWeekStart(new Date()))}
        />
      </div>

      {showSelectedEmpty && (
        <div className="px-4 sm:px-6 lg:px-10 xl:px-12">
          <div
            className="surface-panel-subtle text-muted-foreground rounded-2xl px-4 py-3 text-[13px]"
            role="status"
          >
            Nothing scheduled for{" "}
            <span className="text-foreground font-semibold">
              {formatDayHeading(activeSelectedDay!)}
            </span>
            .
          </div>
        </div>
      )}

      {currentQuery.isLoading ? (
        <CalendarSkeleton />
      ) : days.length > 0 ? (
        <div className="space-y-6">
          {days.map((day) => (
            <DayGroup key={day.date} day={day} isSelected={activeSelectedDay === day.date} />
          ))}
        </div>
      ) : (
        <div className="px-4 sm:px-6 lg:px-10 xl:px-12">
          <CalendarEmpty filter={filter} onSelectPreset={setFilter} />
        </div>
      )}
    </div>
  );
}

function CalendarSkeleton() {
  return (
    <div className="space-y-6">
      {Array.from({ length: CALENDAR_SKELETON_DAY_ROWS }).map((_, i) => (
        <MediaCarousel
          key={i}
          title="Loading..."
          loading
          skeletonCount={CALENDAR_SKELETON_ITEMS_PER_ROW}
        >
          {null}
        </MediaCarousel>
      ))}
    </div>
  );
}

function CalendarEmpty({
  filter,
  onSelectPreset,
}: {
  filter: string;
  onSelectPreset: (f: string) => void;
}) {
  const isEverything = filter === "everything" || filter === "all";
  return (
    <div className="surface-panel flex min-h-[300px] flex-col items-center justify-center gap-3 rounded-[1.8rem] border-0 px-6 py-16 text-center">
      <CalendarDays className="text-muted-foreground h-10 w-10" strokeWidth={1.5} />
      <p className="text-muted-foreground text-sm">
        {filter === "following"
          ? "Nothing upcoming from shows you follow this week."
          : isEverything
            ? "Nothing scheduled this week."
            : "No events this week for this view."}
      </p>
      {!isEverything && (
        <div className="flex flex-wrap items-center justify-center gap-2">
          <Button
            variant="link"
            size="sm"
            className="text-primary text-sm"
            onClick={() => onSelectPreset("trending")}
          >
            Trending
          </Button>
          <Button
            variant="link"
            size="sm"
            className="text-primary text-sm"
            onClick={() => onSelectPreset("everything")}
          >
            Show everything
          </Button>
        </div>
      )}
    </div>
  );
}
