import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { Announcement, NotificationListResponse, NotificationPreference } from "@/api/types";
import { notificationKeys } from "./keys";
import { toast } from "sonner";

const PAGE_SIZE = 25;

interface AnnouncementsResponse {
  items: Announcement[];
}

interface NotificationPreferencesResponse {
  preferences: NotificationPreference[];
}

interface UnreadCountResponse {
  count: number;
}

export function useNotificationsList(filters: { unread?: boolean; category?: string } = {}) {
  return useInfiniteQuery({
    queryKey: notificationKeys.list(filters),
    queryFn: ({ pageParam }: { pageParam: number }) => {
      const params = new URLSearchParams();
      params.set("limit", String(PAGE_SIZE));
      if (filters.unread) params.set("unread", "1");
      if (filters.category) params.set("category", filters.category);
      if (pageParam) params.set("cursor", String(pageParam));
      return api<NotificationListResponse>(`/notifications?${params}`);
    },
    initialPageParam: 0,
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
    staleTime: 2 * 60 * 1000,
  });
}

export function useUnreadCount() {
  return useQuery({
    queryKey: notificationKeys.unreadCount(),
    queryFn: () => api<UnreadCountResponse>("/notifications/unread-count"),
    select: (d) => d.count,
  });
}

export function useMarkRead() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: { ids?: number[]; all?: boolean }) =>
      api("/notifications/read", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: notificationKeys.all });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to mark read");
    },
  });
}

export function useDismissNotification() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) =>
      api(`/notifications/${id}/dismiss`, {
        method: "POST",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: notificationKeys.all });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to dismiss notification");
    },
  });
}

export function useNotificationPreferences() {
  return useQuery({
    queryKey: notificationKeys.preferences(),
    queryFn: () => api<NotificationPreferencesResponse>("/notifications/preferences"),
    select: (d) => d.preferences,
  });
}

export function useSetNotificationPreferences() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: { preferences: NotificationPreference[] }) =>
      api("/notifications/preferences", {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onMutate: async (body) => {
      await queryClient.cancelQueries({ queryKey: notificationKeys.preferences() });
      const previous = queryClient.getQueryData(notificationKeys.preferences());
      queryClient.setQueryData(
        notificationKeys.preferences(),
        (old: { preferences: NotificationPreference[] } | undefined) => {
          if (!old) return old;
          return {
            preferences: old.preferences.map(
              (p) => body.preferences.find((b) => b.category === p.category) ?? p,
            ),
          };
        },
      );
      return { previous };
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: notificationKeys.preferences() });
    },
    onError: (err, _variables, context) => {
      if (context?.previous)
        queryClient.setQueryData(notificationKeys.preferences(), context.previous);
      toast.error(err instanceof Error ? err.message : "Failed to save preferences");
    },
  });
}

export function useAnnouncements() {
  return useQuery({
    queryKey: notificationKeys.announcements(),
    queryFn: () => api<AnnouncementsResponse>("/admin/announcements"),
    select: (d) => d.items ?? [],
  });
}

export function useCreateAnnouncement() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: Omit<Announcement, "id" | "created_at" | "created_by">) =>
      api<Announcement>("/admin/announcements", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Announcement published");
      queryClient.invalidateQueries({ queryKey: notificationKeys.announcements() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to publish announcement");
    },
  });
}

export function useDeleteAnnouncement() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) =>
      api(`/admin/announcements/${id}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      toast.success("Announcement deleted");
      queryClient.invalidateQueries({ queryKey: notificationKeys.announcements() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete announcement");
    },
  });
}
