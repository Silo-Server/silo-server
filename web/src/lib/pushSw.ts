export interface PushPayload {
  id?: number;
  title?: string;
  body?: string;
  link?: string;
  category?: string;
}

export interface SwNotification {
  title: string;
  options: { body: string; data: { link: string }; tag: string };
}

/** Maps a raw push payload to showNotification args; tolerant of partial/missing fields. */
export function buildNotification(payload: PushPayload | null): SwNotification {
  const p = payload ?? {};
  const link = p.link && p.link.length > 0 ? p.link : "/notifications";
  const tag = p.id != null ? `n${p.id}` : "n";
  return {
    title: p.title && p.title.length > 0 ? p.title : "Silo",
    options: { body: p.body ?? "", data: { link }, tag },
  };
}

/** The URL a notification click should open/focus. */
export function clickUrl(data: unknown): string {
  const d = (data ?? {}) as { link?: string };
  return d.link && d.link.length > 0 ? d.link : "/notifications";
}
