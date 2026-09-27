import { useEffect, useRef } from "react";
import {
  captureEmailVerificationIntent,
  queueNotificationEmailVerification,
  type EmailVerificationIntent,
} from "@/api/v2/notificationEmailVerification";
import {
  beginNotificationDiscordLink,
  unlinkNotificationDiscord,
} from "@/api/v2/notificationDiscord";
import type { QueryClient } from "@tanstack/react-query";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { captureProfileRequestContext, StaleApiRequestContextError } from "@/api/client";
import {
  captureNotificationAuthority,
  notificationScope,
  requireNotificationAuthority,
  listNotifications,
  unreadNotificationCount,
  notificationPreferences,
  updateNotificationPreferences,
  markNotificationRead,
  markAllNotificationsRead,
} from "@/api/v2/notifications";
import {
  getNotificationEmailPreferences,
  clearNotificationEmailAddress,
  updateNotificationEmailPreferences,
  getNotificationDiscordPreferences,
  updateNotificationDiscordPreferences,
} from "@/api/v2/notificationChannels";
import type {
  AppNotification,
  NotificationDiscordMode,
  NotificationEmailPreferencesUpdate,
  NotificationListResponse,
  NotificationPreferences,
  NotificationReadEventPayload,
} from "@/api/types";
import { notificationKeys } from "./keys";
import { toast } from "sonner";

const NOTIFICATIONS_PAGE_SIZE = 25;

export const notificationInboxKeys = {
  list: (status: "all" | "unread", scope = notificationScope()) => [
    ...notificationKeys.list(status),
    scope,
  ],
  count: (scope = notificationScope()) => [...notificationKeys.unreadCount(), scope],
  preferences: (scope = notificationScope()) => [...notificationKeys.preferences(), scope],
};
function invalidateInbox(queryClient: QueryClient, scope = notificationScope()) {
  for (const status of ["all", "unread"] as const)
    void queryClient.invalidateQueries({ queryKey: notificationInboxKeys.list(status, scope) });
  void queryClient.invalidateQueries({ queryKey: notificationInboxKeys.count(scope) });
}
export function useNotifications(status: "all" | "unread" = "all") {
  const context = captureProfileRequestContext();
  const scope = notificationScope(context);
  const queryClient = useQueryClient();
  const queryKey = notificationInboxKeys.list(status, scope);
  const query = useInfiniteQuery({
    queryKey,
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const result = await listNotifications(
        status,
        pageParam,
        context ?? captureNotificationAuthority(),
      );
      const params =
        queryClient.getQueryData<{ pageParams: (string | undefined)[] }>(queryKey)?.pageParams ??
        [];
      const index = params.indexOf(pageParam);
      const prior = index < 0 ? params : params.slice(0, index + 1);
      if (result.next_cursor && prior.includes(result.next_cursor))
        throw new Error("Notification cursor repeated. Reload notifications.");
      return result;
    },
    getNextPageParam: (last) => last.next_cursor,
    enabled: context !== null,
    retry: false,
  });
  return { ...query, restart: () => queryClient.resetQueries({ queryKey, exact: true }) };
}
export function useUnreadNotificationCount(enabled = true) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: notificationInboxKeys.count(notificationScope(context)),
    queryFn: () => unreadNotificationCount(context ?? captureNotificationAuthority()),
    enabled: enabled && context !== null,
    staleTime: 30_000,
    retry: false,
  });
}
export function useMarkNotificationRead() {
  const queryClient = useQueryClient();
  const context = captureNotificationAuthority();
  return useMutation({
    mutationFn: (id: string) => markNotificationRead(id, context),
    retry: false,
    onMutate: (id: string) => {
      requireNotificationAuthority(context);
      applyNotificationRead(queryClient, { profile_id: context.profileId, id });
    },
    onError: () => {
      toast.error("Failed to mark notification read");
      invalidateInbox(queryClient, notificationScope(context));
    },
  });
}
export function useMarkAllNotificationsRead() {
  const queryClient = useQueryClient();
  const context = captureNotificationAuthority();
  return useMutation({
    mutationFn: (through: string) => markAllNotificationsRead(through, context),
    retry: false,
    onSuccess: () => invalidateInbox(queryClient, notificationScope(context)),
    onError: () => {
      toast.error("Failed to mark notifications read");
      invalidateInbox(queryClient, notificationScope(context));
    },
  });
}
export function useNotificationPreferences() {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: notificationInboxKeys.preferences(notificationScope(context)),
    queryFn: () => notificationPreferences(context ?? captureNotificationAuthority()),
    enabled: context !== null,
    retry: false,
  });
}
export function useUpdateNotificationPreferences() {
  const queryClient = useQueryClient();
  const context = captureNotificationAuthority();
  return useMutation({
    mutationFn: (input: Partial<NotificationPreferences>) =>
      updateNotificationPreferences(input, context),
    retry: false,
    onSuccess: (prefs) => {
      queryClient.setQueryData(
        notificationInboxKeys.preferences(notificationScope(context)),
        prefs,
      );
    },
    onError: () => toast.error("Failed to save notification preferences"),
  });
}

