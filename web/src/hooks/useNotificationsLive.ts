import { useMemo } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import type { AppNotification, EventsEventMessage, EventsSnapshotMessage } from "@/api/types";
import { useEventChannel } from "@/components/realtimeEventsContext";
import { isNotificationDropdownOpen } from "@/components/NotificationBell";
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

  const handlers = useMemo(
    () => ({
      onEvent: (message: unknown) => {
        const f = message as EventsEventMessage<AppNotification>;
        if (f.event !== "notification.created" || !f.data) return;
        const n = f.data;
        if (n.profile_id && n.profile_id !== profileId) return;
        if (isChild && CHILD_HIDDEN_CATEGORIES.has(n.category)) return;

        queryClient.setQueryData<{ count: number }>(notificationKeys.unreadCount(), (prev) => ({
          count: (prev?.count ?? 0) + 1,
        }));
        // Announcements are system-wide notices we want surfaced immediately, so
        // refetch the active notification lists (home bar, inbox, bell) the
        // moment one arrives. Routine notifications only mark the lists stale to
        // avoid disrupting a list the user is currently reading.
        void queryClient.invalidateQueries({
          queryKey: [...notificationKeys.all, "list"],
          refetchType: n.category === "announcement" ? "active" : "none",
        });
        if (!isNotificationDropdownOpen()) {
          toast(n.title, { description: n.body || undefined });
        }
      },
      onSnapshot: (message: unknown) => {
        const f = message as EventsSnapshotMessage<{ unread_count?: number }>;
        if (typeof f.data?.unread_count !== "number") return;
        queryClient.setQueryData(notificationKeys.unreadCount(), { count: f.data.unread_count });
      },
    }),
    [queryClient, profileId, isChild],
  );

  useEventChannel("notifications", handlers);
}
