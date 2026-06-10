# Core Notifications — Web UI Implementation Plan (Phase 2 of 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Notifications UI in the silo-server web app: unread badge (sidebar + mobile header bell with dropdown), live toasts from the events WebSocket, an inbox page, per-category preference toggles in settings, and an admin announcements page.

**Architecture:** A `notifications` query-hook module talks to the Phase-1 REST API; one live-wiring hook subscribes to the `notifications` WS channel via the existing `RealtimeEventsProvider` ref-counted `useEventChannel`, firing sonner toasts and patching React Query caches. UI components consume only the hooks. Desktop surface is an `AppSidebar` item with badge (no desktop top bar exists); mobile surface is a bell + popover in the `Layout` header.

**Tech Stack:** React 19, React Router v7, TanStack Query, sonner, lucide-react, shadcn/ui primitives (Popover, Switch, Dialog), Vitest + RTL.

**Spec:** `docs/superpowers/specs/2026-06-10-core-notifications-design.md` (Web UI section). Server API shipped in Phase 1.

Commands assume the repository root is the cwd; web commands run in `web/`. `pnpm` is the package manager.

---

## Server API contract (frozen, Phase 1)

The `api()` client prefixes `/api/v1` and adds auth + `X-Profile-Id` headers automatically.

```
GET    /notifications?unread=1&category=&cursor=&limit=   → {"items": Notification[], "next_cursor": number|null}
GET    /notifications/unread-count                        → {"count": number}
POST   /notifications/read         {"ids":[...]} | {"all":true}  → 204
POST   /notifications/{id}/dismiss                        → 204 | 404
GET    /notifications/preferences                         → {"preferences":[{"category","enabled"}]}
PUT    /notifications/preferences  {"preferences":[...]}  → 204 | 400
GET    /admin/announcements                               → {"items": Announcement[]}
POST   /admin/announcements        {title, body, audience:{all|user_ids|library_ids}, expires_at?} → 201
DELETE /admin/announcements/{id}                          → 204 | 404
```

Notification JSON: `{id, user_id, profile_id?, category, type, title, body, link?, item_id?, created_at, read_at?, expires_at?}`. Categories: `request | content | announcement | system | admin`; preference categories additionally `content_digest` (never a notification category). WS: channel `"notifications"`, event `"notification.created"`, `data` = the Notification; subscribe snapshot `data` = `{"unread_count": number}`.

## File map

| File | Action | Responsibility |
|---|---|---|
| `web/src/api/types.ts` | modify | `AppNotification`, `NotificationPreference`, `Announcement`, `AnnouncementAudience` types; add `"notifications"` to `EventChannel` |
| `web/src/hooks/queries/keys.ts` | modify | `notificationKeys` factory |
| `web/src/hooks/queries/notifications.ts` | create | all query/mutation hooks (user + admin) |
| `web/src/hooks/useNotificationsLive.ts` | create | WS subscription → toast + cache patching |
| `web/src/hooks/useNotificationsLive.test.ts` | create | live-hook tests |
| `web/src/components/NotificationBell.tsx` | create | badge + popover dropdown (mobile header) |
| `web/src/components/NotificationBell.test.tsx` | create | bell tests |
| `web/src/components/Layout.tsx` | modify | mount bell in mobile header; mount live hook |
| `web/src/components/AppSidebar.tsx` | modify | Notifications nav item with badge |
| `web/src/pages/Notifications.tsx` | create | inbox page |
| `web/src/pages/Notifications.test.tsx` | create | inbox tests |
| `web/src/pages/settings/NotificationsSettings.tsx` | create | preference toggles |
| `web/src/pages/SettingsLayout.tsx` | modify | nav section entry |
| `web/src/pages/AdminAnnouncements.tsx` | create | admin CRUD page |
| `web/src/components/AdminSidebar.tsx` | modify | announcements nav item |
| `web/src/App.tsx` | modify | routes: `/notifications`, admin `announcements`, settings `notifications` |

---

### Task 1: API types and event channel

**Files:**
- Modify: `web/src/api/types.ts` (EventChannel union ~line 2198; types appended near other domain types)

