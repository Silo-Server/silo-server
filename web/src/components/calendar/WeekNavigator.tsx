import { ChevronLeft, ChevronRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useAppLanguage, useT } from "@/i18n";
import { getWeekDays, formatShortDay, formatWeekRangeLabel, isToday } from "@/lib/calendarWeek";
import type { CalendarDay } from "@/hooks/queries/calendar";

interface WeekNavigatorProps {
  weekStart: string;
  days: CalendarDay[];
  selectedDay: string | null;
  onSelectDay: (date: string) => void;
  onPrev: () => void;
  onNext: () => void;
  onToday: () => void;
}

export default function WeekNavigator({
  weekStart,
  days,
  selectedDay,
  onSelectDay,
  onPrev,
  onNext,
  onToday,
}: WeekNavigatorProps) {
  const t = useT();
  // The labels come from plain helpers; this re-renders them on a language change.
  useAppLanguage();
  const weekDays = getWeekDays(weekStart);
  const datesWithEvents = new Set(days.map((d) => d.date));
  const rangeLabel = formatWeekRangeLabel(weekStart);

  return (
    // Sticky positioning is applied by the parent so the containing block spans
    // the full page scroll height — keeping the navigator pinned past the heading.
    <div
      className="glass-dark border-border/50 rounded-2xl border p-2 shadow-[0_18px_40px_-28px_rgba(0,0,0,0.7)] sm:p-3"
      role="navigation"
      aria-label={t("web.calendar.weekOf", { range: rangeLabel })}
    >
      <div className="mb-2 flex items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-0.5 sm:gap-1">
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8 shrink-0 rounded-full"
            onClick={onPrev}
            aria-label={t("web.calendar.previousWeek")}
          >
            <ChevronLeft className="h-4 w-4" />
          </Button>
          <p className="text-foreground/90 truncate px-1 text-[12px] font-semibold tracking-wide tabular-nums sm:text-sm">
            {rangeLabel}
          </p>
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8 shrink-0 rounded-full"
            onClick={onNext}
            aria-label={t("web.calendar.nextWeek")}
          >
            <ChevronRight className="h-4 w-4" />
          </Button>
        </div>
        <Button
          variant="outline"
          size="sm"
          className="h-8 shrink-0 rounded-full px-3 text-[11px] font-semibold sm:text-[12px]"
          onClick={onToday}
        >
          {t("web.calendar.todayButton")}
        </Button>
      </div>

      <div className="grid grid-cols-7 gap-1 sm:gap-1.5">
        {weekDays.map((dateStr) => {
          const { label, day } = formatShortDay(dateStr);
          const today = isToday(dateStr);
          const hasEvents = datesWithEvents.has(dateStr);
          const isSelected = selectedDay === dateStr;

          const variant = isSelected
            ? "bg-primary text-primary-foreground shadow-sm"
            : today
              ? "bg-primary/15 text-primary ring-1 ring-primary/25 hover:bg-primary/20"
              : "text-muted-foreground hover:bg-surface-hover hover:text-foreground";

          return (
            <button
              key={dateStr}
              type="button"
              aria-pressed={isSelected}
              aria-current={today ? "date" : undefined}
              aria-label={t(
                hasEvents ? "web.calendar.dayLabelWithEvents" : "web.calendar.dayLabel",
                { weekday: label, day: String(day) },
              )}
              onClick={() => onSelectDay(dateStr)}
              className={`group/day focus-visible:ring-primary/50 flex min-h-[3.25rem] flex-col items-center justify-center gap-0.5 rounded-xl px-0.5 py-2 transition-all duration-150 outline-none focus-visible:ring-2 active:scale-[0.97] sm:min-h-[3.5rem] sm:gap-1 sm:px-2 sm:py-2.5 ${variant}`}
            >
              <span
                className={`text-[10px] font-medium tracking-wide uppercase sm:text-[11px] ${
                  isSelected ? "text-primary-foreground/85" : ""
                }`}
              >
                {label}
              </span>
              <span
                className={`text-[14px] font-bold tabular-nums sm:text-base ${
                  isSelected
                    ? "text-primary-foreground"
                    : today
                      ? "text-primary"
                      : "text-foreground"
                }`}
              >
                {day}
              </span>
              <span
                aria-hidden
                className={`mt-0.5 h-1 w-1 rounded-full transition-opacity ${
                  hasEvents
                    ? isSelected
                      ? "bg-primary-foreground/85 opacity-100"
                      : today
                        ? "bg-primary opacity-100"
                        : "bg-muted-foreground/60 opacity-100"
                    : "opacity-0"
                }`}
              />
            </button>
          );
        })}
      </div>
    </div>
  );
}
