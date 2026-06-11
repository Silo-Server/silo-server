# Home Notification Bars — Design

Date: 2026-06-11
Status: approved (design); implementation to follow

## Summary

Surface notifications at the top of the Home page so they're seen without opening the inbox tab, with system-level **announcements** visually distinguished from personal notifications. Two slim, dismissible bars stack at the top of Home: an **announcement bar** (distinct accent + 📢 + "Announcement" label) above a **notification strip** (the most recent unread personal notification). Both reuse existing hooks; no server changes. Web-only, additive.

## Goals

- Show the most recent unread notification high on the Home page, not only behind the bell/inbox.
- Make admin **announcements** clearly distinct from request/content/system/admin notifications, via a separate styled bar.
- Dismiss (✕) marks read and advances to the next unread; bars hide when nothing's unread.
- Reuse the Phase-2 notifications hooks; inherit per-profile + child filtering and live WebSocket updates for free.

## Non-goals

- No server/API changes (the data already distinguishes `category: "announcement"`).
- No change to the bell, inbox page, or toast (carrying the 📢 styling there is a possible follow-up, out of scope here).
- Not app-wide — Home page only.

## Components

Two components in `web/src/components/home/` (or alongside Home), each small and independently testable, plus one mount edit.

### AnnouncementBar (`web/src/components/AnnouncementBar.tsx`)
- Data: `useNotificationsList({ unread: true, category: "announcement" })` → the first (newest) item. Reliably finds the latest unread announcement regardless of how many other notifications are unread.
- Render: a distinct bar using the app's accent tokens (reads as a system notice, not a personal row) — a 📢 icon, an "ANNOUNCEMENT" label, the title, truncated body, relative time, and a ✕.
- Empty: renders nothing when there's no unread announcement.
- ✕: `useMarkRead().mutate({ ids: [id] })` → the unread query updates → the next unread announcement shows, or the bar hides. Marking read also drops the bell badge (shared cache).
- Click (anywhere but ✕): navigate to the notification's `link` (or `/notifications` when it has none) and mark it read.

### NotificationStrip (`web/src/components/NotificationStrip.tsx`)
- Data: `useNotificationsList({ unread: true })` → the first item whose `category !== "announcement"` (so announcements live only in the bar above, never duplicated here).
- Render: the slim strip — 🔔 icon, title, truncated body, relative time, ✕. Lighter styling than the announcement bar.
- Empty: renders nothing when there's no unread non-announcement.
- ✕ / click: same behavior as the bar (mark read + advance / navigate + mark read).

### Mount (`web/src/pages/Home.tsx`)
At the top of the Home content region (near the existing `heroSlot` / `TasteSeedBanner`), render `<AnnouncementBar />` then `<NotificationStrip />` as the first elements, so they sit above the content rows. When both are empty, Home renders exactly as today.

## Data flow & shared behavior

- Both bars read through TanStack Query; the announcement bar's query (`{unread:true, category:"announcement"}`) and the strip's (`{unread:true}`) are distinct cache keys — two small fetches, both already used elsewhere.
- Live updates: `useNotificationsLive` already invalidates the notifications list on `notification.created`, so both bars reactively show the newest unread without a reload.
- Filtering: the list endpoint already scopes to the active profile and suppresses child-restricted categories server-side, so the bars inherit correct per-profile behavior.
- The relative-time helper is the existing `timeAgo` (with the absolute-date fallback for old timestamps).

## Error handling

- While the query is loading, render nothing (no skeleton flash for a slim bar).
- A mark-read failure surfaces the existing mutation `onError` toast; the bar stays until the next successful read.
- A notification with no `link` navigates to `/notifications` rather than a dead link.

## Testing

- `AnnouncementBar.test.tsx`: renders the latest unread announcement with the 📢/label styling; ✕ calls `markRead({ids:[id]})`; clicking navigates (mocked `useNavigate`) to the link and marks read; renders nothing when no unread announcement.
- `NotificationStrip.test.tsx`: shows the newest unread non-announcement (skips an announcement at the head of the list); ✕ marks read; click navigates + marks read; renders nothing when no unread non-announcement.
- Existing suites stay green; tsc + lint clean.

## Future work

- Carry the 📢 announcement styling into the bell dropdown and inbox rows for a consistent identity.
- Optional: a per-user "hide the home bars" preference if they prove noisy.