- [ ] **Step 1: Add the types**

Add `"notifications"` to the `EventChannel` union. Then add (named `AppNotification` to avoid colliding with the DOM `Notification` type — verify no existing `AppNotification` symbol first with grep):

```typescript
export type NotificationCategory = "request" | "content" | "announcement" | "system" | "admin";

export type NotificationPreferenceCategory = NotificationCategory | "content_digest";

export interface AppNotification {
  id: number;
  user_id: number;
  profile_id?: string;
  category: NotificationCategory;
  type: string;
  title: string;
  body: string;
  link?: string;
  item_id?: string;
  created_at: string;
  read_at?: string;
  expires_at?: string;
}

export interface NotificationListResponse {
  items: AppNotification[] | null;
  next_cursor: number | null;
}

export interface NotificationPreference {
  category: NotificationPreferenceCategory;
  enabled: boolean;
}

export interface AnnouncementAudience {
  all?: boolean;
  user_ids?: number[];
  library_ids?: number[];
}

export interface Announcement {
  id: number;
  title: string;
  body: string;
  audience: AnnouncementAudience;
  created_by?: number;
  created_at: string;
  expires_at?: string;
}
```

- [ ] **Step 2: Typecheck and commit**

```bash
cd web && pnpm exec tsc -b --noEmit 2>&1 | head -5
git add src/api/types.ts && git commit -m "feat(web): notification API types and event channel"
```

(If the repo's typecheck command differs — check package.json scripts for `typecheck`/`build` — use that; `pnpm run build` also typechecks via `tsc -b`.)

---

### Task 2: Query keys and hooks

**Files:**
- Modify: `web/src/hooks/queries/keys.ts`
- Create: `web/src/hooks/queries/notifications.ts`

- [ ] **Step 1: Keys** — follow the existing factory style in keys.ts:

```typescript
export const notificationKeys = {
  all: ["notifications"] as const,
  list: (filters: { unread?: boolean; category?: string }) =>
    [...notificationKeys.all, "list", filters] as const,
  unreadCount: () => [...notificationKeys.all, "unread-count"] as const,
  preferences: () => [...notificationKeys.all, "preferences"] as const,
  announcements: () => ["admin", "announcements"] as const,
};
```

- [ ] **Step 2: Hooks** — `web/src/hooks/queries/notifications.ts`. Mirror the conventions in `web/src/hooks/queries/admin/users.ts` (api client import path, toast usage, invalidation):

```typescript
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { api } from "@/api/client";
import type {
  Announcement,
  AnnouncementAudience,
  AppNotification,
  NotificationListResponse,
  NotificationPreference,
} from "@/api/types";
import { notificationKeys } from "@/hooks/queries/keys";

const PAGE_SIZE = 25;

export function useNotificationsList(filters: { unread?: boolean; category?: string } = {}) {
  return useInfiniteQuery({
    queryKey: notificationKeys.list(filters),
    queryFn: async ({ pageParam }) => {
      const params = new URLSearchParams();
      params.set("limit", String(PAGE_SIZE));
      if (filters.unread) params.set("unread", "1");
      if (filters.category) params.set("category", filters.category);
      if (pageParam) params.set("cursor", String(pageParam));
      return api<NotificationListResponse>(`/notifications?${params.toString()}`);
    },
    initialPageParam: 0,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
}

export function useUnreadCount() {
  return useQuery({
    queryKey: notificationKeys.unreadCount(),
    queryFn: () => api<{ count: number }>("/notifications/unread-count"),
    select: (d) => d.count,
  });
}

export function useMarkRead() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: { ids?: number[]; all?: boolean }) =>
      api("/notifications/read", { method: "POST", body: JSON.stringify(body) }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: notificationKeys.all });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to mark read");
    },
  });
}

export function useDismissNotification() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api(`/notifications/${id}/dismiss`, { method: "POST" }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: notificationKeys.all });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to dismiss");
    },
  });
}

export function useNotificationPreferences() {
  return useQuery({
    queryKey: notificationKeys.preferences(),
    queryFn: () => api<{ preferences: NotificationPreference[] }>("/notifications/preferences"),
    select: (d) => d.preferences,
  });
}

export function useSetNotificationPreferences() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (preferences: NotificationPreference[]) =>
      api("/notifications/preferences", { method: "PUT", body: JSON.stringify({ preferences }) }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: notificationKeys.preferences() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save preferences");
    },
  });
}

export function useAnnouncements() {
  return useQuery({
    queryKey: notificationKeys.announcements(),
    queryFn: () => api<{ items: Announcement[] | null }>("/admin/announcements"),
    select: (d) => d.items ?? [],
  });
}

export function useCreateAnnouncement() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: {
      title: string;
      body: string;
      audience: AnnouncementAudience;
      expires_at?: string;
    }) => api<Announcement>("/admin/announcements", { method: "POST", body: JSON.stringify(body) }),
    onSuccess: () => {
      toast.success("Announcement published");
      void queryClient.invalidateQueries({ queryKey: notificationKeys.announcements() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to publish");
    },
  });
}

export function useDeleteAnnouncement() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api(`/admin/announcements/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Announcement deleted");
      void queryClient.invalidateQueries({ queryKey: notificationKeys.announcements() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete");
    },
  });
}
```

Check `api()`'s actual error behavior (does it throw on non-2xx with a message? read `web/src/api/client.ts:343-380`) and the import alias style (`@/` vs relative — match the queries directory's existing imports).

- [ ] **Step 3: Typecheck + commit**

```bash
cd web && pnpm exec tsc -b --noEmit 2>&1 | head -5
git add src/hooks/queries/ && git commit -m "feat(web): notification query hooks"
```

---

### Task 3: Live wiring hook (WS → toasts + cache)

**Files:**
- Create: `web/src/hooks/useNotificationsLive.ts`
- Test: `web/src/hooks/useNotificationsLive.test.ts`

- [ ] **Step 1: Write the failing test**

Mock `useEventChannel` (from `@/components/realtimeEventsContext`) to capture handlers, mock `sonner`'s toast, wrap in a QueryClientProvider. Follow the mocking style of `web/src/components/RealtimeEventsProvider.test.tsx` (vi.mock + vi.hoisted).

```typescript
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const { capturedHandlers, toastMock } = vi.hoisted(() => ({
  capturedHandlers: {} as { onEvent?: (m: unknown) => void; onSnapshot?: (m: unknown) => void },
  toastMock: vi.fn(),
}));

