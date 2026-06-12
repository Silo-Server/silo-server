import type { QueryClient } from "@tanstack/react-query";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type {
  AppNotification,
  NotificationDiscordLinkInit,
  NotificationDiscordMode,
  NotificationDiscordPreferences,
  NotificationEmailPreferences,
  NotificationEmailPreferencesUpdate,
  NotificationListResponse,
  NotificationPreferences,
  NotificationReadEventPayload,
  NotificationUnreadCountResponse,
} from "@/api/types";
import { notificationKeys } from "./keys";
import { toast } from "sonner";

const NOTIFICATIONS_PAGE_SIZE = 25;

export function useNotifications(status: "all" | "unread" = "all") {
  return useInfiniteQuery({
    queryKey: notificationKeys.list(status),
    initialPageParam: "",
    queryFn: ({ pageParam }) => {
      const search = new URLSearchParams({ limit: String(NOTIFICATIONS_PAGE_SIZE) });
      if (status === "unread") {
        search.set("status", "unread");
      }
      if (pageParam) {
        search.set("before", pageParam);
      }
      return api<NotificationListResponse>(`/notifications?${search.toString()}`);
    },
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
  });
}

export function useUnreadNotificationCount(enabled = true) {
  return useQuery({
    queryKey: notificationKeys.unreadCount(),
    queryFn: () =>
      api<NotificationUnreadCountResponse>("/notifications/unread-count").then((d) => d.count),
    enabled,
    staleTime: 30_000,
  });
}

export function useMarkNotificationRead() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api(`/notifications/${id}/read`, { method: "POST" }),
    onMutate: (id: string) => {
      applyNotificationRead(queryClient, { profile_id: "", id });
    },
    onError: () => {
      toast.error("Failed to mark notification read");
      void queryClient.invalidateQueries({ queryKey: notificationKeys.all });
    },
  });
}

export function useMarkAllNotificationsRead() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => api("/notifications/read-all", { method: "POST" }),
    onMutate: () => {
      applyNotificationRead(queryClient, { profile_id: "", all: true });
    },
    onError: () => {
      toast.error("Failed to mark notifications read");
      void queryClient.invalidateQueries({ queryKey: notificationKeys.all });
    },
  });
}

export function useNotificationPreferences() {
  return useQuery({
    queryKey: notificationKeys.preferences(),
    queryFn: () => api<NotificationPreferences>("/notifications/preferences"),
  });
}

export function useUpdateNotificationPreferences() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (update: Partial<NotificationPreferences>) =>
      api<NotificationPreferences>("/notifications/preferences", {
        method: "PUT",
        body: JSON.stringify(update),
      }),
    onSuccess: (prefs) => {
      queryClient.setQueryData(notificationKeys.preferences(), prefs);
    },
    onError: () => {
      toast.error("Failed to save notification preferences");
    },
  });
}

export function useEmailNotificationPreferences(enabled = true) {
  return useQuery({
    queryKey: notificationKeys.emailPreferences(),
    queryFn: () => api<NotificationEmailPreferences>("/notifications/email-preferences"),
    enabled,
  });
}

export function useUpdateEmailNotificationPreferences() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (update: NotificationEmailPreferencesUpdate) =>
      api<NotificationEmailPreferences>("/notifications/email-preferences", {
        method: "PUT",
        body: JSON.stringify(update),
      }),
    onSuccess: (prefs) => {
      queryClient.setQueryData(notificationKeys.emailPreferences(), prefs);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to save email preferences");
    },
  });
}

export function useRequestEmailNotificationAddress() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (email: string) =>
      api<NotificationEmailPreferences>("/notifications/email-preferences/address", {
        method: "PUT",
        body: JSON.stringify({ email }),
      }),
    onSuccess: (prefs) => {
      queryClient.setQueryData(notificationKeys.emailPreferences(), prefs);
      toast.success(`Verification email sent to ${prefs.pending_email}`);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to send the verification email");
    },
  });
}

export function useClearEmailNotificationAddress() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      api<NotificationEmailPreferences>("/notifications/email-preferences/address", {
        method: "DELETE",
      }),
    onSuccess: (prefs) => {
      queryClient.setQueryData(notificationKeys.emailPreferences(), prefs);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to remove the custom address");
    },
  });
}

export function useDiscordNotificationPreferences(enabled = true) {
  return useQuery({
    queryKey: notificationKeys.discordPreferences(),
    queryFn: () => api<NotificationDiscordPreferences>("/notifications/discord-preferences"),
    enabled,
  });
}

