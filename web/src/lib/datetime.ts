/**
 * Preference-aware date/time display formatting.
 *
 * All UI code that renders an absolute date or clock time should go through
 * these helpers instead of calling `toLocale*String` directly, so the
 * per-user date/time format settings (issue #303) apply consistently.
 *
 * Preferences live in module state so plain (non-React) helpers can format
 * correctly; `DateTimeFormatProvider` keeps this state in sync with the
 * persisted settings, and `useDateTimeFormat` lets components subscribe.
 */

export const DATE_FORMAT_PREFERENCES = ["auto", "DD/MM/YYYY", "MM/DD/YYYY", "YYYY-MM-DD"] as const;
export const TIME_FORMAT_PREFERENCES = ["auto", "12h", "24h"] as const;

export type DateFormatPreference = (typeof DATE_FORMAT_PREFERENCES)[number];
export type TimeFormatPreference = (typeof TIME_FORMAT_PREFERENCES)[number];

export interface DateTimeFormatPreferences {
  dateFormat: DateFormatPreference;
  timeFormat: TimeFormatPreference;
}

const DEFAULT_PREFERENCES: DateTimeFormatPreferences = {
  dateFormat: "auto",
  timeFormat: "auto",
};

export function parseDateFormatPreference(value: string | null | undefined): DateFormatPreference {
  return DATE_FORMAT_PREFERENCES.includes(value as DateFormatPreference)
    ? (value as DateFormatPreference)
    : "auto";
}

export function parseTimeFormatPreference(value: string | null | undefined): TimeFormatPreference {
  return TIME_FORMAT_PREFERENCES.includes(value as TimeFormatPreference)
    ? (value as TimeFormatPreference)
    : "auto";
}

let preferences: DateTimeFormatPreferences = DEFAULT_PREFERENCES;
const listeners = new Set<() => void>();

export function getDateTimeFormatPreferences(): DateTimeFormatPreferences {
  return preferences;
}

export function setDateTimeFormatPreferences(next: DateTimeFormatPreferences): void {
  if (next.dateFormat === preferences.dateFormat && next.timeFormat === preferences.timeFormat) {
    return;
  }
  preferences = { dateFormat: next.dateFormat, timeFormat: next.timeFormat };
  for (const listener of listeners) {
    listener();
  }
}

export function subscribeDateTimeFormatPreferences(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/**
 * Locale whose composed date formats ("Jun 5, 2026" vs "5 Jun 2026") match the
 * preferred day/month order. Returns undefined for "auto" (browser locale) and
 * for ISO, which has no month-name form and falls back to browser ordering.
 * Use for fragments like `d.toLocaleDateString(preferredDateLocale(), { month: "short", day: "numeric" })`.
 */
export function preferredDateLocale(): string | undefined {
  switch (preferences.dateFormat) {
    case "DD/MM/YYYY":
      return "en-GB";
    case "MM/DD/YYYY":
      return "en-US";
    default:
      return undefined;
  }
}

function toDate(value: Date | string | number): Date | null {
  const date = value instanceof Date ? value : new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

function pad2(value: number): string {
  return String(value).padStart(2, "0");
}

/**
 * Format a calendar date.
 * - "numeric": all-digit form, honoring the exact preferred pattern.
 * - "medium": abbreviated month name ("Jun 5, 2026" / "5 Jun 2026"); the
 *   YYYY-MM-DD preference always renders ISO digits since it has no
 *   month-name form.
 */
export function formatDate(
  value: Date | string | number,
  style: "numeric" | "medium" = "numeric",
): string {
  const date = toDate(value);
  if (!date) {
    return "";
  }
  const { dateFormat } = preferences;
  if (dateFormat === "YYYY-MM-DD") {
    return `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())}`;
  }
  if (style === "numeric") {
    switch (dateFormat) {
      case "DD/MM/YYYY":
        return `${pad2(date.getDate())}/${pad2(date.getMonth() + 1)}/${date.getFullYear()}`;
      case "MM/DD/YYYY":
        return `${pad2(date.getMonth() + 1)}/${pad2(date.getDate())}/${date.getFullYear()}`;
      default:
        return date.toLocaleDateString();
    }
  }
  return date.toLocaleDateString(preferredDateLocale(), {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
}

/**
 * Format a clock time, honoring the 12h/24h preference. Extra Intl options
 * (e.g. `{ second: "2-digit" }`) are merged over the `hour`/`minute` defaults.
 */
export function formatTime(
  value: Date | string | number,
  options?: Intl.DateTimeFormatOptions,
): string {
  const date = toDate(value);
  if (!date) {
    return "";
  }
  const merged: Intl.DateTimeFormatOptions = {
    hour: "numeric",
    minute: "2-digit",
    ...options,
  };
  const { timeFormat } = preferences;
  if (timeFormat === "12h") {
    merged.hourCycle = "h12";
  } else if (timeFormat === "24h") {
    merged.hourCycle = "h23";
    // h23 alone doesn't guarantee zero-padding in every locale; 24h convention
    // is "09:04". Callers may still override with an explicit hour option.
    if (!options?.hour) {
      merged.hour = "2-digit";
    }
  }
  return date.toLocaleTimeString(undefined, merged);
}

/**
 * Format a date plus clock time ("05/06/2026, 15:04:05"). Defaults mirror the
 * bare `toLocaleString()` this replaces: numeric date with seconds.
 */
export function formatDateTime(
  value: Date | string | number,
  options?: { dateStyle?: "numeric" | "medium"; seconds?: boolean },
): string {
  const date = toDate(value);
  if (!date) {
    return "";
  }
  const datePart = formatDate(date, options?.dateStyle ?? "numeric");
  const timePart = formatTime(date, (options?.seconds ?? true) ? { second: "2-digit" } : undefined);
  return `${datePart}, ${timePart}`;
}
