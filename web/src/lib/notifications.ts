import { timeAgo } from "@/lib/timeAgo";

/**
 * Relative time for the notifications UI, without the shared util's "Added "
 * prefix (e.g. "1 minute ago" instead of "Added 1 minute ago"). Returns null
 * for dates the shared util can't format (invalid or older than 30 days).
 */
export function notificationTimeAgo(iso: string): string | null {
  const formatted = timeAgo(iso);
  if (formatted === null) return null;
  return formatted.replace(/^Added /, "");
}

/**
 * Returns the given items with announcements first, then the rest, preserving
 * the original relative order within each group (stable partition).
 */
export function partitionAnnouncementsFirst<T extends { category?: string }>(items: T[]): T[] {
  return [
    ...items.filter((n) => n.category === "announcement"),
    ...items.filter((n) => n.category !== "announcement"),
  ];
}
