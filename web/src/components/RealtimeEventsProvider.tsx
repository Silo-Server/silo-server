import type { QueryClient } from "@tanstack/react-query";
import { useQueryClient } from "@tanstack/react-query";
import type {
  AdminJob,
  AdminSession,
  AppNotification,
  EventChannel,
  EventsEventMessage,
  EventsSnapshotMessage,
  EventsStreamMessage,
  HistoryImportRun,
  NotificationReadEventPayload,
  ScanRun,
  TaskInfo,
} from "@/api/types";
import { api, getAccessToken } from "@/api/client";
import {
  applyNotificationCreated,
  applyNotificationRead,
  applyNotificationsSnapshot,
  formatEpisodeCode,
} from "@/hooks/queries/notifications";
import { toast } from "sonner";
import {
  RealtimeEventsContext,
  type EventChannelHandlers,
  type RealtimeConnectionState,
  type RealtimeEventsContextValue,
} from "@/components/realtimeEventsContext";
import { invalidateCatalogState } from "@/components/realtimeCatalogInvalidation";
import { useAuth } from "@/hooks/useAuth";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import { usePageActivity } from "@/hooks/usePageActivity";
import { adminKeys, historyImportKeys, libraryKeys } from "@/hooks/queries/keys";
import {
  invalidateMediaSurfaceQueries,
  updateCatalogItemDetail,
} from "@/hooks/queries/mediaSurfaceRefresh";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";
import type { ReactNode } from "react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useLocation } from "react-router";

interface JobWaiter {
  timeoutId: number;
  resolve: (job: AdminJob) => void;
  reject: (error: Error) => void;
}

interface UserStatePayload {
  profile_id: string;
  content_id?: string;
  series_id?: string;
  change: "progress" | "favorite" | "watchlist" | "history" | "watched" | "home_dismissal";
  played?: boolean;
  is_favorite?: boolean;
  in_watchlist?: boolean;
}

const CATALOG_ITEM_CHANGED_EVENTS = new Set([
  "catalog.item.changed",
  "library.item_added",
  "metadata.updated",
]);
const SCOPED_CATALOG_LIBRARY_EVENTS = new Set(["catalog.library.changed", "library.changed"]);
const DASHBOARD_QUERY_KEYS = [
  adminKeys.stats(),
  adminKeys.sessions(),
  adminKeys.libraries(),
  adminKeys.users(),
] as const;

function buildEventsUrl(
  token: string | null,
  location: Pick<Location, "protocol" | "host">,
  ticket?: string | null,
) {
  const protocol = location.protocol === "https:" ? "wss:" : "ws:";
  const search = new URLSearchParams();
  if (token) {
    search.set("token", token);
  }
  if (ticket) {
    search.set("ticket", ticket);
  }
  return `${protocol}//${location.host}/api/v1/events/ws${search.toString() ? `?${search.toString()}` : ""}`;
}

/**
 * Mints a short-lived single-use websocket ticket binding the connection to
 * the active profile (required for the notifications channel). Returns null
 * when no profile is active or the mint fails — the connection then proceeds
 * unbound, and the subscribed-message handler retries the binding with
 * backoff when the notifications subscription is rejected.
 */
async function mintEventsTicket(hasProfile: boolean): Promise<string | null> {
  if (!hasProfile) {
    return null;
  }
  try {
    const response = await api<{ ticket: string }>("/events/ws-ticket", {
      method: "POST",
      // A hung mint must settle: connect() awaits this before any socket
      // exists, so without a timeout no onclose fires and no reconnect is
      // ever scheduled — realtime would stay "connecting" forever.
      signal: AbortSignal.timeout(10_000),
    });
    return response.ticket || null;
  } catch {
    return null;
  }
}

function parseEventsMessage(value: unknown): EventsStreamMessage | null {
  if (typeof value !== "string") {
    return null;
  }

  try {
    const parsed = JSON.parse(value) as EventsStreamMessage;
    if (!parsed || typeof parsed.type !== "string") {
      return null;
    }
    return parsed;
  } catch {
    return null;
  }
}

function isTerminalJob(job: AdminJob) {
  return job.status === "completed" || job.status === "failed" || job.status === "cancelled";
}

