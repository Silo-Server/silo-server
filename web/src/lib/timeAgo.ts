const MINUTE = 60;
const HOUR = 3600;
const DAY = 86400;

/**
 * Returns a human-readable relative time string like "Added 3 minutes ago".
 * @param iso ISO date string
 * @param prefix Optional prefix (default: "Added"). If empty string, no prefix is prepended.
 * Returns null for dates older than 30 days.
 */
export function timeAgo(iso: string, prefix = "Added"): string | null {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return null;

  const seconds = Math.floor((Date.now() - date.getTime()) / 1000);

  const withPrefix = (s: string) => (prefix ? `${prefix} ${s}` : s);

  if (seconds < 0) return withPrefix("just now");

  if (seconds < MINUTE) return withPrefix("just now");
  if (seconds < HOUR) {
    const m = Math.floor(seconds / MINUTE);
    return withPrefix(`${m} ${m === 1 ? "minute" : "minutes"} ago`);
  }
  if (seconds < DAY) {
    const h = Math.floor(seconds / HOUR);
    return withPrefix(`${h} ${h === 1 ? "hour" : "hours"} ago`);
  }

  const days = Math.floor(seconds / DAY);
  if (days <= 30) {
    return withPrefix(`${days} ${days === 1 ? "day" : "days"} ago`);
  }

  return null;
}