export function useEmailNotificationPreferences(enabled = true) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...notificationKeys.emailPreferences(), notificationScope(context)],
    queryFn: () => getNotificationEmailPreferences(context ?? captureNotificationAuthority()),
    enabled: enabled && context !== null,
    retry: false,
  });
}

export function useUpdateEmailNotificationPreferences() {
  const queryClient = useQueryClient();
  const context = captureProfileRequestContext();
  return useMutation({
    retry: false,
    mutationFn: (update: NotificationEmailPreferencesUpdate) =>
      updateNotificationEmailPreferences(update, context ?? captureNotificationAuthority()),
    onSuccess: (prefs) => {
      if (!context) return;
      requireNotificationAuthority(context);
      queryClient.setQueryData(
        [...notificationKeys.emailPreferences(), notificationScope(context)],
        prefs,
      );
    },
    onError: (error) => {
      if (error instanceof StaleApiRequestContextError) return;
      toast.error(error instanceof Error ? error.message : "Failed to save email preferences");
    },
  });
}

export function useRequestEmailNotificationAddress() {
  const queryClient = useQueryClient();
  const authority = captureProfileRequestContext();
  const draft = useRef<EmailVerificationIntent | null>(null);
  const completed = useRef<EmailVerificationIntent | null>(null);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const current = (intent: EmailVerificationIntent) => {
    if (!mounted.current || draft.current !== intent) return false;
    try {
      requireNotificationAuthority(intent.authority);
      return true;
    } catch {
      return false;
    }
  };
  const mutation = useMutation({
    retry: false,
    mutationFn: (intent: EmailVerificationIntent) => {
      if (!current(intent)) throw new StaleApiRequestContextError();
      return queueNotificationEmailVerification(intent);
    },
    onSuccess: (receipt, intent) => {
      if (!current(intent)) return;
      completed.current = intent;
      void queryClient.invalidateQueries({
        queryKey: [...notificationKeys.emailPreferences(), intent.scope],
        exact: true,
      });
      toast.success(
        receipt.current
          ? "Verification request queued."
          : "This verification request is no longer pending.",
      );
    },
    onError: (error, intent) => {
      if (!current(intent) || error instanceof StaleApiRequestContextError) return;
      toast.error(error instanceof Error ? error.message : "Failed to queue verification request");
    },
  });
  type Options = NonNullable<Parameters<typeof mutation.mutate>[1]>;
  const callbacks = (options: Options | undefined, intent: EmailVerificationIntent): Options => ({
    ...options,
    onSuccess: (...args) => {
      if (current(intent)) options?.onSuccess?.(...args);
    },
    onError: (...args) => {
      if (current(intent)) options?.onError?.(...args);
    },
    onSettled: (...args) => {
      if (current(intent)) options?.onSettled?.(...args);
    },
  });
  const capture = (email: string) => {
    const intent = captureEmailVerificationIntent(
      email,
      authority,
      completed.current === draft.current ? null : draft.current,
    );
    draft.current = intent;
    return intent;
  };
  return {
    ...mutation,
    mutate: (email: string, options?: Options) => {
      try {
        const intent = capture(email);
        mutation.mutate(intent, callbacks(options, intent));
      } catch (error) {
        if (!(error instanceof StaleApiRequestContextError)) throw error;
      }
    },
    mutateAsync: async (email: string, options?: Options) => {
      const intent = capture(email);
      return mutation.mutateAsync(intent, callbacks(options, intent));
    },
  };
}

export function useClearEmailNotificationAddress() {
  const queryClient = useQueryClient();
  const context = captureProfileRequestContext();
  const mutation = useMutation({
    retry: false,
    mutationFn: (authority: ReturnType<typeof captureProfileRequestContext>) => {
      if (!authority) throw new StaleApiRequestContextError();
      return clearNotificationEmailAddress(authority);
    },
    onSuccess: (prefs, authority) => {
      if (!authority) return;
      requireNotificationAuthority(authority);
      queryClient.setQueryData(
        [...notificationKeys.emailPreferences(), notificationScope(authority)],
        prefs,
      );
    },
    onError: (error, authority) => {
      if (!authority) return;
      try {
        requireNotificationAuthority(authority);
      } catch {
        return;
      }
      if (error instanceof StaleApiRequestContextError) return;
      toast.error(error instanceof Error ? error.message : "Failed to remove the custom address");
    },
  });
  // Pending mutations can receive new observer options after a rerender.
  // Keep the invocation's authority in variables, independent of those closures.
  return {
    ...mutation,
    mutate: (_?: void, options?: Parameters<typeof mutation.mutate>[1]) =>
      mutation.mutate(context, options),
    mutateAsync: (_?: void, options?: Parameters<typeof mutation.mutateAsync>[1]) =>
      mutation.mutateAsync(context, options),
  };
}

