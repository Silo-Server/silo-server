import { useMemo, type ComponentProps } from "react";
import { ChevronDown, ChevronLeft, ChevronRight, ChevronUp } from "lucide-react";
import { Direction } from "radix-ui";
import { DateLib, DayPicker, type Formatters, type Labels } from "react-day-picker";

import { buttonVariants } from "@/components/ui/button";
import { useT, type Translate } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";
import { cn } from "@/lib/utils";

// The full date a screen reader says for a day ("Friday, November 6th, 2026"),
// as react-day-picker formats it; dates still follow its English locale.
function fullDate(
  date: Date,
  options?: ConstructorParameters<typeof DateLib>[0],
  dateLib?: DateLib,
) {
  return (dateLib ?? new DateLib(options)).format(date, "PPPP");
}

// react-day-picker's built-in English labels, from the catalog. The grid,
// weekday and navigation-bar labels are dates or empty and keep its defaults.
function calendarLabels(t: Translate): Partial<Labels> {
  return {
    labelPrevious: () => t("web.common.ui.calendar.previousMonth"),
    labelNext: () => t("web.common.ui.calendar.nextMonth"),
    labelMonthDropdown: () => t("web.common.ui.calendar.monthDropdown"),
    labelYearDropdown: () => t("web.common.ui.calendar.yearDropdown"),
    labelWeekNumberHeader: () => t("web.common.ui.calendar.weekNumberHeader"),
    labelWeekNumber: (weekNumber) => t("web.common.ui.calendar.weekNumber", { number: weekNumber }),
    labelDayButton: (date, modifiers, options, dateLib) => {
      const full = fullDate(date, options, dateLib);
      if (modifiers.today && modifiers.selected) {
        return t("web.common.ui.calendar.dayTodaySelected", { date: full });
      }
      if (modifiers.today) return t("web.common.ui.calendar.dayToday", { date: full });
      if (modifiers.selected) return t("web.common.ui.calendar.daySelected", { date: full });
      return full;
    },
    labelGridcell: (date, modifiers, options, dateLib) => {
      const full = fullDate(date, options, dateLib);
      return modifiers?.today ? t("web.common.ui.calendar.dayToday", { date: full }) : full;
    },
  };
}

// Weekday headers sit in fixed 2rem day columns. Short names run to eight
// letters (ar الثلاثاء, lv Ceturtd.) and overlap their neighbours; narrow
// names ("S M T W T F S") fit in every locale. A few locales' narrow names
// barely differ (tzm "A A A A A A A"), and their short names are short enough
// to fit, so those keep them. The header row is hidden from screen readers,
// which hear each day's full date instead.
function weekdayNameFormat(): Intl.DateTimeFormat {
  const narrow = new Intl.DateTimeFormat(formatLocale(), { weekday: "narrow" });
  // 4 January 2026 is a Sunday: the seven days from it are one of each weekday.
  const names = new Set(
    Array.from({ length: 7 }, (_, day) => narrow.format(new Date(2026, 0, 4 + day))),
  );
  return names.size < 4 ? new Intl.DateTimeFormat(formatLocale(), { weekday: "short" }) : narrow;
}

// Month captions, month dropdown entries and weekday headers are shown text:
// format them in the format locale, not react-day-picker's English default.
// The grid is Gregorian, so month names are too, even where the locale's own
// calendar is not (fa-IR, th-TH).
const calendarFormatters: Partial<Formatters> = {
  formatCaption: (month) =>
    new Intl.DateTimeFormat(formatLocale(), {
      month: "long",
      year: "numeric",
      calendar: "gregory",
    }).format(month),
  formatMonthDropdown: (month) =>
    new Intl.DateTimeFormat(formatLocale(), { month: "long", calendar: "gregory" }).format(month),
  formatWeekdayName: (weekday) => weekdayNameFormat().format(weekday),
};

// Week data from Intl.Locale: getWeekInfo(), or the weekInfo property some
// browsers shipped first. TypeScript's lib doesn't declare either yet.
type LocaleWithWeekInfo = Intl.Locale & {
  getWeekInfo?: () => { firstDay: number };
  weekInfo?: { firstDay: number };
};

type WeekStart = NonNullable<ComponentProps<typeof DayPicker>["weekStartsOn"]>;

// The format locale's first day of the week (Monday in pl, Saturday in ar).
// firstDay counts 1 (Monday) to 7 (Sunday); DayPicker counts 0 (Sunday) to 6.
// Without week data, DayPicker keeps its Sunday default. The format locale's
// own tag keeps any -u-fw- first-day override; resolving drops it.
function weekStartsOn(): WeekStart | undefined {
  const locale: LocaleWithWeekInfo = new Intl.Locale(
    formatLocale() ?? new Intl.DateTimeFormat().resolvedOptions().locale,
  );
  const firstDay = (locale.getWeekInfo?.() ?? locale.weekInfo)?.firstDay;
  return firstDay === undefined ? undefined : ((firstDay % 7) as WeekStart);
}

function Calendar({
  className,
  classNames,
  showOutsideDays = true,
  labels,
  formatters,
  ...props
}: ComponentProps<typeof DayPicker>) {
  const t = useT();
  const translatedLabels = useMemo(() => ({ ...calendarLabels(t), ...labels }), [t, labels]);
  // DayPicker turns its arrow keys for right to left; its month chevrons mirror
  // through the app's icon rule (the default nav always draws left and right).
  const dir = Direction.useDirection();
  return (
    <DayPicker
      dir={dir}
      weekStartsOn={weekStartsOn()}
      showOutsideDays={showOutsideDays}
      labels={translatedLabels}
      formatters={{ ...calendarFormatters, ...formatters }}
      className={cn("p-3", className)}
      classNames={{
        months: "relative flex flex-col gap-4 sm:flex-row",
        month: "flex w-full flex-col gap-4",
        month_caption: "flex h-7 items-center justify-center",
        caption_label: "text-sm font-medium",
        nav: "absolute inset-x-0 top-0 flex items-center justify-between",
        button_previous: cn(
          buttonVariants({ variant: "outline" }),
          "size-7 bg-transparent p-0 opacity-60 hover:opacity-100",
        ),
        button_next: cn(
          buttonVariants({ variant: "outline" }),
          "size-7 bg-transparent p-0 opacity-60 hover:opacity-100",
        ),
        month_grid: "w-full border-collapse",
        weekdays: "flex",
        weekday: "text-muted-foreground w-8 text-[0.8rem] font-normal",
        week: "mt-2 flex w-full",
        day: "relative size-8 p-0 text-center text-sm",
        day_button: cn(
          buttonVariants({ variant: "ghost" }),
          "size-8 p-0 font-normal aria-selected:opacity-100",
        ),
        selected:
          "rounded-md [&>button]:bg-primary [&>button]:text-primary-foreground [&>button]:hover:bg-primary [&>button]:hover:text-primary-foreground",
        today: "rounded-md [&>button:not([aria-selected])]:bg-accent",
        outside: "text-muted-foreground",
        disabled: "text-muted-foreground opacity-50",
        hidden: "invisible",
        ...classNames,
      }}
      components={{
        Chevron: ({ orientation, className: chevronClassName }) => {
          const Icon =
            orientation === "left"
              ? ChevronLeft
              : orientation === "right"
                ? ChevronRight
                : orientation === "up"
                  ? ChevronUp
                  : ChevronDown;
          return <Icon className={cn("size-4", chevronClassName)} aria-hidden="true" />;
        },
      }}
      {...props}
    />
  );
}

export { Calendar };
