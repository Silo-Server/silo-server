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
  if (seconds < 0) return "Added just now";

  if (seconds < MINUTE) return "Added just now";
  if (seconds < HOUR) {
    const m = Math.floor(seconds / MINUTE);
    return `Added ${m} ${m === 1 ? "minute" : "minutes"} ago`;
  }
  if (seconds < DAY) {
    const h = Math.floor(seconds / HOUR);
    return `Added ${h} ${h === 1 ? "hour" : "hours"} ago`;
  }

  const days = Math.floor(seconds / DAY);
  if (days <= 30) {
    return `Added ${days} ${days === 1 ? "day" : "days"} ago`;
  }

  return null;
}