vi.mock("@/components/realtimeEventsContext", () => ({
  useEventChannel: (_channel: string, handlers: typeof capturedHandlers) => {
    capturedHandlers.onEvent = handlers?.onEvent;
    capturedHandlers.onSnapshot = handlers?.onSnapshot;
  },
}));

vi.mock("sonner", () => ({ toast: toastMock }));

const profileState = vi.hoisted(() => ({ profile: { id: "p-1", is_child: false } }));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => profileState }));

import { useNotificationsLive } from "./useNotificationsLive";
import { notificationKeys } from "@/hooks/queries/keys";

function setup() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(notificationKeys.unreadCount(), { count: 1 });
  renderHook(() => useNotificationsLive(), {
    wrapper: ({ children }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    ),
  });
  return queryClient;
}

function frame(data: object) {
  return { type: "event", channel: "notifications", event: "notification.created", data };
}

describe("useNotificationsLive", () => {
  it("toasts and bumps unread count on notification.created", () => {
    const qc = setup();
    capturedHandlers.onEvent?.(
      frame({ id: 5, category: "request", title: "Request approved", body: "Dune", profile_id: "p-1" }),
    );
    expect(toastMock).toHaveBeenCalledWith("Request approved", expect.anything());
    expect(qc.getQueryData(notificationKeys.unreadCount())).toEqual({ count: 2 });
  });

  it("ignores frames for another profile", () => {
    setup();
    capturedHandlers.onEvent?.(
      frame({ id: 6, category: "content", title: "New content", body: "", profile_id: "p-other" }),
    );
    expect(toastMock).not.toHaveBeenCalled();
  });

  it("suppresses restricted categories on child profiles", () => {
    profileState.profile = { id: "p-1", is_child: true };
    setup();
    capturedHandlers.onEvent?.(
      frame({ id: 7, category: "request", title: "Request approved", body: "", profile_id: undefined }),
    );
    expect(toastMock).not.toHaveBeenCalled();
    profileState.profile = { id: "p-1", is_child: false };
  });

  it("seeds unread count from snapshot", () => {
    const qc = setup();
    capturedHandlers.onSnapshot?.({ type: "snapshot", channel: "notifications", data: { unread_count: 9 } });
    expect(qc.getQueryData(notificationKeys.unreadCount())).toEqual({ count: 9 });
  });
});
```

(The test file needs `.tsx` extension if JSX is used in the wrapper — name it `useNotificationsLive.test.tsx`.)

- [ ] **Step 2: Run to verify failure**

```bash
cd web && pnpm exec vitest run src/hooks/useNotificationsLive.test.tsx 2>&1 | tail -5
```

Expected: FAIL — module `./useNotificationsLive` not found.

- [ ] **Step 3: Implement**

```typescript
import { useCallback } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import type { AppNotification } from "@/api/types";
import { useEventChannel } from "@/components/realtimeEventsContext";
import { notificationKeys } from "@/hooks/queries/keys";
import { useAuth } from "@/hooks/useAuth";

