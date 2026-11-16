import type { AppNotification } from "@/api/types";

/** The day headings the inbox sorts notifications under, newest first. */
export type InboxDay = "today" | "yesterday" | "week" | "older";

/**
 * One inbox row: a series' new-episode notifications under one day heading,
 * newest first, or any other notification on its own.
 */
export type InboxEntry = {
  key: string;
  /** The entry's newest notification, first in notifications. */
  latest: AppNotification;
  notifications: AppNotification[];
  /** Set when the entry is a series' new episodes. */
  seriesId?: string;
};

export type InboxSection = { day: InboxDay; entries: InboxEntry[] };

const DAY_MS = 86_400_000;

/** Which heading a notification created at `created` falls under, counted in
 * local calendar days before `now`. */
export function inboxDay(created: Date, now: Date): InboxDay {
  const startOf = (date: Date) =>
    new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
  // Round, since a day that crosses a daylight-saving change is not 24 hours.
  const days = Math.round((startOf(now) - startOf(created)) / DAY_MS);
  if (days <= 0) return "today";
  if (days === 1) return "yesterday";
  if (days < 7) return "week";
  return "older";
}

const DAY_ORDER: InboxDay[] = ["today", "yesterday", "week", "older"];

/**
 * Groups a newest-first notification list for the inbox: by day heading, and
 * under each heading each series' new episodes into one entry placed where
 * its newest one was. Order is otherwise kept, so pages appended to the list
 * only add to its end.
 */
export function groupInbox(notifications: AppNotification[], now = new Date()): InboxSection[] {
  const sections: InboxSection[] = [];
  const bySeries = new Map<string, InboxEntry>();
  for (const notification of notifications) {
    const created = new Date(notification.created_at);
    const day = Number.isNaN(created.getTime()) ? "older" : inboxDay(created, now);
    let section = sections.find((candidate) => candidate.day === day);
    if (!section) {
      section = { day, entries: [] };
      sections.push(section);
    }
    const seriesId = notification.type === "episode.available" ? notification.series_id : undefined;
    if (!seriesId) {
      section.entries.push({
        key: notification.id,
        latest: notification,
        notifications: [notification],
      });
      continue;
    }
    const groupKey = `${day}:${seriesId}`;
    const group = bySeries.get(groupKey);
    if (group) {
      group.notifications.push(notification);
      continue;
    }
    const entry = { key: groupKey, latest: notification, notifications: [notification], seriesId };
    bySeries.set(groupKey, entry);
    section.entries.push(entry);
  }
  // Headings keep their order even if a bad timestamp puts "older" first.
  return sections.sort((a, b) => DAY_ORDER.indexOf(a.day) - DAY_ORDER.indexOf(b.day));
}