export function useUpdateDiscordNotificationPreferences() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (update: { mode: NotificationDiscordMode }) =>
      api<NotificationDiscordPreferences>("/notifications/discord-preferences", {
        method: "PUT",
        body: JSON.stringify(update),
      }),
    onSuccess: (prefs) => {
      queryClient.setQueryData(notificationKeys.discordPreferences(), prefs);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to save Discord preferences");
    },
  });
}

/** Starts the Discord account-link OAuth flow; navigate to the returned URL. */
export function useDiscordLinkInit() {
  return useMutation({
    mutationFn: () =>
      api<NotificationDiscordLinkInit>("/notifications/discord/link/init", { method: "POST" }),
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to start Discord link");
    },
  });
}

export function useUnlinkDiscord() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => api("/notifications/discord-link", { method: "DELETE" }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: notificationKeys.discordPreferences() });
      toast.success("Discord account unlinked");
    },
    onError: () => {
      toast.error("Failed to unlink Discord account");
    },
  });
}

// --- Realtime cache reducers (used by RealtimeEventsProvider) ---

type NotificationsInfiniteData = {
  pages: NotificationListResponse[];
  pageParams: unknown[];
};

function updateCachedLists(
  queryClient: QueryClient,
  update: (notification: AppNotification) => AppNotification,
) {
  for (const status of ["all", "unread"] as const) {
    queryClient.setQueryData<NotificationsInfiniteData>(notificationKeys.list(status), (data) =>
      data
        ? {
            ...data,
            pages: data.pages.map((page) => ({
              ...page,
              notifications: page.notifications.map(update),
            })),
          }
        : data,
    );
  }
}

/** Prepends a freshly created notification and bumps the unread badge. */
export function applyNotificationCreated(queryClient: QueryClient, notification: AppNotification) {
  queryClient.setQueryData<NotificationsInfiniteData>(notificationKeys.list("all"), (data) => {
    const first = data?.pages[0];
    if (!data || !first) {
      return data;
    }
    if (first.notifications.some((entry) => entry.id === notification.id)) {
      return data;
    }
    return {
      ...data,
      pages: [
        { ...first, notifications: [notification, ...first.notifications] },
        ...data.pages.slice(1),
      ],
    };
  });
  void queryClient.invalidateQueries({ queryKey: notificationKeys.list("unread") });
  if (!notification.read_at) {
    queryClient.setQueryData<number>(notificationKeys.unreadCount(), (count) => (count ?? 0) + 1);
  }
}

/** Applies a read event (single id or all) to cached rows and the badge. */
export function applyNotificationRead(
  queryClient: QueryClient,
  payload: NotificationReadEventPayload,
) {
  const readAt = new Date().toISOString();
  if (payload.all) {
    updateCachedLists(queryClient, (entry) =>
      entry.read_at ? entry : { ...entry, read_at: readAt },
    );
    queryClient.setQueryData<number>(notificationKeys.unreadCount(), 0);
    return;
  }
  if (!payload.id) {
    return;
  }
  let found = false;
  let transitioned = false;
  updateCachedLists(queryClient, (entry) => {
    if (entry.id !== payload.id) {
      return entry;
    }
    found = true;
    if (entry.read_at) {
      return entry;
    }
    transitioned = true;
    return { ...entry, read_at: readAt };
  });
  // Decrement when we observed the unread -> read flip, or when the row is
  // not cached at all (the backend only publishes read events on real
  // transitions, so an unseen row was unread).
  if (!found || transitioned) {
    queryClient.setQueryData<number>(notificationKeys.unreadCount(), (count) =>
      count == null ? count : Math.max(0, count - 1),
    );
  }
}

/** Hydrates the unread badge from the websocket snapshot (recent unread rows). */
export function applyNotificationsSnapshot(queryClient: QueryClient, rows: AppNotification[]) {
  // The snapshot is capped (25 rows); use it as a lower bound and refresh the
  // real count lazily.
  queryClient.setQueryData<number>(notificationKeys.unreadCount(), (count) =>
    Math.max(count ?? 0, rows.length),
  );
  void queryClient.invalidateQueries({ queryKey: notificationKeys.unreadCount() });
  void queryClient.invalidateQueries({
    queryKey: notificationKeys.all,
    refetchType: "active",
  });
}

/** Formats the "S2E5" style episode code for a notification row. */
export function formatEpisodeCode(notification: AppNotification): string | null {
  if (notification.season_number == null || notification.episode_number == null) {
    return null;
  }
  return `S${notification.season_number}E${notification.episode_number}`;
}