const CHILD_HIDDEN_CATEGORIES = new Set(["request", "system", "admin"]);

/**
 * Subscribes to the notifications WS channel: fires a toast for live
 * notifications addressed to the active profile, keeps the unread-count
 * cache current, and seeds it from the subscribe snapshot.
 * Mount exactly once inside the authenticated layout.
 */
export function useNotificationsLive() {
  const queryClient = useQueryClient();
  const { profile } = useAuth();
  const profileId = profile?.id;
  const isChild = profile?.is_child ?? false;

  const onEvent = useCallback(
    (message: unknown) => {
      const frame = message as { event?: string; data?: AppNotification };
      if (frame.event !== "notification.created" || !frame.data) return;
      const n = frame.data;
      if (n.profile_id && n.profile_id !== profileId) return;
      if (isChild && CHILD_HIDDEN_CATEGORIES.has(n.category)) return;

      queryClient.setQueryData<{ count: number }>(notificationKeys.unreadCount(), (prev) => ({
        count: (prev?.count ?? 0) + 1,
      }));
      void queryClient.invalidateQueries({
        queryKey: [...notificationKeys.all, "list"],
        refetchType: "none",
      });
      toast(n.title, { description: n.body });
    },
    [queryClient, profileId, isChild],
  );

  const onSnapshot = useCallback(
    (message: unknown) => {
      const frame = message as { data?: { unread_count?: number } };
      if (typeof frame.data?.unread_count !== "number") return;
      queryClient.setQueryData(notificationKeys.unreadCount(), { count: frame.data.unread_count });
    },
    [queryClient],
  );

  useEventChannel("notifications", { onEvent, onSnapshot });
}
```

NOTE on handler identity: `useEventChannel`'s effect re-subscribes when the handlers object changes — pass a stable object. Check how existing consumers handle this (grep `useEventChannel(` usages); if they memoize the handlers object, do the same: `useMemo(() => ({ onEvent, onSnapshot }), [onEvent, onSnapshot])`.

The toast call shape (`toast(title, { description })`) must match the test's `expect.anything()` second arg. If `useAuth` returns a different profile field shape, adapt (read `useAuth.tsx`).

- [ ] **Step 4: Run tests until green**

```bash
cd web && pnpm exec vitest run src/hooks/useNotificationsLive.test.tsx 2>&1 | tail -5
```

- [ ] **Step 5: Commit**

```bash
git add web/src/hooks/useNotificationsLive.* && git commit -m "feat(web): live notifications wiring (toasts, unread cache)"
```

---

### Task 4: NotificationBell component + Layout mount

**Files:**
- Create: `web/src/components/NotificationBell.tsx`
- Test: `web/src/components/NotificationBell.test.tsx`
- Modify: `web/src/components/Layout.tsx` (mobile header actions ~lines 151-168; also mount `useNotificationsLive()` once in the Layout component body)

- [ ] **Step 1: Failing test**

```typescript
// Mocks: @/hooks/queries/notifications (useUnreadCount → 3, useNotificationsList → two items,
// useMarkRead → mutate spy), react-router (MemoryRouter wrapper).
// Asserts: badge renders "3"; opening the popover (click bell button, role="button" name /notifications/i)
// shows both item titles; "Mark all read" calls markRead with {all:true};
// item with link renders an anchor/Link to that link.
```

Write it concretely following an existing component test's structure (see `web/src/components/AppSidebar.test.tsx` for provider wrapping conventions). Badge cap: 4+ digits render "99+" when count > 99 — include one assertion with count 120.

- [ ] **Step 2: Implement NotificationBell**

```tsx
import { Bell } from "lucide-react";
import { Link } from "react-router";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import {
  useMarkRead,
  useNotificationsList,
  useUnreadCount,
} from "@/hooks/queries/notifications";

export function NotificationBell() {
  const { data: count = 0 } = useUnreadCount();
  const list = useNotificationsList({});
  const markRead = useMarkRead();
  const items = (list.data?.pages ?? []).flatMap((p) => p.items ?? []).slice(0, 10);

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button variant="ghost" size="icon" aria-label="Notifications" className="relative">
          <Bell className="h-5 w-5" />
          {count > 0 && (
            <Badge className="absolute -top-1 -right-1 h-4 min-w-4 px-1 text-[10px]">
              {count > 99 ? "99+" : count}
            </Badge>
          )}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 p-0">
        <div className="flex items-center justify-between border-b px-3 py-2">
          <span className="text-sm font-medium">Notifications</span>
          <Button
            variant="ghost"
            size="sm"
            disabled={count === 0}
            onClick={() => markRead.mutate({ all: true })}
          >
            Mark all read
          </Button>
        </div>
        <ul className="max-h-96 list-none overflow-y-auto">
          {items.length === 0 && (
            <li className="text-muted-foreground px-3 py-6 text-center text-sm">
              You're all caught up
            </li>
          )}
          {items.map((n) => (
            <li key={n.id} className={n.read_at ? "opacity-60" : ""}>
              <Link to={n.link ?? "/notifications"} className="hover:bg-accent block px-3 py-2">
                <div className="text-sm font-medium">{n.title}</div>
                {n.body && <div className="text-muted-foreground truncate text-xs">{n.body}</div>}
              </Link>
            </li>
          ))}
        </ul>
        <div className="border-t px-3 py-2 text-center">
          <Link to="/notifications" className="text-primary text-sm">
            View all
          </Link>
        </div>
      </PopoverContent>
    </Popover>
  );
}
```

Verify the ui primitives exist with these export names (`badge.tsx`, `button.tsx`, `popover.tsx` under `components/ui/`); adjust imports/props to the actual variants. Match `react-router` import name used elsewhere (the repo uses `react-router` v7 — check whether components import `Link` from `react-router` or `react-router-dom`).

Dropdown-open toast suppression (spec requirement): export a tiny module-level flag from NotificationBell —

```tsx
let bellOpenFlag = false;
export function isNotificationDropdownOpen() {
  return bellOpenFlag;
}
// in the component: <Popover onOpenChange={(open) => { bellOpenFlag = open; }}>
```

and in `useNotificationsLive`'s onEvent, before toasting: `if (isNotificationDropdownOpen()) return;` (cache updates still run — only the toast is suppressed; the open dropdown shows the new item via the list invalidation). Add one live-hook test: set the flag via a popover-open simulation or export a test-only setter, assert no toast while open.

- [ ] **Step 3: Mount in Layout**

In `Layout.tsx`: call `useNotificationsLive()` at the top of the component (single mount point for the live hook), and render `<NotificationBell />` in the mobile header's right-side action cluster (next to the search icon button).

- [ ] **Step 4: Tests green, typecheck, commit**

```bash
cd web && pnpm exec vitest run src/components/NotificationBell.test.tsx 2>&1 | tail -5 && pnpm exec tsc -b --noEmit 2>&1 | head -3
git add src/components/NotificationBell.* src/components/Layout.tsx src/hooks/
git commit -m "feat(web): notification bell with live badge in mobile header"
```

---

### Task 5: Sidebar nav item with badge (desktop)

**Files:**
- Modify: `web/src/components/AppSidebar.tsx` (nav list — insert after the History item, ~line 645)
- Test: extend `web/src/components/AppSidebar.test.tsx`

- [ ] **Step 1: Add the nav item**

Follow the exact ViewTransitionLink pattern of the History item (active indicator span, icon sizing, `SidebarLabel`):

```tsx
<li>
  <ViewTransitionLink
    to="/notifications"
    onClick={onNavigate}
    className={navLinkClass("/notifications")}
    aria-current={isActive("/notifications") ? "page" : undefined}
  >
    {isActive("/notifications") && (
      <span
        className="absolute top-1/2 left-0 h-[18px] w-[3px] -translate-y-1/2 rounded-r-sm"
        style={{ background: "var(--primary)" }}
      />
    )}
    <Bell className="h-[18px] w-[18px] shrink-0" />
    {unreadCount > 0 && (
      <span className="bg-primary text-primary-foreground absolute top-1 left-6 flex h-4 min-w-4 items-center justify-center rounded-full px-1 text-[10px]">
        {unreadCount > 99 ? "99+" : unreadCount}
      </span>
    )}
    <SidebarLabel show={showLabels}>Notifications</SidebarLabel>
  </ViewTransitionLink>