async function pollAdminJobUntilTerminal(jobId: string): Promise<AdminJob> {
  for (;;) {
    const job = await api<AdminJob>(`/admin/jobs/${jobId}`);
    if (job.status === "completed") {
      return job;
    }
    if (job.status === "failed") {
      throw new Error(job.error_message || job.message || "Job failed");
    }
    if (job.status === "cancelled") {
      throw new Error(job.message || "Job cancelled");
    }
    await new Promise((resolve) => window.setTimeout(resolve, 1_000));
  }
}

function sortJobs(jobs: AdminJob[]) {
  return [...jobs].sort((left, right) => {
    const leftTime = Date.parse(left.requested_at);
    const rightTime = Date.parse(right.requested_at);
    if (Number.isNaN(leftTime) || Number.isNaN(rightTime)) {
      const leftID = left.id ?? "";
      const rightID = right.id ?? "";
      return rightID.localeCompare(leftID);
    }
    return rightTime - leftTime;
  });
}

function upsertJob(existing: AdminJob[] | undefined, nextJob: AdminJob, limit = 50) {
  if (!nextJob || !nextJob.id) {
    return existing ?? [];
  }
  const jobs = existing ? [...existing] : [];
  const index = jobs.findIndex((job) => job.id === nextJob.id);
  if (index >= 0) {
    jobs[index] = nextJob;
  } else {
    jobs.push(nextJob);
  }
  return sortJobs(jobs).slice(0, limit);
}

function hydrateAdminJobSnapshot(queryClient: QueryClient, jobs: AdminJob[]) {
  const sorted = sortJobs(jobs);
  queryClient.setQueryData<AdminJob[]>(adminKeys.jobs("__all"), sorted);

  const jobsByType = new Map<string, AdminJob[]>();
  for (const job of sorted) {
    const list = jobsByType.get(job.job_type) ?? [];
    list.push(job);
    jobsByType.set(job.job_type, list);
  }
  for (const [jobType, entries] of jobsByType) {
    queryClient.setQueryData<AdminJob[]>(adminKeys.jobs(jobType), entries);
  }
}

function applyAdminJobUpdate(queryClient: QueryClient, job: AdminJob) {
  queryClient.setQueryData<AdminJob[]>(adminKeys.jobs(job.job_type), (existing) =>
    upsertJob(existing, job, 50),
  );
  queryClient.setQueryData<AdminJob[]>(adminKeys.jobs("__all"), (existing) =>
    upsertJob(existing, job, 50),
  );
}

function findCachedAdminJob(queryClient: QueryClient, jobId: string) {
  const matches = queryClient.getQueryCache().findAll({
    predicate: (query) =>
      Array.isArray(query.queryKey) &&
      query.queryKey[0] === "admin" &&
      query.queryKey[1] === "jobs",
  });

  for (const query of matches) {
    const jobs = query.state.data as AdminJob[] | undefined;
    const job = jobs?.find((entry) => entry.id === jobId);
    if (job) {
      return job;
    }
  }

  return null;
}

function invalidateDashboardQueries(queryClient: QueryClient, allowRefetch: boolean) {
  for (const queryKey of DASHBOARD_QUERY_KEYS) {
    void queryClient.invalidateQueries({
      queryKey,
      refetchType: allowRefetch ? "active" : "none",
    });
  }
}

