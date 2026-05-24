import type { QueryClient } from "@tanstack/react-query";
import { useQueryClient } from "@tanstack/react-query";
import type {
  AdminJob,
  AdminSession,
  EventChannel,
  EventsEventMessage,
  EventsSnapshotMessage,
  EventsStreamMessage,
  HistoryImportRun,
  ScanRun,
  TaskInfo,
} from "@/api/types";
import { api, getAccessToken } from "@/api/client";
import {
  RealtimeEventsContext,
  type EventChannelHandlers,
  type RealtimeConnectionState,
  type RealtimeEventsContextValue,
} from "@/components/realtimeEventsContext";
import { useAuth } from "@/hooks/useAuth";
import { adminKeys, historyImportKeys, libraryKeys } from "@/hooks/queries/keys";
import { invalidateMediaSurfaceQueries } from "@/hooks/queries/mediaSurfaceRefresh";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";
import type { ReactNode } from "react";
import { useEffect, useMemo, useRef, useState } from "react";

interface JobWaiter {
  timeoutId: number;
  resolve: (job: AdminJob) => void;
  reject: (error: Error) => void;
}

interface UserStatePayload {
  profile_id: string;
  content_id?: string;
  series_id?: string;
  change: "progress" | "favorite" | "watchlist" | "history" | "watched";
}

function buildEventsUrl(token: string | null, location: Pick<Location, "protocol" | "host">) {
  const protocol = location.protocol === "https:" ? "wss:" : "ws:";
  const search = new URLSearchParams();
  if (token) {
    search.set("token", token);
  }
  return `${protocol}//${location.host}/api/v1/events/ws${search.toString() ? `?${search.toString()}` : ""}`;
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
  return job.status === "completed" || job.status === "failed";
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

function invalidateCatalogState(queryClient: QueryClient, itemId?: string) {
  void invalidateMediaSurfaceQueries(queryClient, itemId ? { itemId } : {}).then(() => {
    bumpHomeRefreshSignal(queryClient);
  });
  void queryClient.invalidateQueries({ queryKey: adminKeys.libraries() });
  void queryClient.invalidateQueries({ queryKey: adminKeys.stats() });
  void queryClient.invalidateQueries({ queryKey: libraryKeys.all });
}

function handleJobSideEffects(queryClient: QueryClient, job: AdminJob, eventName: string) {
  if (job.job_type === "delete_library") {
    void queryClient.invalidateQueries({ queryKey: adminKeys.libraries() });
    void queryClient.invalidateQueries({ queryKey: libraryKeys.all });
  }

  if (eventName === "job.completed" && job.job_type === "catalog_import") {
    invalidateCatalogState(queryClient);
  }

  if (eventName === "job.completed" && job.job_type === "delete_library") {
    invalidateCatalogState(queryClient);
  }
}

function hydrateSessions(queryClient: QueryClient, sessions: AdminSession[]) {
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

function handleUserStateEvent(queryClient: QueryClient, payload: UserStatePayload) {
  void invalidateMediaSurfaceQueries(
    queryClient,
    payload.content_id ? { itemId: payload.content_id } : {},
  ).then(() => {
    bumpHomeRefreshSignal(queryClient);
  });
  void queryClient.invalidateQueries({ queryKey: adminKeys.stats() });
}

export function RealtimeEventsProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const [connectionState, setConnectionState] = useState<RealtimeConnectionState>("connecting");
  const reconnectTimerRef = useRef<number | undefined>(undefined);
  const closingRef = useRef(false);
  const socketRef = useRef<WebSocket | null>(null);
  const helloReceivedRef = useRef(false);
  const requestCounterRef = useRef(0);
  const channelRefsRef = useRef(new Map<EventChannel, number>());
  const channelHandlersRef = useRef(new Map<EventChannel, Map<number, EventChannelHandlers>>());
  const nextHandlerIDRef = useRef(1);
  const waitersRef = useRef(new Map<string, JobWaiter>());

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
        hydrateSessions(queryClient, (message.data as AdminSession[]) ?? []);
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
      default:
        break;
    }
    dispatchChannelMessage(message.channel, "snapshot", message);
  }

  function handleEvent(message: EventsEventMessage) {
    switch (message.channel) {
      case "catalog":
        if (message.event === "metadata.updated") {
          invalidateCatalogState(
            queryClient,
            typeof message.data === "object" && message.data && "content_id" in message.data
              ? (message.data as { content_id?: string }).content_id
              : undefined,
          );
        } else {
          invalidateCatalogState(queryClient);
        }
        break;
      case "jobs":
        applyAdminJobUpdate(queryClient, message.data as AdminJob);
        handleJobSideEffects(queryClient, message.data as AdminJob, message.event);
        settleWaiterRef.current(message.data as AdminJob);
        break;
      case "sessions":
        if (message.event === "sessions.replaced") {
          hydrateSessions(queryClient, (message.data as AdminSession[]) ?? []);
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
        handleUserStateEvent(queryClient, message.data as UserStatePayload);
        break;
      default:
        break;
    }
    dispatchChannelMessage(message.channel, "event", message);
  }

  useEffect(() => {
    if (!user) {
      return;
    }

    closingRef.current = false;

    const clearReconnect = () => {
      if (reconnectTimerRef.current !== undefined) {
        window.clearTimeout(reconnectTimerRef.current);
        reconnectTimerRef.current = undefined;
      }
    };

    const scheduleReconnect = () => {
      if (closingRef.current || reconnectTimerRef.current !== undefined) {
        return;
      }
      reconnectTimerRef.current = window.setTimeout(() => {
        reconnectTimerRef.current = undefined;
        connect();
      }, 1_000);
    };

    const connect = () => {
      setConnectionState("connecting");
      helloReceivedRef.current = false;

      let socket: WebSocket;
      try {
        socket = new WebSocket(buildEventsUrl(getAccessToken(), window.location));
      } catch {
        setConnectionState("disconnected");
        scheduleReconnect();
        return;
      }

      socketRef.current = socket;

      socket.onopen = () => {
        setConnectionState("live");
      };

      socket.onmessage = (event) => {
        const message = parseEventsMessage(event.data);
        if (!message) {
          return;
        }

        switch (message.type) {
          case "hello":
            helloReceivedRef.current = true;
            sendSubscribe();
            return;
          case "subscribed":
            return;
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
        setConnectionState("disconnected");
      };

      socket.onclose = () => {
        helloReceivedRef.current = false;
        setConnectionState("disconnected");
        if (!closingRef.current) {
          scheduleReconnect();
        }
      };
    };

    connect();

    return () => {
      closingRef.current = true;
      clearReconnect();
      for (const [jobId, waiter] of waitersRef.current) {
        window.clearTimeout(waiter.timeoutId);
        waiter.reject(new Error(`Realtime events provider closed before job ${jobId} finished`));
      }
      waitersRef.current.clear();
      const socket = socketRef.current;
      socketRef.current = null;
      if (
        socket &&
        (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)
      ) {
        socket.close();
      }
    };
  }, [queryClient, user]);

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
        }

        if (user?.role !== "admin" || connectionState !== "live") {
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
    [connectionState, queryClient, user?.role],
  );

  return <RealtimeEventsContext.Provider value={value}>{children}</RealtimeEventsContext.Provider>;
}

export { buildEventsUrl };
