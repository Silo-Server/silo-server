import { appLanguage } from "@/lib/appLocale";

let relative: { language: string; format: Intl.RelativeTimeFormat } | null = null;

/** One formatter per interface language; cards call this in long grids. */
function relativeFormat(): Intl.RelativeTimeFormat {
  const language = appLanguage();
  if (relative?.language !== language) {
    relative = { language, format: new Intl.RelativeTimeFormat(language, { numeric: "always" }) };
  }
  return relative.format;
}

const MINUTE = 60;
const HOUR = 3600;
const DAY = 86400;

/**
 * Returns a human-readable relative time string like "Added 3 minutes ago".
 * Returns null for dates older than 30 days.
 */
export function timeAgo(iso: string): string | null {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return null;

  const seconds = Math.floor((Date.now() - date.getTime()) / 1000);
  if (seconds < MINUTE) return "Added just now";

  const ago = relativeFormat();
  if (seconds < HOUR) return `Added ${ago.format(-Math.floor(seconds / MINUTE), "minute")}`;
  if (seconds < DAY) return `Added ${ago.format(-Math.floor(seconds / HOUR), "hour")}`;

  const days = Math.floor(seconds / DAY);
  return days <= 30 ? `Added ${ago.format(-days, "day")}` : null;
}