</li>
```

`const { data: unreadCount = 0 } = useUnreadCount();` at the top of AppSidebar (import from `@/hooks/queries/notifications`; `Bell` from lucide-react — add to the existing lucide import list at line ~56). Badge positioning: verify visually against the collapsed-sidebar state (`showLabels` false) — the absolute chip must remain visible on the icon in both states; tweak offsets if the icon container differs.

- [ ] **Step 2: Test**

Extend AppSidebar.test.tsx: mock `@/hooks/queries/notifications` `useUnreadCount` to return `{ data: 5 }`; assert the rendered sidebar contains a link to `/notifications` showing "5". Follow the file's existing mock arrangement for other hooks.

- [ ] **Step 3: Run, typecheck, commit**

```bash
cd web && pnpm exec vitest run src/components/AppSidebar.test.tsx 2>&1 | tail -4
git add src/components/AppSidebar.* && git commit -m "feat(web): notifications sidebar item with unread badge"
```

---

### Task 6: Inbox page + route

**Files:**
- Create: `web/src/pages/Notifications.tsx`
- Test: `web/src/pages/Notifications.test.tsx`
- Modify: `web/src/App.tsx` (user routes block ~line 331+)

- [ ] **Step 1: Failing test**

Mock the notifications query hooks. Assertions:
- renders rows for items across categories with title/body/relative time;
- category filter tabs (All, Requests, Content, Announcements, System — admin tab only when `useAuth().user?.role === "admin"`) change the `category` filter passed to `useNotificationsList` (capture via mock);
- unread rows visually distinct (e.g. a `data-unread="true"` attribute — assert on it);
- per-row dismiss button calls `useDismissNotification().mutate(id)`;
- "Load more" appears when `hasNextPage` and calls `fetchNextPage`;
- on mount, visible unread ids are marked read: `useMarkRead().mutate({ ids: [...] })` (assert called once with the unread ids from page one).

- [ ] **Step 2: Implement**

Page structure (use the repo's page conventions — check an existing simple page like the History/Collections page for header/container classes):

```tsx
const CATEGORY_TABS: { key: string; label: string; adminOnly?: boolean }[] = [
  { key: "", label: "All" },
  { key: "request", label: "Requests" },
  { key: "content", label: "Content" },
  { key: "announcement", label: "Announcements" },
  { key: "system", label: "System" },
  { key: "admin", label: "Admin", adminOnly: true },
];
```

- `useState` for active category; `useNotificationsList({ category })` (omit when "");
- mark-on-view effect:

```tsx
const firstPage = list.data?.pages?.[0];
const markRead = useMarkRead();
const markedRef = useRef(false);
useEffect(() => {
  if (markedRef.current || !firstPage) return;
  const unreadIds = (firstPage.items ?? []).filter((n) => !n.read_at).map((n) => n.id);
  if (unreadIds.length > 0) markRead.mutate({ ids: unreadIds });
  markedRef.current = true;
}, [firstPage, markRead]);
```

- rows: title, body, `created_at` rendered with the repo's existing relative-time helper (grep `formatDistance\|relativeTime\|timeAgo` in web/src/lib — use what exists; if nothing, use `Intl.RelativeTimeFormat` inline helper in the page);
- dismiss button (X icon) per row; "Mark all read" header button; "Load more" via `fetchNextPage` when `hasNextPage`;
- empty state per tab.

- [ ] **Step 3: Route**

In `App.tsx`, register alongside the other authenticated user pages (match the guard used by e.g. the favorites/history page — likely inside the layout route with profile required):

```tsx
<Route path="/notifications" element={<Notifications />} />
```

(Lazy-load if neighboring pages are lazy — match the file's import pattern.)

- [ ] **Step 4: Tests, typecheck, commit**

```bash
cd web && pnpm exec vitest run src/pages/Notifications.test.tsx 2>&1 | tail -5 && pnpm exec tsc -b --noEmit 2>&1 | head -3
git add src/pages/Notifications.* src/App.tsx && git commit -m "feat(web): notifications inbox page"
```

---

### Task 7: Settings preferences section

**Files:**
- Create: `web/src/pages/settings/NotificationsSettings.tsx`
- Modify: `web/src/pages/SettingsLayout.tsx` (NAV_SECTIONS), `web/src/App.tsx` (settings child route)

- [ ] **Step 1: Implement the settings page**

Template: `web/src/pages/settings/PlaybackSettings.tsx` (SettingsGroup/SettingRow/Switch composition). Content:

```tsx
const PREF_LABELS: Record<string, { label: string; description: string; adminOnly?: boolean }> = {
  request: { label: "Requests", description: "Updates when your requests are approved, declined, or ready" },
  content: { label: "New content", description: "New arrivals matching your watchlist, favorites, and shows in progress" },
  system: { label: "Account & security", description: "Password changes and account notices" },
  admin: { label: "Admin alerts", description: "Failed jobs and scans (admins only)", adminOnly: true },
  content_digest: { label: "Daily digest", description: "One daily summary of everything added to your libraries" },
};
```

- `useNotificationPreferences()` for current values; render a `Switch` per category in PREF_LABELS order, skipping `adminOnly` entries when `useAuth().user?.role !== "admin"`;
- on toggle: `useSetNotificationPreferences().mutate([{ category, enabled }])` (single-category PUT — server upserts per row);
- note under the group: "Announcements from your server admin can't be turned off."

- [ ] **Step 2: Register in SettingsLayout NAV_SECTIONS**

Add a "Notifications" section (icon `Bell`) pointing at path `notifications`, following the structure at SettingsLayout.tsx lines 39-100. Register the child route in App.tsx where other settings pages are routed (find `PlaybackSettings` route registration and mirror it).

- [ ] **Step 3: Test**

Extend `web/src/pages/SettingsLayout.test.tsx` if it asserts nav items (check); add `NotificationsSettings` rendering test only if sibling settings pages have tests (check `ls web/src/pages/settings/*.test.tsx`) — match local convention; if none exist, rely on the layout test + typecheck (note this in the commit message).

- [ ] **Step 4: Typecheck, test, commit**

```bash
cd web && pnpm exec tsc -b --noEmit 2>&1 | head -3 && pnpm exec vitest run src/pages/SettingsLayout.test.tsx 2>&1 | tail -4
git add src/pages/settings/NotificationsSettings.tsx src/pages/SettingsLayout.tsx src/App.tsx
git commit -m "feat(web): notification preference toggles in settings"
```

---

### Task 8: Admin announcements page

**Files:**
- Create: `web/src/pages/AdminAnnouncements.tsx`
- Modify: `web/src/components/AdminSidebar.tsx` (nav items ~lines 81-134), `web/src/App.tsx` (admin routes ~lines 363-398)

- [ ] **Step 1: Implement the page**

Template: `web/src/pages/AdminUsers.tsx` for the table + dialog patterns (read it first; reuse its Table/Dialog/Button composition). Features:

- Table of announcements: title, audience summary ("Everyone" / "N users" / "Libraries: a, b"), created date, expires date, delete button with confirm dialog ("Deleting dismisses unread copies from user inboxes.").
- "New announcement" button → Dialog with: title (Input, required), body (Textarea), audience radio group (Everyone / Specific users / Libraries) — for "Specific users" a multi-select of users (check how AdminUsers fetches the user list — reuse that hook; if a multi-select primitive is missing, comma-separated ids input is NOT acceptable — use checkboxes in a scrollable list), for Libraries the same against the libraries list hook (grep `useLibraries` in hooks/queries) — optional `expires_at` datetime-local input.
- Submit via `useCreateAnnouncement()`; disable submit until title non-empty and a valid audience selection exists.

- [ ] **Step 2: Nav + route**

AdminSidebar: add item (icon `Megaphone` from lucide-react) labeled "Announcements" → `/admin/announcements`, following the existing items' structure. App.tsx admin routes: `<Route path="announcements" element={<AdminAnnouncements />} />`.

- [ ] **Step 3: Test**

`web/src/pages/AdminAnnouncements.test.tsx` — mock the three admin hooks; assert: table renders rows incl. audience summaries; create dialog validates (submit disabled with empty title); submitting with Everyone calls `useCreateAnnouncement().mutate` with `{audience: {all: true}, ...}`; delete confirm calls `useDeleteAnnouncement().mutate(id)`.

- [ ] **Step 4: Tests, typecheck, commit**

```bash
cd web && pnpm exec vitest run src/pages/AdminAnnouncements.test.tsx 2>&1 | tail -5 && pnpm exec tsc -b --noEmit 2>&1 | head -3
git add src/pages/AdminAnnouncements.* src/components/AdminSidebar.tsx src/App.tsx
git commit -m "feat(web): admin announcements management page"
```

---

### Task 9: Full verification

- [ ] **Step 1: Web checks**

```bash
cd web && pnpm run lint && pnpm run format:check && pnpm exec vitest run 2>&1 | tail -6 && pnpm run build 2>&1 | tail -4
```

All green; build also refreshes `web/dist` so the Go embed keeps working.

- [ ] **Step 2: Backend still green**

```bash
GOWORK=off go build ./... && GOWORK=off go test ./internal/api/handlers/ 2>&1 | tail -2
make verify-local-paths
```

- [ ] **Step 3: E2E smoke against the dev stack**

```bash
docker compose up -d postgres redis
GOWORK=off go build -o /tmp/silo-web-e2e ./cmd/silo && /tmp/silo-web-e2e &
```

In a browser (or report for manual check): log in, confirm the sidebar Notifications item + mobile bell render; create an announcement in /admin/announcements; verify toast fires live (WS) and badge increments without reload; open inbox, rows mark read, badge clears; toggle a preference off and verify it persists after reload.

- [ ] **Step 4: Commit anything outstanding; report**

---

## Self-review notes

- Desktop bell+dropdown from the spec adapted to a sidebar nav item + badge (no desktop top bar exists in this app); the dropdown ships on the mobile header bell. Spec intent (always-visible unread indicator + 1-click access) preserved.
- Toast child/profile filtering is client-side by necessity (WS frames are user-scoped; see spec Future work on claims enrichment) — `useNotificationsLive` suppresses other-profile and child-restricted frames.
- `content_digest` rows arrive as category `content` (server stores digest as content/content.digest) — the inbox Content tab includes them; the preferences page exposes the separate `content_digest` toggle. No UI filters by `content_digest` category (final server review noted this trap).
- Mark-on-view marks only page one's unread ids once per mount — deliberate; deeper pages mark via "Mark all read".
- Several steps include verify-greps (ui primitive names, Link import source, relative-time helper, settings test conventions, multi-select availability) — implementers must align with what exists rather than invent.
