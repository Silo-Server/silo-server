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

const MONTH_NAMES = [
  "Jan",
  "Feb",
  "Mar",
  "Apr",
  "May",
  "Jun",
  "Jul",
  "Aug",
  "Sep",
  "Oct",
  "Nov",
  "Dec",
];

function monthDayParts(value: string): [string, number] {
  return [MONTH_NAMES[Number(value.slice(0, 2)) - 1]!, Number(value.slice(3))];
}

/** A window in the catalog's style: "Oct 20–Nov 2", or "Oct 15–31" within a month. */
export function holidayDatesLabel(dates: HolidayDates): string {
  const [startMonth, startDay] = monthDayParts(dates.start);
  const [endMonth, endDay] = monthDayParts(dates.end);
  return startMonth === endMonth && startDay <= endDay
    ? `${startMonth} ${startDay}–${endDay}`
    : `${startMonth} ${startDay}–${endMonth} ${endDay}`;
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
  return "Enter both dates as MM-DD, or clear them to use the usual dates.";
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
  if (holiday.name.trim() === "") return "Give the holiday a name.";
  if (!validMonthDay(holiday.start) || !validMonthDay(holiday.end)) {
    return "Enter the start and end as MM-DD, such as 07-04.";
  }
  if (!isMDBListList(holiday.list_url)) {
    return "Use a public MDBList list link (https://mdblist.com/lists/…).";
  }
  return null;
}
