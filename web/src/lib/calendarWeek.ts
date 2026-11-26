import { t } from "@/i18n";
import { appLanguage, formatLocale } from "@/lib/appLocale";

/** Returns the ISO date string (YYYY-MM-DD) of the Monday for the week containing `date`. */
export function getWeekStart(date: Date): string {
  const d = new Date(date);
  const day = d.getDay(); // 0=Sun, 1=Mon, ...
  const diff = day === 0 ? -6 : 1 - day;
  d.setDate(d.getDate() + diff);
  return formatDate(d);
}

/** Adds `n` weeks to a week-start date string and returns the new week-start. */
export function addWeeks(weekStart: string, n: number): string {
  const d = parseDate(weekStart);
  d.setDate(d.getDate() + n * 7);
  return formatDate(d);
}

/** Adds `n` days to a date string and returns the new date. */
export function addDays(dateStr: string, n: number): string {
  const d = parseDate(dateStr);
  d.setDate(d.getDate() + n);
  return formatDate(d);
}

/** Returns an array of 7 ISO date strings for the week starting at `weekStart` (Monday). */
export function getWeekDays(weekStart: string): string[] {
  const d = parseDate(weekStart);
  const days: string[] = [];
  for (let i = 0; i < 7; i++) {
    const day = new Date(d);
    day.setDate(d.getDate() + i);
    days.push(formatDate(day));
  }
  return days;
}

/** Returns a human-readable heading like "Monday, April 7th". */
export function formatDayHeading(dateStr: string): string {
  const d = parseDate(dateStr);
  const weekday = d.toLocaleDateString(formatLocale(), { weekday: "long" });
  const month = d.toLocaleDateString(formatLocale(), { month: "long" });
  const heading = t("web.calendar.dayHeading", { weekday, month, day: d.getDate() });
  // Languages that write weekdays in lower case ("miércoles, 4 de noviembre")
  // still start a heading with a capital; skip any leading isolate marks.
  // English keeps its bytes, even with a browser that names days in another
  // language.
  if (appLanguage() === "en") return heading;
  return heading.replace(
    /^([\u2066-\u2069\u200e\u200f]*)(\p{Ll})/u,
    (_, marks: string, letter: string) => marks + letter.toLocaleUpperCase(formatLocale()),
  );
}

// Phone-width week-strip buttons fit short weekday names of up to four
// characters ("Wed", "qua.", "mer.") at every text size; longer ones (pt-PT
// "segunda", sw "Jumatatu", lv "Ceturtd.", pl "niedz.") run into their
// neighbours, so those locales use narrow names ("S T Q Q S S D"). Narrow
// names that repeat (mg "A A T A A Z A") still fit, and the day number below
// tells the days apart.
const COMPACT_SHORT_NAME_MAX_LENGTH = 4;
const compactFormats = new Map<string | undefined, Intl.DateTimeFormat>();

function compactWeekdayFormat(): Intl.DateTimeFormat {
  const locale = formatLocale();
  let format = compactFormats.get(locale);
  if (!format) {
    const short = new Intl.DateTimeFormat(locale, { weekday: "short" });
    // 4 January 2026 is a Sunday: the seven days from it are one of each weekday.
    const fits = Array.from({ length: 7 }, (_, day) =>
      short.format(new Date(2026, 0, 4 + day)),
    ).every((name) => [...name].length <= COMPACT_SHORT_NAME_MAX_LENGTH);
    format = fits ? short : new Intl.DateTimeFormat(locale, { weekday: "narrow" });
    compactFormats.set(locale, format);
  }
  return format;
}

/**
 * Returns a short day label like "Mon", the label for a phone-width week strip
 * (the short one when every weekday's fits, otherwise the narrow "M"), and the
 * day number like "7".
 */
export function formatShortDay(dateStr: string): {
  label: string;
  compactLabel: string;
  day: number;
} {
  const d = parseDate(dateStr);
  return {
    label: d.toLocaleDateString(formatLocale(), { weekday: "short" }),
    compactLabel: compactWeekdayFormat().format(d),
    day: d.getDate(),
  };
}

/** Returns the month + year label like "April 2026". */
export function formatMonthYear(dateStr: string): string {
  const d = parseDate(dateStr);
  return d.toLocaleDateString(formatLocale(), { month: "long", year: "numeric" });
}

/** Compact range for a Mon–Sun week, e.g. "Apr 6 – 12, 2026" or spanning two months. */
export function formatWeekRangeLabel(weekStart: string): string {
  const start = parseDate(weekStart);
  const end = new Date(start);
  end.setDate(end.getDate() + 6);
  const sameMonth =
    start.getMonth() === end.getMonth() && start.getFullYear() === end.getFullYear();
  if (sameMonth) {
    const month = start.toLocaleDateString(formatLocale(), { month: "short" });
    return t("web.calendar.weekRangeSameMonth", {
      month,
      start_day: start.getDate(),
      end_day: end.getDate(),
      year: start.getFullYear(),
    });
  }
  return t("web.calendar.weekRange", {
    start: start.toLocaleDateString(formatLocale(), { month: "short", day: "numeric" }),
    end: end.toLocaleDateString(formatLocale(), {
      month: "short",
      day: "numeric",
      year: "numeric",
    }),
  });
}

/** Checks if a date string is today. */
export function isToday(dateStr: string): boolean {
  return dateStr === formatDate(new Date());
}

/** Formats a Date to YYYY-MM-DD. */
function formatDate(d: Date): string {
  const year = d.getFullYear();
  const month = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

/** Parses a YYYY-MM-DD string to a local Date. */
function parseDate(dateStr: string): Date {
  const parts = dateStr.split("-").map(Number);
  return new Date(parts[0]!, parts[1]! - 1, parts[2]!);
}
