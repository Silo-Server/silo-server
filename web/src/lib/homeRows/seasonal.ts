import { t } from "@/i18n";
import { appLanguage } from "@/lib/appLocale";

type Config = Record<string, unknown>;

/** A holiday a user adds to a Seasonal Picks row (the server's SeasonalCustomHoliday). */
export interface CustomHoliday {
  name: string;
  /** MM-DD, inclusive. An end before the start wraps across New Year. */
  start: string;
  end: string;
  list_url: string;
  title?: string;
}

export function customHolidaysOf(config: Config): CustomHoliday[] {
  if (!Array.isArray(config.custom_holidays)) return [];
  return config.custom_holidays
    .filter((entry): entry is Record<string, unknown> => !!entry && typeof entry === "object")
    .map((entry) => ({
      ...(entry as Partial<CustomHoliday>),
      name: typeof entry.name === "string" ? entry.name : "",
      start: typeof entry.start === "string" ? entry.start : "",
      end: typeof entry.end === "string" ? entry.end : "",
      list_url: typeof entry.list_url === "string" ? entry.list_url : "",
    }));
}

// Days per month in a leap year: Feb 29 is a valid boundary.
const MONTH_DAYS = [31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];

function validMonthDay(value: string): boolean {
  const match = /^(\d{2})-(\d{2})$/.exec(value);
  if (!match) return false;
  const month = Number(match[1]);
  const day = Number(match[2]);
  return month >= 1 && month <= 12 && day >= 1 && day <= MONTH_DAYS[month - 1]!;
}

/** The month's short name in the interface language ("Oct"), from MM-DD. */
function monthName(value: string): string {
  // Mid-month in UTC, so no time zone moves it into another month.
  const date = new Date(Date.UTC(2000, Number(value.slice(0, 2)) - 1, 15));
  return new Intl.DateTimeFormat(appLanguage(), { month: "short", timeZone: "UTC" }).format(date);
}

/** A window in the catalog's style: "Oct 20–Nov 2", or "Oct 15–31" within a month. */
export function holidayDatesLabel(dates: HolidayDates): string {
  const startDay = Number(dates.start.slice(3));
  const endDay = Number(dates.end.slice(3));
  const startMonth = monthName(dates.start);
  const sameMonth = dates.start.slice(0, 2) === dates.end.slice(0, 2) && startDay <= endDay;
  return sameMonth
    ? t("web.home.rows.seasonal.datesSameMonth", {
        month: startMonth,
        start_day: startDay,
        end_day: endDay,
      })
    : t("web.home.rows.seasonal.datesSpan", {
        start_month: startMonth,
        start_day: startDay,
        end_month: monthName(dates.end),
        end_day: endDay,
      });
}

/** A row's own yearly window for a built-in holiday (the server's SeasonalDates). */
export interface HolidayDates {
  start: string;
  end: string;
}

/** The row's theme_dates, keyed by holiday. */
export function themeDatesOf(config: Config): Record<string, HolidayDates> {
  const out: Record<string, HolidayDates> = {};
  const raw = config.theme_dates;
  if (!raw || typeof raw !== "object") return out;
  for (const [key, entry] of Object.entries(raw as Record<string, unknown>)) {
    if (!entry || typeof entry !== "object") continue;
    const { start, end } = entry as Record<string, unknown>;
    out[key] = {
      start: typeof start === "string" ? start : "",
      end: typeof end === "string" ? end : "",
    };
  }
  return out;
}

/** Why the server would refuse a holiday's own dates, or null when they're complete. */
export function holidayDatesProblem(dates: HolidayDates): string | null {
  if (validMonthDay(dates.start) && validMonthDay(dates.end)) return null;
  return t("web.home.rows.seasonal.ownDatesInvalid");
}

function isMDBListList(value: string): boolean {
  try {
    const raw = value.trim();
    const url = new URL(raw);
    const host = url.hostname.toLowerCase().replace(/\.$/, "");
    // The same rules as the server's ValidateMDBListURL. The server refuses
    // any userinfo, even an empty "https://@host", which URL reports as no
    // username, so look for "@" in the authority as typed.
    const authority = /^[a-z][a-z\d+.-]*:\/\/([^/?#]*)/i.exec(raw)?.[1] ?? "";
    return (
      (url.protocol === "https:" || url.protocol === "http:") &&
      !authority.includes("@") &&
      url.username === "" &&
      url.password === "" &&
      (url.port === "" || url.port === "80" || url.port === "443") &&
      (host === "mdblist.com" || host === "www.mdblist.com") &&
      url.pathname.startsWith("/lists/")
    );
  } catch {
    return false;
  }
}

/** Why the server would refuse this holiday, or null when it's complete. */
export function customHolidayProblem(holiday: CustomHoliday): string | null {
  if (holiday.name.trim() === "") return t("web.home.rows.seasonal.nameMissing");
  if (!validMonthDay(holiday.start) || !validMonthDay(holiday.end)) {
    return t("web.home.rows.seasonal.datesInvalid");
  }
  if (!isMDBListList(holiday.list_url)) {
    return t("web.home.rows.seasonal.listInvalid");
  }
  return null;
}