export function useDiscordNotificationPreferences(enabled = true) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...notificationKeys.discordPreferences(), notificationScope(context)],
    queryFn: () => getNotificationDiscordPreferences(context ?? captureNotificationAuthority()),
    enabled: enabled && context !== null,
    retry: false,
  });
}

export function useUpdateDiscordNotificationPreferences() {
  const queryClient = useQueryClient();
  const context = captureProfileRequestContext();
  return useMutation({
    retry: false,
    mutationFn: (update: { mode: NotificationDiscordMode }) =>
      updateNotificationDiscordPreferences(update, context ?? captureNotificationAuthority()),
    onSuccess: (prefs) => {
      if (!context) return;
      requireNotificationAuthority(context);
      queryClient.setQueryData(
        [...notificationKeys.discordPreferences(), notificationScope(context)],
        prefs,
      );
    },
    onError: (error) => {
      if (error instanceof StaleApiRequestContextError) return;
      toast.error(error instanceof Error ? error.message : "Failed to save discord preferences");
    },
  });
}

/** Starts the Discord account-link OAuth flow; navigate to the returned URL. */
export function useDiscordLinkInit() {
  const context = captureProfileRequestContext();
  const inFlight = useRef(false);
  return useMutation({
    retry: false,
    mutationFn: async () => {
      if (!context) throw new StaleApiRequestContextError();
      if (inFlight.current) throw new Error("Discord linking is already starting.");
      inFlight.current = true;
      try {
        return await beginNotificationDiscordLink(context);
      } finally {
        inFlight.current = false;
      }
    },
    onError: (error) => {
      if (!context) return;
      try {
        requireNotificationAuthority(context);
      } catch {
        return;
      }
      toast.error(error instanceof Error ? error.message : "Failed to start Discord link");
    },
  });
}

export function useUnlinkDiscord() {
  const queryClient = useQueryClient();
  const context = captureProfileRequestContext();
  return useMutation({
    retry: false,
    mutationFn: async () => {
      if (!context) throw new StaleApiRequestContextError();
      await unlinkNotificationDiscord(context);
      return context;
    },
    onSuccess: (authority) => {
      requireNotificationAuthority(authority);
      void queryClient.invalidateQueries({
        queryKey: [...notificationKeys.discordPreferences(), notificationScope(authority)],
        exact: true,
      });
      toast.success("Discord account unlinked");
    },
    onError: () => {
      if (!context) return;
      try {
        requireNotificationAuthority(context);
      } catch {
        return;
      }
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
    queryClient.setQueryData<NotificationsInfiniteData>(
      notificationInboxKeys.list(status),
      (data) =>
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
  const context = captureProfileRequestContext();
  if (!context || notification.profile_id !== context.profileId) return;
  queryClient.setQueryData<NotificationsInfiniteData>(notificationInboxKeys.list("all"), (data) => {
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
  void queryClient.invalidateQueries({ queryKey: notificationInboxKeys.list("unread") });
  if (!notification.read_at) {
    queryClient.setQueryData<number>(notificationInboxKeys.count(), (count) => (count ?? 0) + 1);
  }
}

/** Applies a read event (single id or all) to cached rows and the badge. */
export function applyNotificationRead(
  queryClient: QueryClient,
  payload: NotificationReadEventPayload & { through_created_at?: string; through_id?: string },
) {
  const context = captureProfileRequestContext();
  if (!context || (payload.profile_id && payload.profile_id !== context.profileId)) return;
  if (payload.through_created_at && payload.through_id) {
    // The wire list timestamps have millisecond precision; the delivery cutoff
    // can retain microseconds. Re-read instead of guessing tuple order locally.
    invalidateInbox(queryClient, notificationScope(context));
    return;
  }
  const readAt = new Date().toISOString();
  if (payload.all) {
    updateCachedLists(queryClient, (entry) =>
      entry.read_at ? entry : { ...entry, read_at: readAt },
    );
    queryClient.setQueryData<number>(notificationInboxKeys.count(), 0);
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
    queryClient.setQueryData<number>(notificationInboxKeys.count(), (count) =>
      count == null ? count : Math.max(0, count - 1),
    );
  }
}

/** Hydrates the unread badge from the websocket snapshot (recent unread rows). */
export function applyNotificationsSnapshot(queryClient: QueryClient, rows: AppNotification[]) {
  const context = captureProfileRequestContext();
  if (!context) return;
  rows = rows.filter((row) => row.profile_id === context.profileId);
  // The snapshot is capped (25 rows); use it as a lower bound and refresh the
  // exact count only when the cap means the lower bound may be incomplete.
  queryClient.setQueryData<number>(notificationInboxKeys.count(), (count) =>
    Math.max(count ?? 0, rows.length),
  );
  if (rows.length >= NOTIFICATIONS_PAGE_SIZE) {
    void queryClient.invalidateQueries({ queryKey: notificationInboxKeys.count() });
  }
  void queryClient.invalidateQueries({
    queryKey: notificationInboxKeys.list("all"),
    refetchType: "active",
  });
  void queryClient.invalidateQueries({
    queryKey: notificationInboxKeys.list("unread"),
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