function catalogEventLibraryID(data: unknown) {
  if (!data || typeof data !== "object" || !("library_id" in data)) {
    return undefined;
  }
  const value = (data as { library_id?: unknown }).library_id;
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function handleJobSideEffects(
  queryClient: QueryClient,
  job: AdminJob,
  eventName: string,
  allowDashboardRefetch: boolean,
) {
  if (job.job_type === "delete_library") {
    void queryClient.invalidateQueries({
      queryKey: adminKeys.libraries(),
      refetchType: allowDashboardRefetch ? "active" : "none",
    });
    void queryClient.invalidateQueries({ queryKey: libraryKeys.all });
  }

  if (eventName === "job.completed" && job.job_type === "catalog_import") {
    invalidateCatalogState(queryClient, { allowDashboardRefetch });
  }

  if (eventName === "job.completed" && job.job_type === "delete_library") {
    invalidateCatalogState(queryClient, { allowDashboardRefetch });
  }
}

function hydrateSessions(
  queryClient: QueryClient,
  sessions: AdminSession[],
  allowDashboardUpdates: boolean,
) {
  if (!allowDashboardUpdates) {
    invalidateDashboardQueries(queryClient, false);
    return;
  }
  queryClient.setQueryData(adminKeys.sessions(), sessions);
  void queryClient.invalidateQueries({ queryKey: adminKeys.stats() });
}

function hydrateTasks(queryClient: QueryClient, tasks: TaskInfo[]) {
  queryClient.setQueryData(adminKeys.tasks(), tasks);
  for (const task of tasks) {
    queryClient.setQueryData(adminKeys.task(task.key), task);
  }
}

function applyTaskUpdate(queryClient: QueryClient, task: TaskInfo) {
  const previousTask = queryClient.getQueryData<TaskInfo>(adminKeys.task(task.key));
  queryClient.setQueryData<TaskInfo[]>(adminKeys.tasks(), (existing) => {
    const tasks = existing ? [...existing] : [];
    const index = tasks.findIndex((entry) => entry.key === task.key);
    if (index >= 0) {
      tasks[index] = task;
    } else {
      tasks.push(task);
    }
    return tasks.sort((left, right) => left.key.localeCompare(right.key));
  });
  queryClient.setQueryData(adminKeys.task(task.key), task);

  if (
    task.state === "idle" &&
    task.last_execution?.completed_at &&
    previousTask?.last_execution?.completed_at !== task.last_execution.completed_at
  ) {
    void queryClient.invalidateQueries({ queryKey: adminKeys.taskHistory(task.key) });
    void queryClient.invalidateQueries({ queryKey: adminKeys.taskMetrics(task.key) });
  }
}

function hydrateScans(queryClient: QueryClient, scans: ScanRun[]) {
  queryClient.setQueryData(adminKeys.activeScans(), scans);
}

function applyScanUpdate(queryClient: QueryClient, scan: ScanRun, eventName: string) {
  queryClient.setQueryData<ScanRun[]>(adminKeys.activeScans(), (existing) => {
    const scans = existing ? [...existing] : [];
    const index = scans.findIndex((entry) => entry.id === scan.id);
    const active = scan.status === "accepted" || scan.status === "running";
    if (index >= 0 && !active) {
      scans.splice(index, 1);
    } else if (index >= 0) {
      scans[index] = scan;
    } else if (active) {
      scans.push(scan);
    }
    return scans;
  });

  if (
    eventName === "scan.completed" ||
    eventName === "scan.failed" ||
    eventName === "scan.cancelled"
  ) {
    void queryClient.invalidateQueries({ queryKey: adminKeys.libraries() });
    void queryClient.invalidateQueries({ queryKey: adminKeys.libraryMatchQueueStatuses() });
  }
}

function updateHistoryImportCaches(queryClient: QueryClient, run?: HistoryImportRun) {
  if (run) {
    queryClient.setQueryData(historyImportKeys.run(run.id), run);
    queryClient.setQueryData(adminKeys.historyImportAdminRun(run.id), run);
  }

  void queryClient.invalidateQueries({
    predicate: (query) =>
      Array.isArray(query.queryKey) &&
      query.queryKey[0] === historyImportKeys.all[0] &&
      query.queryKey[1] === "runs",
  });
  void queryClient.invalidateQueries({
    predicate: (query) =>
      Array.isArray(query.queryKey) &&
      query.queryKey[0] === adminKeys.historyImportAdminRuns({})[0] &&
      query.queryKey[1] === adminKeys.historyImportAdminRuns({})[1],
  });
  void queryClient.invalidateQueries({
    predicate: (query) =>
      Array.isArray(query.queryKey) &&
      query.queryKey[0] === adminKeys.historyImportMappings(undefined)[0] &&
      query.queryKey[1] === adminKeys.historyImportMappings(undefined)[1],
  });
}

function handleUserStateEvent(
  queryClient: QueryClient,
  payload: UserStatePayload,
  activeProfileID: string | null | undefined,
  allowDashboardRefetch: boolean,
) {
  if (payload.profile_id && activeProfileID && payload.profile_id !== activeProfileID) {
    return;
  }

  if (payload.content_id) {
    updateCatalogItemDetail(queryClient, payload.content_id, (detail) => ({
      ...detail,
      user_state: {
        played: payload.played ?? detail.user_state?.played ?? false,
        is_favorite: payload.is_favorite ?? detail.user_state?.is_favorite ?? false,
        in_watchlist: payload.in_watchlist ?? detail.user_state?.in_watchlist ?? false,
      },
    }));
  }

  void invalidateMediaSurfaceQueries(
    queryClient,
    payload.content_id ? { itemId: payload.content_id } : {},
  ).then(() => {
    bumpHomeRefreshSignal(queryClient);
  });
  void queryClient.invalidateQueries({
    queryKey: adminKeys.stats(),
    refetchType: allowDashboardRefetch ? "active" : "none",
  });
}

export function RealtimeEventsProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const { user, profile } = useAuth();
  const actingAdmin = useIsActingAdmin();
  const pageActivity = usePageActivity();
  const location = useLocation();
  const authenticatedUserID = user?.id ?? null;
  const isDashboardRoute = location.pathname === "/admin" || location.pathname === "/admin/";
  const allowDashboardRealtimeUpdates = !isDashboardRoute || pageActivity.canPollDashboard;
  const [connectionState, setConnectionState] = useState<RealtimeConnectionState>("connecting");
  const reconnectTimerRef = useRef<number | undefined>(undefined);
  const profileRebindAttemptsRef = useRef(0);
  const nextReconnectDelayRef = useRef<number | null>(null);
  const socketRef = useRef<WebSocket | null>(null);
  const helloReceivedRef = useRef(false);
  const requestCounterRef = useRef(0);
  const channelRefsRef = useRef(new Map<EventChannel, number>());
  const channelHandlersRef = useRef(new Map<EventChannel, Map<number, EventChannelHandlers>>());
  const nextHandlerIDRef = useRef(1);
  const waitersRef = useRef(new Map<string, JobWaiter>());
  const activeProfileIDRef = useRef<string | null | undefined>(profile?.id);
  const canApplyRealtimeUpdatesRef = useRef(pageActivity.canApplyRealtimeUpdates);
  const allowDashboardRealtimeUpdatesRef = useRef(allowDashboardRealtimeUpdates);
  const shouldCatchUpOnFocusRef = useRef(!pageActivity.canApplyRealtimeUpdates);

  activeProfileIDRef.current = profile?.id;
  canApplyRealtimeUpdatesRef.current = pageActivity.canApplyRealtimeUpdates;
  allowDashboardRealtimeUpdatesRef.current = allowDashboardRealtimeUpdates;

  const settleWaiterRef = useRef<(job: AdminJob) => void>(() => {});
  settleWaiterRef.current = (job: AdminJob) => {
    const waiter = waitersRef.current.get(job.id);
    if (!waiter || !isTerminalJob(job)) {
      return;
    }
    waitersRef.current.delete(job.id);
    window.clearTimeout(waiter.timeoutId);
    if (job.status === "completed") {
      waiter.resolve(job);
      return;
    }
    if (job.status === "cancelled") {
      waiter.reject(new Error(job.message || "Job cancelled"));
      return;
    }
    waiter.reject(new Error(job.error_message || job.message || "Job failed"));
  };

  function currentChannels() {
    return Array.from(channelRefsRef.current.entries())
      .filter(([, count]) => count > 0)
      .map(([channel]) => channel);
  }

  function sendSubscribe() {
    const socket = socketRef.current;
    if (!socket || socket.readyState !== WebSocket.OPEN || !helloReceivedRef.current) {
      return;
    }
    requestCounterRef.current += 1;
    socket.send(
      JSON.stringify({
        type: "subscribe",
        request_id: `req-${requestCounterRef.current}`,
        channels: currentChannels(),
      }),
    );
  }

  function dispatchChannelMessage(
    channel: EventChannel,
    kind: "snapshot" | "event",
    message: EventsSnapshotMessage | EventsEventMessage,
  ) {
    const handlers = channelHandlersRef.current.get(channel);
    if (!handlers) {
      return;
    }
    for (const entry of handlers.values()) {
      if (kind === "snapshot") {
        entry.onSnapshot?.(message);
      } else {
        entry.onEvent?.(message);
      }
    }
  }

  function handleSnapshot(message: EventsSnapshotMessage) {
    switch (message.channel) {
      case "jobs":
        if (Array.isArray(message.data)) {
          hydrateAdminJobSnapshot(queryClient, message.data as AdminJob[]);
          for (const job of message.data as AdminJob[]) {
            settleWaiterRef.current(job);
          }
        }
        break;
      case "sessions":
        hydrateSessions(
          queryClient,
          (message.data as AdminSession[]) ?? [],
          allowDashboardRealtimeUpdatesRef.current,
        );
        break;
      case "tasks":
        hydrateTasks(queryClient, (message.data as TaskInfo[]) ?? []);
        break;
      case "scans":
        hydrateScans(queryClient, (message.data as ScanRun[]) ?? []);
        break;
      case "history_import":
        updateHistoryImportCaches(queryClient);
        break;
      case "notifications":
        if (Array.isArray(message.data)) {
          applyNotificationsSnapshot(queryClient, message.data as AppNotification[]);
        }
        break;
      default:
        break;
    }
    dispatchChannelMessage(message.channel, "snapshot", message);
  }

  function handleNotificationEvent(message: EventsEventMessage) {
    if (message.event === "notification.created") {
      const notification = message.data as AppNotification;
      if (
        notification.profile_id &&
        activeProfileIDRef.current &&
        notification.profile_id !== activeProfileIDRef.current
      ) {
        return;
      }
      applyNotificationCreated(queryClient, notification);
      if (notification.type === "episode.available" && notification.series_title) {
        const episodeCode = formatEpisodeCode(notification);
        toast(`New episode of ${notification.series_title}`, {
          description: [episodeCode, notification.episode_title].filter(Boolean).join(" — "),
        });
      } else if (notification.type === "request.fulfilled") {
        toast(
          notification.series_title
            ? `${notification.series_title} is now available`
            : "Your request is now available",
          { description: "Your media request has arrived in the library." },
        );
      } else if (
        notification.type === "request.approved" ||
        notification.type === "request.declined"
      ) {
        const verb = notification.type === "request.approved" ? "approved" : "declined";
        const title = notification.reason_flags?.title;
        toast(title ? `Request ${verb}: ${title}` : `Your request was ${verb}`, {
          description:
            notification.type === "request.declined"
              ? notification.reason_flags?.reason
              : undefined,
        });
      }
      return;
    }
    if (message.event === "notification.read") {
      applyNotificationRead(queryClient, message.data as NotificationReadEventPayload);
    }
  }

  function handleEvent(message: EventsEventMessage) {
    switch (message.channel) {
      case "catalog":
        {
          const eventLibraryID = catalogEventLibraryID(message.data);
          if (CATALOG_ITEM_CHANGED_EVENTS.has(message.event)) {
            invalidateCatalogState(queryClient, {
              itemId:
                typeof message.data === "object" && message.data && "content_id" in message.data
                  ? (message.data as { content_id?: string }).content_id
                  : undefined,
              libraryId: eventLibraryID,
              allowDashboardRefetch: allowDashboardRealtimeUpdatesRef.current,
              includeLibraryLists: false,
            });
          } else if (SCOPED_CATALOG_LIBRARY_EVENTS.has(message.event) && eventLibraryID) {
            invalidateCatalogState(queryClient, {
              libraryId: eventLibraryID,
              allowDashboardRefetch: allowDashboardRealtimeUpdatesRef.current,
            });
          } else {
            invalidateCatalogState(queryClient, {
              libraryId: eventLibraryID,
              allowDashboardRefetch: allowDashboardRealtimeUpdatesRef.current,
            });
          }
        }
        break;
      case "jobs":
        applyAdminJobUpdate(queryClient, message.data as AdminJob);
        handleJobSideEffects(
          queryClient,
          message.data as AdminJob,
          message.event,
          allowDashboardRealtimeUpdatesRef.current,
        );
        settleWaiterRef.current(message.data as AdminJob);
        break;
      case "sessions":
        if (message.event === "sessions.replaced") {
          hydrateSessions(
            queryClient,
            (message.data as AdminSession[]) ?? [],
            allowDashboardRealtimeUpdatesRef.current,
          );
        }
        break;
      case "tasks":
        if (message.event === "task.updated") {
          applyTaskUpdate(queryClient, message.data as TaskInfo);
        }
        break;
      case "scans":
        applyScanUpdate(queryClient, message.data as ScanRun, message.event);
        break;
      case "history_import":
        updateHistoryImportCaches(queryClient, message.data as HistoryImportRun);
        break;
      case "user_state":
        handleUserStateEvent(
          queryClient,
          message.data as UserStatePayload,
          activeProfileIDRef.current,
          allowDashboardRealtimeUpdatesRef.current,
        );
        break;
      case "notifications":
        handleNotificationEvent(message);
        break;
      default:
        break;
    }
    dispatchChannelMessage(message.channel, "event", message);
  }

  useEffect(() => {
    if (!authenticatedUserID) {
      return;
    }
    if (!pageActivity.canApplyRealtimeUpdates) {
      shouldCatchUpOnFocusRef.current = true;
      return;
    }
    if (!shouldCatchUpOnFocusRef.current) {
      return;
    }

    shouldCatchUpOnFocusRef.current = false;
    void queryClient.refetchQueries({
      type: "active",
      predicate: (query) => !isDashboardQueryKey(query.queryKey),
    });
  }, [authenticatedUserID, pageActivity.canApplyRealtimeUpdates, queryClient]);

  useEffect(() => {
    if (!authenticatedUserID || !pageActivity.canApplyRealtimeUpdates) {
      setConnectionState("disconnected");
      return;
    }

    let closedByEffect = false;
    let activeSocket: WebSocket | null = null;

    const clearReconnect = () => {
      if (reconnectTimerRef.current !== undefined) {
        window.clearTimeout(reconnectTimerRef.current);
        reconnectTimerRef.current = undefined;
      }
    };

    const scheduleReconnect = () => {
      if (closedByEffect || reconnectTimerRef.current !== undefined) {
        return;
      }
      // The profile-rebind path stretches the delay so a persistently failing
      // ticket mint cannot turn into a tight reconnect loop.
      const delay = nextReconnectDelayRef.current ?? 1_000;
      nextReconnectDelayRef.current = null;
      reconnectTimerRef.current = window.setTimeout(() => {
        reconnectTimerRef.current = undefined;
        if (closedByEffect) {
          return;
        }
        connect();
      }, delay);
    };

    const connect = () => {
      if (closedByEffect) {
        return;
      }
      setConnectionState("connecting");
      helloReceivedRef.current = false;

      // The ticket binds the connection to the active profile so the server
      // can authorize the notifications channel. Failure degrades gracefully
      // to an unbound connection; without a profile we connect synchronously.
      if (!activeProfileIDRef.current) {
        openSocket(null);
        return;
      }
      void mintEventsTicket(true).then((ticket) => {
        if (closedByEffect) {
          return;
        }
        openSocket(ticket);
      });
    };

    const openSocket = (ticket: string | null) => {
      let socket: WebSocket;
      try {
        socket = new WebSocket(buildEventsUrl(getAccessToken(), window.location, ticket));
      } catch {
        setConnectionState("disconnected");
        scheduleReconnect();
        return;
      }

      activeSocket = socket;
      socketRef.current = socket;

      socket.onopen = () => {
        if (closedByEffect || socketRef.current !== socket) {
          return;
        }
        setConnectionState("live");
      };

      socket.onmessage = (event) => {
        if (closedByEffect || socketRef.current !== socket) {
          return;
        }
        if (!canApplyRealtimeUpdatesRef.current) {
          return;
        }
        const message = parseEventsMessage(event.data);
        if (!message) {
          return;
        }

        switch (message.type) {
          case "hello":
            helloReceivedRef.current = true;
            sendSubscribe();
            return;
          case "subscribed": {
            // A profile_required rejection means the profile binding was lost
            // (the ticket mint failed or the ticket was not honored). Left
            // alone, this socket would stay healthy for hours while silently
            // delivering no notifications — reconnect with backoff to re-mint.
            const profileRequired = (message.rejected ?? []).some(
              (entry) => entry.channel === "notifications" && entry.code === "profile_required",
            );
            if (profileRequired && activeProfileIDRef.current) {
              // Rebinding requires a fresh handshake (tickets are consumed at
              // upgrade time), so the shared socket must close. The backoff
              // grows to 5 minutes so a persistent notifications-only outage
              // costs the catalog/user_state channels one brief flap per
              // cycle instead of a permanent fast reconnect loop.
              profileRebindAttemptsRef.current += 1;
              nextReconnectDelayRef.current = Math.min(
                300_000,
                1_000 * 2 ** Math.min(profileRebindAttemptsRef.current, 9),
              );
              socket.close();
            } else {
              profileRebindAttemptsRef.current = 0;
            }
            return;
          }
          case "snapshot":
            handleSnapshot(message);
            return;
          case "event":
            handleEvent(message);
            return;
          case "error":
            return;
        }
      };

      socket.onerror = () => {
        if (closedByEffect || socketRef.current !== socket) {
          return;
        }
        setConnectionState("disconnected");
      };

      socket.onclose = () => {
        if (socketRef.current !== socket) {
          return;
        }
        socketRef.current = null;
        activeSocket = null;
        helloReceivedRef.current = false;
        setConnectionState("disconnected");
        if (!closedByEffect) {
          scheduleReconnect();
        }
      };
    };

    connect();

    return () => {
      closedByEffect = true;
      clearReconnect();
      for (const [jobId, waiter] of waitersRef.current) {
        window.clearTimeout(waiter.timeoutId);
        waiter.reject(new Error(`Realtime events provider closed before job ${jobId} finished`));
      }
      waitersRef.current.clear();
      const socket = activeSocket;
      if (socket && socketRef.current === socket) {
        socketRef.current = null;
      }
      activeSocket = null;
      if (
        socket &&
        (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)
      ) {
        socket.close();
      }
    };
    // profile?.id is a dependency on purpose: the websocket binds to the
    // active profile via the handshake ticket, so a profile switch must
    // reconnect (and resubscribe) under the new identity.
  }, [authenticatedUserID, profile?.id, pageActivity.canApplyRealtimeUpdates, queryClient]);

  const value = useMemo<RealtimeEventsContextValue>(
    () => ({
      connectionState,
      awaitAdminJob: (jobId: string) => {
        const cachedJob = findCachedAdminJob(queryClient, jobId);
        if (cachedJob) {
          if (cachedJob.status === "completed") {
            return Promise.resolve(cachedJob);
          }
          if (cachedJob.status === "failed") {
            return Promise.reject(
              new Error(cachedJob.error_message || cachedJob.message || "Job failed"),
            );
          }
          if (cachedJob.status === "cancelled") {
            return Promise.reject(new Error(cachedJob.message || "Job cancelled"));
          }
        }

        // Must match the gate on AdminRealtimeEventChannels: when the jobs
        // channel isn't subscribed (not acting as admin), waiting on a live
        // event would hang until the fallback timeout — poll instead.
        if (!actingAdmin || connectionState !== "live") {
          return pollAdminJobUntilTerminal(jobId);
        }

        return new Promise<AdminJob>((resolve, reject) => {
          const existing = waitersRef.current.get(jobId);
          if (existing) {
            window.clearTimeout(existing.timeoutId);
          }

          const timeoutId = window.setTimeout(() => {
            waitersRef.current.delete(jobId);
            void pollAdminJobUntilTerminal(jobId).then(resolve).catch(reject);
          }, 60_000);

          waitersRef.current.set(jobId, { timeoutId, resolve, reject });
        });
      },
      subscribeChannel: (channel: EventChannel, handlers?: EventChannelHandlers) => {
        channelRefsRef.current.set(channel, (channelRefsRef.current.get(channel) ?? 0) + 1);

        let handlerID: number | null = null;
        if (handlers) {
          handlerID = nextHandlerIDRef.current++;
          const channelHandlers =
            channelHandlersRef.current.get(channel) ?? new Map<number, EventChannelHandlers>();
          channelHandlers.set(handlerID, handlers);
          channelHandlersRef.current.set(channel, channelHandlers);
        }

        sendSubscribe();

        return () => {
          const current = channelRefsRef.current.get(channel) ?? 0;
          if (current <= 1) {
            channelRefsRef.current.delete(channel);
          } else {
            channelRefsRef.current.set(channel, current - 1);
          }

          if (handlerID != null) {
            const channelHandlers = channelHandlersRef.current.get(channel);
            channelHandlers?.delete(handlerID);
            if (channelHandlers && channelHandlers.size === 0) {
              channelHandlersRef.current.delete(channel);
            }
          }

          sendSubscribe();
        };
      },
    }),
    [connectionState, queryClient, actingAdmin],
  );

  return <RealtimeEventsContext.Provider value={value}>{children}</RealtimeEventsContext.Provider>;
}

export { buildEventsUrl };

function isDashboardQueryKey(queryKey: unknown) {
  return (
    Array.isArray(queryKey) &&
    queryKey[0] === "admin" &&
    (queryKey[1] === "stats" ||
      queryKey[1] === "sessions" ||
      queryKey[1] === "libraries" ||
      queryKey[1] === "users")
  );
}
